package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrateManifest_BasicV1ToV2(t *testing.T) {
	v1YAML := `
name: test-container
wait: true
image: ubuntu:22.04
recipes:
  - run_as: root
    scripts:
      - install.sh
`
	migrated, warnings, err := MigrateManifest([]byte(v1YAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}

	str := string(migrated)
	if !strings.Contains(str, "schema: lxm/config/v2") {
		t.Errorf("expected schema: lxm/config/v2 in migrated output:\n%s", str)
	}
	if !strings.Contains(str, "required: true") {
		t.Errorf("expected wait.required: true in migrated output:\n%s", str)
	}
	if len(warnings) == 0 {
		t.Errorf("expected warnings for default flips (sudo, inject_ssh_keys)")
	}
}

func TestMigrateManifest_WaitBool_NoStaleTag(t *testing.T) {
	// B1: wait: bool must compile to a plain {required: bool} mapping, not a
	// mapping tagged !!bool, which strict YAML parsers reject. YAML bool case
	// variants (TRUE/True) and quoted strings must canonicalize the same way.
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "wait: false", want: "required: false"},
		{in: "wait: true", want: "required: true"},
		{in: "wait: TRUE", want: "required: true"},
		{in: "wait: False", want: "required: false"},
		{in: `wait: "true"`, want: "required: true"},
	} {
		migrated, _, err := MigrateManifest([]byte("name: test-container\n" + tc.in + "\n"))
		if err != nil {
			t.Fatalf("MigrateManifest(%q) failed: %v", tc.in, err)
		}
		str := string(migrated)
		if strings.Contains(str, "!!bool") {
			t.Errorf("compiled output retains stale !!bool tag:\n%s", str)
		}
		if !strings.Contains(str, tc.want) {
			t.Errorf("expected %q in compiled output:\n%s", tc.want, str)
		}
		// Round-trip through a strict decoder: wait must decode as a mapping
		// with a boolean required, not fail or come back empty.
		var doc struct {
			Wait struct {
				Required *bool `yaml:"required"`
			} `yaml:"wait"`
		}
		if err := yaml.Unmarshal(migrated, &doc); err != nil {
			t.Fatalf("compiled output failed to round-trip: %v\n%s", err, str)
		}
		if doc.Wait.Required == nil {
			t.Errorf("expected wait.required to decode as bool, got nil:\n%s", str)
		}
	}
}

