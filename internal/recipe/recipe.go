package recipe

import (
	"context"
	"crypto/sha256"

	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/encoding/yaml"
	"github.com/aiyor/lxm/internal/lxd"
	goyaml "gopkg.in/yaml.v3"
)

//go:embed schemas/recipe_v1.cue
var recipeSchemaBytes []byte

var posixEnvRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// RecipeMetadata describes an optional recipe specification file (lxm/recipe/v1).
type RecipeMetadata struct {
	Schema   string            `yaml:"schema,omitempty" json:"schema,omitempty"`
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	RunAs    string            `yaml:"run_as,omitempty" json:"run_as,omitempty"`
	RunAsAlt string            `yaml:"run-as,omitempty" json:"run-as,omitempty"`
	Env      map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Sudo     bool              `yaml:"sudo" json:"sudo"`
	Snapshot *bool             `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`
	Retries  int               `yaml:"retries" json:"retries"`
	Scripts  []string          `yaml:"scripts" json:"scripts"`
	Path     string            `yaml:"-" json:"path,omitempty"`
}

// IsSnapshotEnabled returns true unless snapshot is explicitly set to false.
func (r *RecipeMetadata) IsSnapshotEnabled() bool {
	if r.Snapshot == nil {
		return true
	}
	return *r.Snapshot
}

// GetRunAs returns the effective run_as user.
func (r *RecipeMetadata) GetRunAs() string {
	if r.RunAs != "" {
		return r.RunAs
	}
	if r.RunAsAlt != "" {
		return r.RunAsAlt
	}
	return "root"
}

// ValidateEnvKeys asserts that all environment keys match the POSIX regex ^[a-zA-Z_][a-zA-Z0-9_]*$.
func ValidateEnvKeys(env map[string]string) error {
	for k := range env {
		if !posixEnvRegex.MatchString(k) {
			return fmt.Errorf("invalid POSIX environment variable key %q", k)
		}
	}
	return nil
}

// ValidateRecipeSchema validates recipe raw YAML bytes against the lxm/recipe/v1 CUE schema.
func ValidateRecipeSchema(data []byte) error {
	ctx := cuecontext.New()
	val := ctx.CompileBytes(recipeSchemaBytes)
	if val.Err() != nil {
		return fmt.Errorf("compile recipe cue schema: %w", val.Err())
	}
	schema := val.LookupPath(cue.ParsePath("#LXM_RECIPE_V1"))
	file, err := yaml.Extract("recipe.yaml", data)
	if err != nil {
		return fmt.Errorf("yaml parse recipe: %w", err)
	}
	parsed := ctx.BuildFile(file)
	res := schema.Unify(parsed)
	if err := res.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("recipe schema validation failed: %w", err)
	}
	return nil
}

// PathQualifiedHashKey generates a deterministic, path-qualified metadata key for recipe idempotency (F10).
func PathQualifiedHashKey(scriptPath string, recipeName string) string {
	if recipeName != "" {
		return "user.lxm.recipe." + recipeName + ".hash"
	}
	clean := filepath.Clean(scriptPath)
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.ReplaceAll(clean, "/", "_")
	clean = strings.ReplaceAll(clean, ".", "_")
	clean = strings.ReplaceAll(clean, "-", "_")
	return "user.lxm.recipe." + clean + ".hash"
}

