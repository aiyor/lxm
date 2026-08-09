package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Emit renders the Envelope to stdout/writer in the requested format ("text" or "json").
func Emit(w io.Writer, format string, env *Envelope) error {
	if strings.ToLower(format) == "json" {
		data, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling result envelope: %w", err)
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}

	// Human-readable text format fallback
	if !env.OK {
		if len(env.Errors) > 0 {
			for _, errInfo := range env.Errors {
				if errInfo.Container != "" {
					fmt.Fprintf(w, "Error [%s] (%s): %s\n", errInfo.Code, errInfo.Container, errInfo.Message)
				} else {
					fmt.Fprintf(w, "Error [%s]: %s\n", errInfo.Code, errInfo.Message)
				}
			}
		} else {
			fmt.Fprintf(w, "Command %s failed with exit code %d\n", env.Command, env.ExitCode)
		}
	}

	return nil
}
