package lxm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiyor/lxm/internal/config"
)

func (m *Manager) ExecuteRecipes(conf *config.Config, configBaseDir string) error {
	m.logger.Info("Executing recipes", "name", conf.Name, "groups", len(conf.Recipes))

	for _, group := range conf.Recipes {
		runAs := group.RunAs
		if runAs == "" {
			runAs = conf.User
		}

		for _, scriptPath := range group.Scripts {
			if err := m.ExecuteSingleScript(conf.Name, scriptPath, runAs, configBaseDir); err != nil {
				return err
			}
		}
	}

	m.logger.Info("All recipes completed", "name", conf.Name)
	return nil
}

func (m *Manager) ExecuteSingleScript(containerName string, scriptPath string, runAs string, configBaseDir string) error {
	return m.executeScript(containerName, scriptPath, runAs, configBaseDir, true, nil)
}

func (m *Manager) ExecuteSingleScriptWithEnv(containerName string, scriptPath string, runAs string, configBaseDir string, customEnv map[string]string) error {
	return m.executeScript(containerName, scriptPath, runAs, configBaseDir, true, customEnv)
}

func (m *Manager) ExecuteScriptOnDemand(containerName string, scriptPath string, runAs string) error {
	return m.executeScript(containerName, scriptPath, runAs, "", false, nil)
}

func (m *Manager) executeScript(containerName string, scriptPath string, runAs string, configBaseDir string, track bool, customEnv map[string]string) error {
	ctx := context.Background()
	resolvedPath := scriptPath
	if !filepath.IsAbs(resolvedPath) && configBaseDir != "" {
		resolvedPath = filepath.Join(configBaseDir, resolvedPath)
	}

	scriptData, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("reading script %q: %w", resolvedPath, err)
	}

	scriptName := filepath.Base(resolvedPath)
	var configKey string
	var scriptHash string

	if track {
		h := sha256.New()
		h.Write(scriptData)
		scriptHash = hex.EncodeToString(h.Sum(nil))

		configKey = fmt.Sprintf("user.lxm.recipe.%s.hash", scriptName)
		instance, _, err := m.client.GetInstance(ctx, containerName)
		if err == nil {
			currentHash := instance.Config[configKey]
			if currentHash == scriptHash && !m.force {
				m.logger.Info("Script already applied, skipping", "name", containerName, "script", scriptName)
				return nil
			}
		}
	}

	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return fmt.Errorf("generating random suffix: %w", err)
	}
	suffix := hex.EncodeToString(randBytes)
	remotePath := fmt.Sprintf("/tmp/lxm-script-%s-%s", suffix, scriptName)

	m.logger.Info("Executing script", "name", containerName, "script", scriptName, "run_as", runAs)

	if m.dryRun {
		m.logger.Info("Dry-run: would execute script", "name", containerName, "script", scriptName, "run_as", runAs)
		return nil
	}

	// Resolve the target user's profile inside the container.
	uenv, err := m.client.ResolveUserEnv(ctx, containerName, runAs)
	if err != nil {
		return fmt.Errorf("resolving user %q environment: %w", runAs, err)
	}

	err = m.client.CreateInstanceFile(ctx, containerName, remotePath, bytes.NewReader(scriptData), 0755, 0, 0)
	if err != nil {
		return fmt.Errorf("uploading script %q: %w", scriptName, err)
	}
	defer func() { _ = m.client.DeleteInstanceFile(ctx, containerName, remotePath) }()

	env := uenv.DefaultEnv()
	if env == nil {
		env = make(map[string]string)
	}
	for k, v := range customEnv {
		env[k] = v
	}

	// Determine if the script needs a specific alternative interpreter natively
	firstLine := ""
	if len(scriptData) > 0 {
		parts := strings.SplitN(string(scriptData), "\n", 2)
		firstLine = strings.TrimSpace(parts[0])
	}

	interpreterCmd := "/bin/bash"
	if strings.HasPrefix(firstLine, "#!") {
		shebang := strings.TrimSpace(firstLine[2:])
		if shebang != "" {
			interpreterCmd = shebang
		}
	}

	// We use bash -l to source ~/.profile, then "exec <interpreter>" to replace the
	// bash process and execute the script directly. This securely bypasses the ~/.bash_logout
	// script hook (which fails without a TTY), AND elegantly bypasses `noexec` mounts
	// on /tmp because the executable binary is the interpreter, not the temp file!
	cmdStr := fmt.Sprintf("exec %s %s", interpreterCmd, remotePath)
	cmd := []string{"/bin/bash", "-l", "-c", cmdStr}

	res, err := m.client.ExecInstance(ctx, containerName, cmd, uenv.UID, env)
	exitCode, output := res.ExitCode, res.Combined()
	if m.debug && output != "" {
		m.logger.Debug("Command output", "script", scriptName, "output", output)
	}
	if err != nil {
		return fmt.Errorf("executing %q: %w", scriptName, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("script %q exited with code %d:\n%s", scriptName, exitCode, output)
	}

	if track {
		instance, etag, err := m.client.GetInstance(ctx, containerName)
		if err != nil {
			return fmt.Errorf("getting container %q for hash update: %w", containerName, err)
		}
		if instance.Config == nil {
			instance.Config = make(map[string]string)
		}
		instance.Config[configKey] = scriptHash
		if err := m.client.UpdateInstance(ctx, containerName, instance.Writable(), etag); err != nil {
			return fmt.Errorf("updating script hash for %q: %w", scriptName, err)
		}
	}

	m.logger.Info("Script executed successfully", "name", containerName, "script", scriptName)
	return nil
}