// LoadRecipe loads a recipe from a script file or a metadata YAML manifest.
func LoadRecipe(path string, baseDir string) (*RecipeMetadata, error) {
	target := path
	if baseDir != "" && !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, path)
	}

	target = filepath.Clean(target)
	ext := strings.ToLower(filepath.Ext(target))
	if ext == ".yaml" || ext == ".yml" {
		//nolint:gosec // G304: target is cleaned recipe file path relative to base directory
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("reading recipe metadata %q: %w", target, err)
		}

		if err := ValidateRecipeSchema(data); err != nil {
			return nil, fmt.Errorf("validating recipe metadata %q: %w", target, err)
		}

		var meta RecipeMetadata
		if err := goyaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parsing recipe metadata %q: %w", target, err)
		}

		if err := ValidateEnvKeys(meta.Env); err != nil {
			return nil, fmt.Errorf("recipe metadata %q: %w", target, err)
		}

		meta.Path = path
		// Resolve script paths relative to the metadata YAML file's directory (H3)
		yamlDir := filepath.Dir(path)
		for i, s := range meta.Scripts {
			if !filepath.IsAbs(s) && yamlDir != "." && yamlDir != "" {
				meta.Scripts[i] = filepath.Join(yamlDir, s)
			}
		}
		return &meta, nil
	}

	// Plain script file path (.sh or executable)
	snap := true
	return &RecipeMetadata{
		Name:     "",
		RunAs:    "root",
		Snapshot: &snap,
		Retries:  0,
		Scripts:  []string{path},
		Path:     path,
	}, nil
}

// ComputeScriptHash computes the SHA256 content hash of a recipe script file.
func ComputeScriptHash(scriptPath string, baseDir string) (string, error) {
	target := scriptPath
	if baseDir != "" && !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, scriptPath)
	}
	target = filepath.Clean(target)

	//nolint:gosec // G304: target is cleaned recipe file path relative to base directory
	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("reading script file %q: %w", target, err)
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// ExecuteRecipeScript runs a script inside a container with POSIX env map and retry policy.
func ExecuteRecipeScript(svc lxd.InstanceService, containerName string, scriptPath string, baseDir string, runAs string, env map[string]string, retries int) (lxd.ExecResult, string, error) {
	return ExecuteRecipeScriptContext(context.Background(), svc, containerName, scriptPath, baseDir, runAs, env, retries)
}

// ExecuteRecipeScriptContext runs a script inside a container with POSIX env map, retry policy, and context cancellation.
func ExecuteRecipeScriptContext(ctx context.Context, svc lxd.InstanceService, containerName string, scriptPath string, baseDir string, runAs string, env map[string]string, retries int) (lxd.ExecResult, string, error) {
	if err := ValidateEnvKeys(env); err != nil {
		return lxd.ExecResult{ExitCode: 3}, "", err
	}

	hash, err := ComputeScriptHash(scriptPath, baseDir)
	if err != nil {
		return lxd.ExecResult{ExitCode: 3}, "", err
	}

	target := scriptPath
	if baseDir != "" && !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, scriptPath)
	}
	target = filepath.Clean(target)

	//nolint:gosec // G304: target is cleaned recipe file path relative to base directory
	scriptBytes, err := os.ReadFile(target)
	if err != nil {
		return lxd.ExecResult{ExitCode: 3}, "", err
	}

	uid, err := svc.ResolveUID(containerName, runAs)
	if err != nil {
		if runAs != "root" && runAs != "" {
			return lxd.ExecResult{ExitCode: 6}, "", fmt.Errorf("failed to resolve UID for user %q in container %q: %w", runAs, containerName, err)
		}
		uid = 0
	}

	// Execute command via /bin/bash -l -c
	cmd := []string{"/bin/bash", "-l", "-c", string(scriptBytes)}

	attempts := retries + 1
	var lastRes lxd.ExecResult
	var lastErr error

	for i := 0; i < attempts; i++ {
		select {
		case <-ctx.Done():
			return lxd.ExecResult{ExitCode: 1, Stderr: "recipe execution cancelled by user interrupt"}, hash, ctx.Err()
		default:
		}

		res, execErr := svc.ExecInstanceContext(ctx, containerName, cmd, uid, env)
		lastRes = res
		lastErr = execErr
		if execErr == nil && res.ExitCode == 0 {
			return res, hash, nil
		}
	}

	if lastErr != nil {
		return lastRes, hash, lastErr
	}
	return lastRes, hash, fmt.Errorf("recipe script %q failed with exit code %d after %d attempt(s)", scriptPath, lastRes.ExitCode, attempts)
}
