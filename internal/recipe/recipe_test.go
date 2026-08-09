package recipe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiyor/lxm/internal/lxd"
	"github.com/canonical/lxd/shared/api"
)

func TestValidateEnvKeys(t *testing.T) {
	valid := map[string]string{
		"GOOD_KEY": "val",
		"_FOO123":  "bar",
		"a_b_c":    "123",
	}
	if err := ValidateEnvKeys(valid); err != nil {
		t.Fatalf("expected valid keys to pass, got: %v", err)
	}

	invalid := map[string]string{
		"BAD-KEY": "val",
	}
	if err := ValidateEnvKeys(invalid); err == nil {
		t.Fatalf("expected hyphenated env key to fail POSIX validation")
	}

	invalidSpace := map[string]string{
		"FOO BAR": "val",
	}
	if err := ValidateEnvKeys(invalidSpace); err == nil {
		t.Fatalf("expected env key with space to fail POSIX validation")
	}
}

func TestPathQualifiedHashKey(t *testing.T) {
	key1 := PathQualifiedHashKey("recipes/db/install.sh", "")
	if key1 != "user.lxm.recipe.recipes_db_install_sh.hash" {
		t.Errorf("unexpected hash key: %q", key1)
	}

	key2 := PathQualifiedHashKey("recipes/app/install.sh", "")
	if key2 != "user.lxm.recipe.recipes_app_install_sh.hash" {
		t.Errorf("unexpected hash key: %q", key2)
	}

	if key1 == key2 {
		t.Errorf("expected distinct hash keys for scripts in different subdirectories")
	}

	namedKey := PathQualifiedHashKey("recipes/db/install.sh", "install-db")
	if namedKey != "user.lxm.recipe.install-db.hash" {
		t.Errorf("unexpected named hash key: %q", namedKey)
	}
}

func TestLoadRecipe(t *testing.T) {
	tmpDir := t.TempDir()

	shPath := filepath.Join(tmpDir, "setup.sh")
	if err := os.WriteFile(shPath, []byte("#!/bin/bash\necho hello"), 0755); err != nil {
		t.Fatalf("writing shPath: %v", err)
	}

	rSh, err := LoadRecipe("setup.sh", tmpDir)
	if err != nil {
		t.Fatalf("loading sh recipe: %v", err)
	}
	if !rSh.IsSnapshotEnabled() {
		t.Errorf("expected default snapshot enabled for sh recipe")
	}
	if rSh.GetRunAs() != "root" {
		t.Errorf("expected default runAs root, got %q", rSh.GetRunAs())
	}

	yamlPath := filepath.Join(tmpDir, "install.yaml")
	yamlContent := `schema: lxm/recipe/v1
name: install-mise
run_as: superuser
env:
  MISE_DIR: /opt/mise
sudo: true
snapshot: true
retries: 2
scripts:
  - setup.sh
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("writing yamlPath: %v", err)
	}

	rYaml, err := LoadRecipe("install.yaml", tmpDir)
	if err != nil {
		t.Fatalf("loading yaml recipe: %v", err)
	}
	if rYaml.Name != "install-mise" {
		t.Errorf("expected name install-mise, got %q", rYaml.Name)
	}
	if rYaml.GetRunAs() != "superuser" {
		t.Errorf("expected run_as superuser, got %q", rYaml.GetRunAs())
	}
	if rYaml.Retries != 2 {
		t.Errorf("expected 2 retries, got %d", rYaml.Retries)
	}
	if len(rYaml.Scripts) != 1 || rYaml.Scripts[0] != "setup.sh" {
		t.Errorf("unexpected scripts: %v", rYaml.Scripts)
	}
}

func TestExecuteRecipeScript_RetryAndEnv(t *testing.T) {
	tmpDir := t.TempDir()
	shPath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(shPath, []byte("echo testing"), 0755); err != nil {
		t.Fatalf("writing test.sh: %v", err)
	}

	fakeSvc := lxd.NewFakeInstanceServer()
	fakeSvc.Instances["c1"] = &api.Instance{Name: "c1", Status: "Running", StatusCode: api.Running}

	attempts := 0
	fakeSvc.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		attempts++
		if attempts == 1 {
			return lxd.ExecResult{ExitCode: 1, Stdout: "", Stderr: "transient error"}, nil
		}
		return lxd.ExecResult{ExitCode: 0, Stdout: "success", Stderr: ""}, nil
	}

	env := map[string]string{"FOO": "BAR"}
	res, hash, err := ExecuteRecipeScript(fakeSvc, "c1", "test.sh", tmpDir, "root", env, 2)
	if err != nil {
		t.Fatalf("expected successful execution after retry, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 execution attempts, got %d", attempts)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if hash == "" {
		t.Errorf("expected non-empty script hash")
	}
}

func TestValidateRecipeSchema_POSIXEnvKeys(t *testing.T) {
	validYAML := []byte(`schema: lxm/recipe/v1
name: test
env:
  GOOD_KEY: "value"
scripts:
  - setup.sh
`)
	if err := ValidateRecipeSchema(validYAML); err != nil {
		t.Errorf("expected valid CUE recipe schema to pass, got: %v", err)
	}

	invalidHyphenYAML := []byte(`schema: lxm/recipe/v1
name: test
env:
  BAD-KEY: "value"
scripts:
  - setup.sh
`)
	if err := ValidateRecipeSchema(invalidHyphenYAML); err == nil {
		t.Errorf("expected CUE schema validation to fail for hyphenated key BAD-KEY")
	}

	invalidSpaceYAML := []byte(`schema: lxm/recipe/v1
name: test
env:
  "BAD KEY": "value"
scripts:
  - setup.sh
`)
	if err := ValidateRecipeSchema(invalidSpaceYAML); err == nil {
		t.Errorf("expected CUE schema validation to fail for spaced key BAD KEY")
	}
}