func TestMigrateManifest_ConflictError(t *testing.T) {
	conflictYAML := `
name: test-container
cloud-init: "user-data"
cloud-init-file: "/tmp/user-data"
`
	_, _, err := MigrateManifest([]byte(conflictYAML))
	if err == nil {
		t.Fatalf("expected error for cloud-init + cloud-init-file conflict, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_ERROR_CONFLICT") {
		t.Errorf("expected CONFIG_ERROR_CONFLICT error, got %v", err)
	}
}

// TestMigrateManifest_TemplateExpansion covers UG5 B3: compile must accept
// exactly what plan/apply accept, so MigrateManifest expands {{ .Env.* }},
// {{ .Vars.* }}, and {{ .Name }} before migration/CUE validation. Before the
// fix, an env-templated `image` failed the #ImageRef pattern even with the
// variable bound, contradicting environment-variables.md and the "Emit
// resolved v2 manifests" description.
func TestMigrateManifest_TemplateExpansion(t *testing.T) {
	t.Run("bound env template expands and validates", func(t *testing.T) {
		t.Setenv("LXM_TEST_IMAGE", "ubuntu:24.04")
		migrated, _, err := MigrateManifest([]byte(`
schema: lxm/config/v2
name: box1
status: present
image: '{{ .Env.LXM_TEST_IMAGE }}'
groups: [dev]
`))
		if err != nil {
			t.Fatalf("MigrateManifest with bound env template failed: %v", err)
		}
		if !strings.Contains(string(migrated), "ubuntu:24.04") {
			t.Errorf("expected expanded alias in compiled output:\n%s", migrated)
		}
		if strings.Contains(string(migrated), "{{") {
			t.Errorf("compiled output still contains a template:\n%s", migrated)
		}
	})

	t.Run("unbound env template fails exit-3 style error", func(t *testing.T) {
		_, _, err := MigrateManifest([]byte(`
schema: lxm/config/v2
name: box1
status: present
image: '{{ .Env.LXM_UG5_NEVER_SET }}'
groups: [dev]
`))
		if err == nil {
			t.Fatal("expected error for unbound env template")
		}
		if !strings.Contains(err.Error(), "unbound environment variable") {
			t.Errorf("expected unbound-variable message, got %v", err)
		}
	})

	t.Run("vars template expands", func(t *testing.T) {
		migrated, _, err := MigrateManifest([]byte(`
schema: lxm/config/v2
name: box1
status: present
image: ubuntu:24.04
vars:
  workspace: /tmp/projects
mounts:
  - source: "{{ .Vars.workspace }}"
    path: /workspace
groups: [dev]
`))
		if err != nil {
			t.Fatalf("MigrateManifest with vars template failed: %v", err)
		}
		if !strings.Contains(string(migrated), "/tmp/projects") {
			t.Errorf("expected expanded vars value in compiled output:\n%s", migrated)
		}
	})

	t.Run("templateless manifest is unchanged", func(t *testing.T) {
		migrated, _, err := MigrateManifest([]byte(`
name: box1
image: ubuntu:22.04
status: present
`))
		if err != nil {
			t.Fatalf("MigrateManifest failed: %v", err)
		}
		if !strings.Contains(string(migrated), "lxm/config/v2") {
			t.Errorf("expected v1->v2 migration, got:\n%s", migrated)
		}
	})

	t.Run("escaped template survives the compile round-trip", func(t *testing.T) {
		// An escaped template (\{{ .Vars.other \}}) must stay escaped in the
		// compiled artifact: the loader's single pass un-escapes it to a
		// literal, so the artifact must not contain a live template that a
		// later plan/apply would substitute a second time (or fail on as
		// unbound). Regression for the expansion pass consuming the escape.
		src := []byte(`
schema: lxm/config/v2
name: box1
status: present
image: ubuntu:24.04
vars:
  other: keepme
cloud-init: |
  runcmd:
    - echo "literal \{{ .Vars.other \}}"
groups: [dev]
`)
		migrated, _, err := MigrateManifest(src)
		if err != nil {
			t.Fatalf("MigrateManifest with escaped template failed: %v", err)
		}
		out := string(migrated)
		if !strings.Contains(out, "\\{{") {
			t.Errorf("compiled output lost the escape marker:\n%s", out)
		}
		if strings.Contains(out, `echo "literal {{ .Vars.other }}`) {
			t.Errorf("compiled output contains a live template (escape consumed):\n%s", out)
		}

		// The artifact must still load: the loader un-escapes once and does
		// not try to substitute the literal.
		dir := t.TempDir()
		compiled := filepath.Join(dir, "compiled.yaml")
		if err := os.WriteFile(compiled, migrated, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(compiled)
		if err != nil {
			t.Fatalf("compiled artifact failed to load: %v\n%s", err, out)
		}
		if !strings.Contains(cfg.CloudInit, `echo "literal {{ .Vars.other }}`) {
			t.Errorf("expected the un-escaped literal in loaded cloud-init, got: %q", cfg.CloudInit)
		}
	})
}

func TestMigrateManifest_SecurityError(t *testing.T) {
	securityYAML := `
name: test-container
mounts:
  - /host/path:/proc
`
	_, _, err := MigrateManifest([]byte(securityYAML))
	if err == nil {
		t.Fatalf("expected security error for restricted mount destination, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_ERROR_SECURITY") {
		t.Errorf("expected CONFIG_ERROR_SECURITY error, got %v", err)
	}
}

func TestMigrateManifest_EmptyRecipePruning(t *testing.T) {
	emptyRecipeYAML := `
name: test-container
image: ubuntu:22.04
recipes:
  - run_as: root
    scripts:
      - "# comment line only"
  - run_as: ubuntu
    scripts:
      - setup.sh
`
	migrated, warnings, err := MigrateManifest([]byte(emptyRecipeYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}

	hasPruneWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_EMPTY_RECIPE") {
			hasPruneWarn = true
			break
		}
	}
	if !hasPruneWarn {
		t.Errorf("expected CONFIG_WARN_EMPTY_RECIPE warning, got %v", warnings)
	}

	str := string(migrated)
	if strings.Contains(str, "run_as: root") {
		t.Errorf("expected empty recipe group to be pruned from output:\n%s", str)
	}
	if !strings.Contains(str, "setup.sh") {
		t.Errorf("expected valid recipe setup.sh to be retained:\n%s", str)
	}
}

func TestSaveMigratedFile_InPlaceAndCompiled(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "dev.yaml")
	content := []byte("name: dev\n")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	migratedContent := []byte("schema: lxm/config/v2\nname: dev\n")

	// 1. Non-destructive compile
	target, err := SaveMigratedFile(srcFile, migratedContent, false)
	if err != nil {
		t.Fatalf("SaveMigratedFile non-destructive failed: %v", err)
	}
	if !strings.Contains(target, filepath.Join(".lxm", "compiled")) {
		t.Errorf("expected output path under .lxm/compiled, got %s", target)
	}

	// 2. In-place rewrite
	inPlaceTarget, err := SaveMigratedFile(srcFile, migratedContent, true)
	if err != nil {
		t.Fatalf("SaveMigratedFile in-place failed: %v", err)
	}
	if inPlaceTarget != srcFile {
		t.Errorf("expected target path %s, got %s", srcFile, inPlaceTarget)
	}
	readBack, _ := os.ReadFile(srcFile)
	if string(readBack) != string(migratedContent) {
		t.Errorf("expected in-place file content %q, got %q", string(migratedContent), string(readBack))
	}
}

func TestMigrateManifest_RelativeMountWarning(t *testing.T) {
	v1Yaml := `name: dev-box
image: ubuntu:22.04
status: present
mounts:
  - source: ./data
    path: /mnt/data
`
	_, warnings, err := MigrateManifest([]byte(v1Yaml))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}

	foundRelWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_RELATIVE_MOUNT") {
			foundRelWarn = true
			break
		}
	}
	if !foundRelWarn {
		t.Errorf("expected CONFIG_WARN_RELATIVE_MOUNT warning for relative mount source, got %v", warnings)
	}
}

