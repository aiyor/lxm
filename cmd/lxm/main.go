package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/output"
	"github.com/aiyor/lxm/internal/plan"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var logLevelVar = new(slog.LevelVar)

var (
	lastComputedPlan   *plan.Plan
	lastApplyReport    *apply.ApplyReport
	lastCommandResults []output.ResultItem
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit code %d", e.code)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := runWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr, nil)
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer, svc lxd.InstanceService) int {
	return runWithContext(context.Background(), args, stdout, stderr, svc)
}

func runWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, svc lxd.InstanceService) int {
	logLevelVar.Set(slog.LevelInfo)
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevelVar}))

	lastComputedPlan = nil
	lastApplyReport = nil
	lastCommandResults = nil

	var once sync.Once
	var cachedSvc lxd.InstanceService
	var svcErr error

	getSvc := func() (lxd.InstanceService, error) {
		if svc != nil {
			return svc, nil
		}
		once.Do(func() {
			cachedSvc, svcErr = lxd.NewService()
		})
		if svcErr != nil {
			return nil, &exitError{code: 4, err: fmt.Errorf("Failed to connect to LXD: %w", svcErr)}
		}
		return cachedSvc, nil
	}

	rootCmd, opts := newRootCmd(ctx, stdout, stderr, getSvc, logger)
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	err := rootCmd.ExecuteContext(ctx)

	executedCmd, _, _ := rootCmd.Find(args)
	cmdName := "lxm"
	target := ""

	if executedCmd != nil && executedCmd != rootCmd {
		cmdName = executedCmd.Name()
		parsedArgs := executedCmd.Flags().Args()
		if len(parsedArgs) > 0 {
			target = parsedArgs[0]
		}
	}

	exitCode := 0
	var finalErr error

	if ctx.Err() != nil {
		exitCode = 1
		finalErr = ctx.Err()
		fmt.Fprintf(stderr, "Error: operation interrupted: %v\n", ctx.Err())
	} else if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			exitCode = ee.code
			finalErr = ee.err
			if ee.err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", ee.err)
			}
		} else {
			exitCode = 2
			finalErr = err
			fmt.Fprintf(stderr, "Error: %v\nRun 'lxm --help' for usage.\n", err)
		}
	}

	fmtLower := strings.ToLower(opts.format)
	if fmtLower != "" && fmtLower != "text" && fmtLower != "json" {
		if exitCode == 0 {
			exitCode = 2
			finalErr = fmt.Errorf("invalid format %q, expected text|json", opts.format)
			fmt.Fprintf(stderr, "Error: %v\n", finalErr)
		}
	}

	isJSONFormat := fmtLower == "json"
	if !isJSONFormat {
		for i, arg := range args {
			if arg == "--format=json" || (arg == "--format" && i+1 < len(args) && strings.ToLower(args[i+1]) == "json") {
				isJSONFormat = true
				break
			}
		}
	}

	isHelpOrCompletion := false
	if executedCmd == rootCmd && err == nil {
		isHelpOrCompletion = true
	} else if executedCmd != nil {
		cmdPath := executedCmd.CommandPath()
		cName := executedCmd.Name()
		if strings.Contains(cmdPath, "help") || strings.Contains(cmdPath, "completion") || cName == "help" || cName == "completion" || executedCmd.Flags().Changed("help") {
			isHelpOrCompletion = true
		}
	}

	if isJSONFormat && !isHelpOrCompletion {
		env := output.NewEnvelope(cmdName, target)

		if lastComputedPlan != nil {
			env.Plan = output.PlanSummary{
				Summary: map[string]int{
					"create":   lastComputedPlan.Summary.Create,
					"update":   lastComputedPlan.Summary.Update,
					"recreate": lastComputedPlan.Summary.Recreate,
					"delete":   lastComputedPlan.Summary.Delete,
					"start":    lastComputedPlan.Summary.Start,
					"stop":     lastComputedPlan.Summary.Stop,
					"noop":     lastComputedPlan.Summary.Noop,
				},
				Steps: lastComputedPlan.Steps,
			}
		}

		if lastApplyReport != nil {
			for _, r := range lastApplyReport.Results {
				env.Results = append(env.Results, output.ResultItem{
					Container:  r.Container,
					Action:     r.Action,
					Changed:    r.Changed,
					OK:         r.OK,
					DurationMS: r.DurationMS,
				})
			}
		}

		if len(lastCommandResults) > 0 {
			env.Results = append(env.Results, lastCommandResults...)
		}

		if exitCode != 0 {
			if ctx.Err() == nil && lastApplyReport != nil && len(lastApplyReport.Errors) > 0 {
				// Propagate the executor's structured errors (UG5 B1): the
				// per-container ErrorInfo list carries the code, container,
				// message, and the retryable flag computed by
				// ClassifyLXDError (e.g. ETag drift -> retryable: true).
				// Rebuilding from the single exit error here would drop the
				// container field and hardcode retryable: false, breaking
				// the documented "retryable on drift" contract. On an
				// interrupt (ctx.Err() != nil) the exit code is forced to 1
				// and the report's per-container codes would violate the
				// code-to-exit mapping, so that path keeps the single
				// INTERNAL_ERROR entry via SetExitCode.
				env.ExitCode = exitCode
				env.OK = false
				for _, rerr := range lastApplyReport.Errors {
					env.Errors = append(env.Errors, output.ErrorInfo{
						Code:      rerr.Code,
						Container: rerr.Container,
						Message:   rerr.Message,
						Retryable: rerr.Retryable,
					})
				}
			} else {
				env.SetExitCode(exitCode, finalErr, "", false)
			}
		}
		_ = output.Emit(stdout, "json", env)
	}

	return exitCode
}
