package common

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aiyor/lxm/internal/provider"
)

// ExecFn represents a function that executes a command inside an instance.
type ExecFn func(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error)

// ResolveUID resolves a username inside an instance to its numeric UID.
func ResolveUID(ctx context.Context, execFn ExecFn, name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}

	res, err := execFn(ctx, name, []string{"id", "-u", username}, 0, nil)
	if err != nil {
		return 0, fmt.Errorf("resolving UID for %q: %w", username, err)
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("resolving UID for %q: id exited with code %d", username, res.ExitCode)
	}

	lines := strings.Split(strings.TrimSpace(res.Combined()), "\n")
	uidStr := strings.TrimSpace(lines[0])
	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing UID %q for %q: %w", uidStr, username, err)
	}

	return uint32(uid), nil
}

// ResolveUserEnv retrieves the user's environment (/etc/passwd entry) inside an instance.
func ResolveUserEnv(ctx context.Context, execFn ExecFn, name string, username string) (*provider.UserEnv, error) {
	cmd := []string{"getent", "passwd", username}
	res, err := execFn(ctx, name, cmd, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("resolving user environment for %q: %w", username, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("user %q not found in instance %q", username, name)
	}

	parts := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(parts) < 7 {
		return nil, fmt.Errorf("malformed passwd entry for %q: %q", username, res.Stdout)
	}

	uid, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid UID in passwd entry: %w", err)
	}

	gid, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid GID in passwd entry: %w", err)
	}

	return &provider.UserEnv{
		User:  parts[0],
		UID:   uint32(uid),
		GID:   uint32(gid),
		Home:  parts[5],
		Shell: parts[6],
	}, nil
}