func TestMigrateManifest_CompactMounts_NoRelativeWarning(t *testing.T) {
	v1Yaml := `name: dev-box
image: ubuntu:22.04
status: present
mounts:
  - /home/user/src:/mnt/src:ro
  - source: /abs/data
    path: /mnt/data
`
	_, warnings, err := MigrateManifest([]byte(v1Yaml))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_RELATIVE_MOUNT") {
			t.Errorf("false positive CONFIG_WARN_RELATIVE_MOUNT for absolute compact/object mounts; warnings=%v", warnings)
		}
	}
}

func TestMigrateManifest_SensitiveMountAndEmptyErrors(t *testing.T) {
	// Sensitive mount source warning
	sensYAML := `name: dev-box
image: ubuntu:22.04
mounts:
  - source: /etc/shadow
    path: /mnt/shadow
`
	_, warnings, err := MigrateManifest([]byte(sensYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	foundSensWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "SECURITY_WARNING") {
			foundSensWarn = true
			break
		}
	}
	if !foundSensWarn {
		t.Errorf("expected SECURITY_WARNING for sensitive host mount source, got %v", warnings)
	}

	// Empty YAML error
	if _, _, err := MigrateManifest([]byte("")); err == nil {
		t.Errorf("expected error for empty YAML in MigrateManifest")
	}

	// Non-mapping YAML root
	if _, _, err := MigrateManifest([]byte("- item1\n- item2\n")); err == nil {
		t.Errorf("expected error for non-mapping root in MigrateManifest")
	}

	// Unsupported schema version
	unsupportedSchema := `schema: lxm/config/v999
name: dev-box
`
	if _, _, err := MigrateManifest([]byte(unsupportedSchema)); err == nil {
		t.Errorf("expected error for unsupported schema version")
	}

	// SaveMigratedFile within current working directory
	relSrc := "test_compile_relative.yaml"
	_ = os.WriteFile(relSrc, []byte("name: rel\n"), 0644)
	defer os.Remove(relSrc)
	defer os.RemoveAll(".lxm")

	targetRel, err := SaveMigratedFile(relSrc, []byte("schema: lxm/config/v2\nname: rel\n"), false)
	if err != nil {
		t.Fatalf("SaveMigratedFile relative failed: %v", err)
	}
	if !strings.HasPrefix(targetRel, filepath.Join(".lxm", "compiled")) {
		t.Errorf("expected relative compiled target path, got %s", targetRel)
	}
}

// ---------------------------------------------------------------------------
// Authoring-shorthand migration (lxm_mount_bug.md §6.4/§6.5, test plan §8)
// ---------------------------------------------------------------------------

func TestMigrateManifest_RootRecipeGroup_NotPruned(t *testing.T) {
	rootYAML := `name: root-recipe
image: ubuntu:22.04
recipes:
  - root:
      - recipes/setup.sh
`
	migrated, warnings, err := MigrateManifest([]byte(rootYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_EMPTY_RECIPE") {
			t.Fatalf("root: group with scripts must not be pruned; warnings=%v", warnings)
		}
	}
	str := string(migrated)
	if !strings.Contains(str, "recipes/setup.sh") {
		t.Errorf("root: scripts must survive compilation:\n%s", str)
	}
	if !strings.Contains(str, "run_as: root") {
		t.Errorf("expected normalized run_as: root in compiled output:\n%s", str)
	}

	// Compiled output must load (regression for §3.3)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "root-recipe.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled root-recipe failed to load: %v", err)
	}
	if len(conf.Recipes) != 1 || conf.Recipes[0].RunAs != "root" || len(conf.Recipes[0].Scripts) != 1 || conf.Recipes[0].Scripts[0] != "recipes/setup.sh" {
		t.Errorf("compiled root-recipe loaded wrong groups: %+v", conf.Recipes)
	}
}

func TestMigrateManifest_RecipeShorthands_Normalized(t *testing.T) {
	shorthandYAML := `name: sh
image: ubuntu:22.04
recipes:
  - recipes/bootstrap.sh
  - root:
      - recipes/setup.sh
  - scripts:
      - recipes/legacy.sh
`
	migrated, warnings, err := MigrateManifest([]byte(shorthandYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_EMPTY_RECIPE") {
			t.Fatalf("no shorthand group is empty; warnings=%v", warnings)
		}
	}
	str := string(migrated)
	if got := strings.Count(str, "run_as: root"); got != 3 {
		t.Errorf("expected 3 normalized run_as: root groups, got %d:\n%s", got, str)
	}
	for _, script := range []string{"recipes/bootstrap.sh", "recipes/setup.sh", "recipes/legacy.sh"} {
		if !strings.Contains(str, script) {
			t.Errorf("script %s missing from compiled output:\n%s", script, str)
		}
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "sh.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled shorthand recipes failed to load: %v", err)
	}
	if len(conf.Recipes) != 3 {
		t.Fatalf("expected 3 recipe groups, got %d", len(conf.Recipes))
	}
	for i, rg := range conf.Recipes {
		if rg.RunAs != "root" || len(rg.Scripts) != 1 {
			t.Errorf("group %d not normalized: %+v", i, rg)
		}
	}
}

func TestMigrateManifest_MapFormMounts_Normalized(t *testing.T) {
	mapYAML := `name: map-mounts
image: ubuntu:22.04
mounts:
  /var/log: /tmp/host-logs
  /srv/app:
    source: /tmp/app-src
    readonly: true
`
	migrated, _, err := MigrateManifest([]byte(mapYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	str := string(migrated)
	if !strings.Contains(str, "source: /tmp/host-logs") || !strings.Contains(str, "path: /var/log") {
		t.Errorf("expected normalized object-form mounts:\n%s", str)
	}
	if !strings.Contains(str, "readonly: true") {
		t.Errorf("expected readonly flag preserved:\n%s", str)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "map.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled map-form mounts failed to load: %v", err)
	}
	if len(conf.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(conf.Mounts))
	}
	if conf.Mounts[0].Source != "/tmp/host-logs" || conf.Mounts[0].Path != "/var/log" {
		t.Errorf("scalar map mount wrong: %+v", conf.Mounts[0])
	}
	if conf.Mounts[1].Source != "/tmp/app-src" || conf.Mounts[1].Path != "/srv/app" || !conf.Mounts[1].Readonly {
		t.Errorf("object map mount wrong: %+v", conf.Mounts[1])
	}
}

func TestMigrateManifest_MixedScriptList_CompiledOutputLoads(t *testing.T) {
	// A group with a comment entry plus a real script is kept by Transform 8
	// but the comment must be filtered from compiled output so the artifact
	// still passes its own load path (compile/plan parity, review round 3).
	mixedYAML := `name: mixed
image: ubuntu:22.04
recipes:
  - run_as: root
    scripts:
      - "# header comment"
      - recipes/real.sh
  - scripts:
      - recipes/other.sh
      - "# trailing note"
`
	migrated, warnings, err := MigrateManifest([]byte(mixedYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_EMPTY_RECIPE") {
			t.Fatalf("mixed group must not be pruned; warnings=%v", warnings)
		}
	}
	str := string(migrated)
	if strings.Contains(str, "header comment") || strings.Contains(str, "trailing note") {
		t.Errorf("comment entries must be filtered from compiled output:\n%s", str)
	}
	if !strings.Contains(str, "recipes/real.sh") || !strings.Contains(str, "recipes/other.sh") {
		t.Errorf("real scripts must survive compilation:\n%s", str)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "mixed.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled mixed-group output failed to load: %v", err)
	}
	if len(conf.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups, got %d", len(conf.Recipes))
	}
	for i, rg := range conf.Recipes {
		if len(rg.Scripts) != 1 {
			t.Errorf("group %d: expected 1 filtered script, got %+v", i, rg)
		}
	}
}

func TestMigrateManifest_ExternallyReferencedRecipeAnchor(t *testing.T) {
	// An anchor on a root:-shorthand group referenced from outside the
	// recipes list (replace.recipes) must survive Transform 9's constructed
	// nodes; stripping it would dangle the alias and fail compile.
	aliasYAML := `name: ext-root
image: ubuntu:22.04
recipes:
  - &g
    root:
      - recipes/setup.sh
replace:
  recipes:
    - *g
`
	migrated, _, err := MigrateManifest([]byte(aliasYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	str := string(migrated)
	if !strings.Contains(str, "&g") || !strings.Contains(str, "*g") {
		t.Errorf("externally referenced anchor must be retained:\n%s", str)
	}
}

func TestMigrateManifest_RecipeGroupAliases_NotPruned(t *testing.T) {
	// Anchored recipe groups referenced by alias must survive compilation:
	// pre-fix Transform 8 pruned the aliased copy as "empty" (data loss with
	// --in-place), while the loader kept both groups.
	aliasYAML := `name: alias-recipes
image: ubuntu:22.04
recipes:
  - &grp
    run_as: root
    scripts:
      - recipes/setup.sh
  - *grp
`
	migrated, warnings, err := MigrateManifest([]byte(aliasYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_EMPTY_RECIPE") {
			t.Fatalf("aliased recipe groups with scripts must not be pruned; warnings=%v", warnings)
		}
	}
	str := string(migrated)
	if got := strings.Count(str, "recipes/setup.sh"); got != 2 {
		t.Errorf("expected both aliased groups' scripts in compiled output, got %d:\n%s", got, str)
	}
	if got := strings.Count(str, "run_as: root"); got != 2 {
		t.Errorf("expected 2 normalized groups, got %d:\n%s", got, str)
	}
	// The alias reference must be consumed; a single dead `&grp` anchor
	// annotation is retained (referenced by the consumed alias) and is valid
	// YAML — never a duplicate definition.
	if strings.Contains(str, "*grp") {
		t.Errorf("compiled output must not contain alias references:\n%s", str)
	}
	if strings.Count(str, "&grp") > 1 {
		t.Errorf("compiled output must not define the anchor twice:\n%s", str)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "alias.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled aliased recipes failed to load: %v", err)
	}
	if len(conf.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups, got %d", len(conf.Recipes))
	}
}

func TestMigrateManifest_MapFormMounts_AliasValues(t *testing.T) {
	// YAML alias values in map-form mounts must survive compilation: the
	// pre-fix Transform 9 silently dropped aliased mounts (data loss with
	// --in-place, lxm_mount_bug.md §5 CRITICAL class).
	aliasYAML := `name: alias-mounts
image: ubuntu:22.04
mounts:
  /q: &s
    source: /tmp/app-src
    path: /srv/app
  /r: *s
  /a: &src /tmp/shared-src
  /b: *src
`
	migrated, _, err := MigrateManifest([]byte(aliasYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	str := string(migrated)
	// Mapping alias resolved into two mounts; scalar alias resolved into two sources.
	if strings.Count(str, "path: /srv/app") != 2 {
		t.Errorf("expected both aliased mapping mounts in compiled output:\n%s", str)
	}
	if strings.Count(str, "source: /tmp/shared-src") != 2 {
		t.Errorf("expected both aliased scalar sources in compiled output:\n%s", str)
	}
	if strings.Contains(str, "&s") || strings.Contains(str, "*s") ||
		strings.Contains(str, "&src") || strings.Contains(str, "*src") {
		t.Errorf("compiled output must not leak YAML anchor annotations:\n%s", str)
	}

	// Two mounts with the identical destination (the aliased mapping) must be
	// rejected post-merge — not silently collapsed or dropped.
	dir := t.TempDir()
	outPath := filepath.Join(dir, "alias.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(outPath); err == nil {
		t.Errorf("expected duplicate mount path rejection for aliased identical mounts")
	}
}

func TestMigrateManifest_MapFormMounts_ObjectWithoutPath(t *testing.T) {
	// Pathless object value: Transform 9 prepends the map key as path, and
	// the compiled output must load (compile/plan parity for #MountMapObjAuthoring).
	mapYAML := `name: pathless
image: ubuntu:22.04
mounts:
  /srv/app:
    source: /tmp/app-src
`
	migrated, _, err := MigrateManifest([]byte(mapYAML))
	if err != nil {
		t.Fatalf("MigrateManifest failed: %v", err)
	}
	str := string(migrated)
	if !strings.Contains(str, "path: /srv/app") || !strings.Contains(str, "source: /tmp/app-src") {
		t.Errorf("expected normalized pathless map mount:\n%s", str)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "pathless.yaml")
	if err := os.WriteFile(outPath, migrated, 0644); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("compiled pathless map mount failed to load: %v", err)
	}
	if len(conf.Mounts) != 1 || conf.Mounts[0].Path != "/srv/app" {
		t.Errorf("expected path from map key, got %+v", conf.Mounts)
	}
}

func TestMigrateManifest_MapFormMount_ProcDestination_Rejected(t *testing.T) {
	mapYAML := `name: m
image: ubuntu:22.04
mounts:
  /proc: /tmp/host-logs
`
	_, _, err := MigrateManifest([]byte(mapYAML))
	if err == nil {
		t.Fatalf("expected CONFIG_ERROR_SECURITY for /proc map destination, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_ERROR_SECURITY") {
		t.Errorf("expected CONFIG_ERROR_SECURITY error, got %v", err)
	}
}

func TestMigrateManifest_ScriptsOnlyGroup_Migrates(t *testing.T) {
	v1YAML := `name: v1-scripts
image: ubuntu:22.04
user: superuser
status: present
recipes:
  - scripts:
      - recipes/install-mise.sh
`
	migrated, _, err := MigrateManifest([]byte(v1YAML))
	if err != nil {
		t.Fatalf("scripts-only v1 group must migrate, got: %v", err)
	}
	str := string(migrated)
	if !strings.Contains(str, "run_as: root") {
		t.Errorf("expected run_as: root inserted into scripts-only group:\n%s", str)
	}
	if !strings.Contains(str, "recipes/install-mise.sh") {
		t.Errorf("scripts must survive migration:\n%s", str)
	}
}

func TestMigrateManifest_FleetConfigs_CompileAndLoad(t *testing.T) {
	fleetFiles := []string{"omp.yaml", "glm.yaml", "dev-station.yaml", "agent01.yaml"}
	for _, name := range fleetFiles {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "config", name))
			if err != nil {
				t.Skipf("fleet config %s not present: %v", name, err)
			}
			migrated, _, err := MigrateManifest(raw)
			if err != nil {
				t.Fatalf("MigrateManifest(%s) failed (exit 3 regression §3.4): %v", name, err)
			}
			// The scripts-only groups must survive migration with their scripts.
			str := string(migrated)
			for _, script := range []string{"install-mise", "install-pyenv", "install-cloud-sdks"} {
				if strings.Contains(string(raw), script) && !strings.Contains(str, script) {
					t.Errorf("%s: script %q lost during migration", name, script)
				}
			}
			// Migrated output must load via LoadConfig (regression for §3.4).
			dir := t.TempDir()
			outPath := filepath.Join(dir, name)
			if err := os.WriteFile(outPath, migrated, 0644); err != nil {
				t.Fatal(err)
			}
			conf, err := LoadConfig(outPath)
			if err != nil {
				t.Fatalf("migrated %s failed to load: %v", name, err)
			}
			for i, rg := range conf.Recipes {
				if len(rg.Scripts) == 0 {
					t.Errorf("migrated %s recipe group %d has no scripts: %+v", name, i, rg)
				}
			}
		})
	}
}
