package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateConfig_NameRequired(t *testing.T) {
	conf := &Config{}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidateConfig_Defaults(t *testing.T) {
	conf := &Config{Name: "test", Image: "ubuntu:22.04"}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.Status != "present" {
		t.Errorf("expected status 'present', got %q", conf.Status)
	}
	if conf.User != "ubuntu" {
		t.Errorf("expected user 'ubuntu', got %q", conf.User)
	}
}

func TestValidateConfig_InvalidStatus(t *testing.T) {
	conf := &Config{Name: "test", Status: "invalid"}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestValidateConfig_ImageRequiredWhenPresent(t *testing.T) {
	conf := &Config{Name: "test", Status: "present"}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error when image missing and status is present")
	}
}

func TestValidateConfig_ImageNotRequiredWhenAbsent(t *testing.T) {
	conf := &Config{Name: "test", Status: "absent"}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_MountSourceMustExist(t *testing.T) {
	conf := &Config{
		Name:   "test",
		Status: "absent",
		Mounts: []Mount{{Source: "/nonexistent/path", Path: "/mnt"}},
	}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error for nonexistent mount source")
	}
}

func TestValidateConfig_MountRequiresAbsolutePaths(t *testing.T) {
	dir := t.TempDir()

	conf := &Config{
		Name:   "test",
		Status: "absent",
		Mounts: []Mount{{Source: dir, Path: "relative/path"}},
	}
	if err := conf.Validate(""); err == nil {
		t.Fatal("expected error for relative container path")
	}

	conf = &Config{
		Name:   "test",
		Status: "absent",
		Mounts: []Mount{{Source: "relative/path", Path: "/mnt"}},
	}
	if err := conf.Validate(""); err == nil {
		t.Fatal("expected error for relative source path")
	}
}

func TestValidateConfig_MountMissingField(t *testing.T) {
	conf := &Config{
		Name:   "test",
		Status: "absent",
		Mounts: []Mount{{Source: "/host", Path: ""}},
	}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error for missing mount path")
	}
}

func TestValidateConfig_ValidMount(t *testing.T) {
	dir := t.TempDir()
	conf := &Config{
		Name:   "test",
		Status: "absent",
		Mounts: []Mount{{Source: dir, Path: "/mnt"}},
	}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_InvalidIPv4(t *testing.T) {
	conf := &Config{
		Name:     "test",
		Status:   "present",
		Image:    "ubuntu:22.04",
		Networks: []NetworkConfig{{Name: "eth0", IPv4: "not-an-ip"}},
	}
	if err := conf.Validate(""); err == nil {
		t.Fatal("expected error for invalid IPv4")
	}
}

func TestValidateConfig_ValidIPv4(t *testing.T) {
	conf := &Config{
		Name:     "test",
		Status:   "present",
		Image:    "ubuntu:22.04",
		Networks: []NetworkConfig{{Name: "eth0", IPv4: "10.0.0.10"}},
	}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_CloudInitFileNotExist(t *testing.T) {
	conf := &Config{
		Name:          "test",
		Status:        "present",
		Image:         "ubuntu:22.04",
		CloudInitFile: "/nonexistent/cloud-init.yaml",
	}
	if err := conf.Validate(""); err == nil {
		t.Fatal("expected error for nonexistent cloud-init file")
	}
}

func TestValidateConfig_CloudInitFileRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ci.yaml"), []byte("packages: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf := &Config{
		Name:          "test",
		Status:        "present",
		Image:         "ubuntu:22.04",
		CloudInitFile: "ci.yaml",
	}
	if err := conf.Validate(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveCloudInit_EmptyConfig(t *testing.T) {
	conf := &Config{Name: "test"}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

func TestResolveCloudInit_Inline(t *testing.T) {
	conf := &Config{
		Name:      "test",
		User:      "ubuntu",
		CloudInit: "package_update: true\npackages:\n  - vim",
	}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "#cloud-config\n") {
		t.Error("expected #cloud-config header")
	}
	if !strings.Contains(result, "package_update: true") {
		t.Error("expected package_update in output")
	}
	if !strings.Contains(result, "ubuntu") {
		t.Error("expected user injection")
	}
}

func TestResolveCloudInit_File(t *testing.T) {
	dir := t.TempDir()
	ciPath := filepath.Join(dir, "ci.yaml")
	if err := os.WriteFile(ciPath, []byte("package_upgrade: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf := &Config{
		Name:          "test",
		User:          "ubuntu",
		CloudInitFile: ciPath,
	}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "package_upgrade: true") {
		t.Error("expected package_upgrade in output")
	}
}

func TestResolveCloudInit_IncludesMergedWithLocal(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(basePath, []byte("packages:\n  - vim\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf := &Config{
		Name:             "test",
		User:             "ubuntu",
		CloudInitInclude: []string{basePath},
		CloudInit:        "packages:\n  - curl\nruncmd:\n  - echo hello\n",
	}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "vim") {
		t.Error("expected vim from base include")
	}
	if !strings.Contains(result, "curl") {
		t.Error("expected curl from local override")
	}
}

func TestResolveCloudInit_StripsCloudConfigHeader(t *testing.T) {
	dir := t.TempDir()
	ciPath := filepath.Join(dir, "ci.yaml")
	if err := os.WriteFile(ciPath, []byte("#cloud-config\npackage_update: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf := &Config{
		Name:          "test",
		CloudInitFile: ciPath,
	}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Count(result, "#cloud-config") != 1 {
		t.Error("expected exactly one #cloud-config header")
	}
}

func TestResolveCloudInit_UserInjectionAddsToExistingUsers(t *testing.T) {
	conf := &Config{
		Name:      "test",
		User:      "deploy",
		CloudInit: "users:\n  - default\n",
	}
	result, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "deploy") {
		t.Error("expected deploy user to be injected")
	}
	if !strings.Contains(result, "default") {
		t.Error("expected default user to be preserved")
	}
}

func TestDeepMerge_Maps(t *testing.T) {
	dst := map[string]interface{}{"a": "1", "b": "2"}
	src := map[string]interface{}{"b": "3", "c": "4"}
	result := deepMerge(dst, src).(map[string]interface{})
	if result["a"] != "1" {
		t.Error("expected a=1 preserved")
	}
	if result["b"] != "3" {
		t.Error("expected b=3 override")
	}
	if result["c"] != "4" {
		t.Error("expected c=4 added")
	}
}

func TestDeepMerge_NestedMaps(t *testing.T) {
	dst := map[string]interface{}{
		"nested": map[string]interface{}{"x": 1, "y": 2},
	}
	src := map[string]interface{}{
		"nested": map[string]interface{}{"y": 3, "z": 4},
	}
	result := deepMerge(dst, src).(map[string]interface{})
	nested := result["nested"].(map[string]interface{})
	if nested["x"] != 1 {
		t.Error("expected x=1 preserved")
	}
	if nested["y"] != 3 {
		t.Error("expected y=3 override")
	}
	if nested["z"] != 4 {
		t.Error("expected z=4 added")
	}
}

func TestDeepMerge_Slices(t *testing.T) {
	dst := []interface{}{"a", "b"}
	src := []interface{}{"c", "d"}
	result := deepMerge(dst, src).([]interface{})
	if len(result) != 4 {
		t.Fatalf("expected 4 elements, got %d", len(result))
	}
	if result[0] != "a" || result[3] != "d" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestDeepMerge_ScalarOverride(t *testing.T) {
	if result := deepMerge("old", "new"); result != "new" {
		t.Errorf("expected 'new', got %v", result)
	}
}

func TestDeepMerge_NonMatchingTypes_SrcWins(t *testing.T) {
	result := deepMerge(map[string]interface{}{"a": 1}, "string")
	if result != "string" {
		t.Errorf("expected src to win on type mismatch, got %v", result)
	}
}

func TestDeepMerge_MapWithNonMapSrc_SrcWins(t *testing.T) {
	dst := map[string]interface{}{"a": 1}
	src := "not-a-map"
	result := deepMerge(dst, src)
	if result != "not-a-map" {
		t.Errorf("expected src to win, got %v", result)
	}
}

func TestDeepMerge_SliceWithNonSliceSrc_SrcWins(t *testing.T) {
	dst := []interface{}{1, 2}
	src := "not-a-slice"
	result := deepMerge(dst, src)
	if result != "not-a-slice" {
		t.Errorf("expected src to win, got %v", result)
	}
}

func TestDeepMerge_ScalarNonZeroWins(t *testing.T) {
	// When src is zero-valued, dst should be preserved.
	tests := []struct {
		name     string
		dst      interface{}
		src      interface{}
		expected interface{}
	}{
		{"empty string doesn't override", "hello", "", "hello"},
		{"non-empty string overrides", "hello", "world", "world"},
		{"zero int doesn't override", 42, 0, 42},
		{"non-zero int overrides", 1, 99, 99},
		{"false doesn't override true", true, false, true},
		{"true overrides false", false, true, true},
		{"nil doesn't override string", "keep", nil, "keep"},
		{"zero float doesn't override", 3.14, 0.0, 3.14},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deepMerge(tt.dst, tt.src)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestDeepMerge_ScalarNonZeroWins_PreservesEmptyString(t *testing.T) {
	// Empty string is zero-valued and should not override.
	result := deepMerge("preserve_me", "")
	if result != "preserve_me" {
		t.Errorf("expected 'preserve_me', got %v", result)
	}
}

func TestDeepMerge_ScalarNonZeroWins_KeepZero(t *testing.T) {
	if result := deepMerge(0, 0); result != 0 {
		t.Errorf("expected 0, got %v", result)
	}
}

// MergeConfigs tests

func TestMergeConfigs_ScalarOverride_NonZeroWins(t *testing.T) {
	base := &Config{User: "devuser"}
	overlay := &Config{User: ""}
	result, _ := MergeConfigs(base, overlay)
	if result.User != "devuser" {
		t.Errorf("expected base user 'devuser' preserved when overlay is empty, got %q", result.User)
	}
}

func TestMergeConfigs_ScalarOverride_LeafWins(t *testing.T) {
	base := &Config{Image: ""}
	overlay := &Config{Image: "ubuntu:24.04"}
	result, _ := MergeConfigs(base, overlay)
	if result.Image != "ubuntu:24.04" {
		t.Errorf("expected overlay image, got %q", result.Image)
	}
}

func TestMergeConfigs_ScalarOverride_Name(t *testing.T) {
	base := &Config{Name: "base-name"}
	overlay := &Config{Name: "final-name"}
	result, _ := MergeConfigs(base, overlay)
	if result.Name != "final-name" {
		t.Errorf("expected overlay name, got %q", result.Name)
	}
}

func TestMergeConfigs_MountsConcat(t *testing.T) {
	base := &Config{Mounts: []Mount{
		{Source: "/host/a", Path: "/mnt/a"},
		{Source: "/host/b", Path: "/mnt/b"},
	}}
	overlay := &Config{Mounts: []Mount{
		{Source: "/host/c", Path: "/mnt/c"},
	}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.Mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(result.Mounts))
	}
	if result.Mounts[0].Source != "/host/a" {
		t.Error("expected base mounts first")
	}
	if result.Mounts[2].Source != "/host/c" {
		t.Error("expected overlay mounts appended")
	}
}

func TestMergeConfigs_RecipesConcat(t *testing.T) {
	base := &Config{Recipes: []RecipeGroup{
		{RunAs: "root", Scripts: []string{"base.sh"}},
	}}
	overlay := &Config{Recipes: []RecipeGroup{
		{Scripts: []string{"leaf.sh"}},
	}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups, got %d", len(result.Recipes))
	}
	if result.Recipes[0].Scripts[0] != "base.sh" {
		t.Error("expected base recipes first")
	}
	if result.Recipes[1].Scripts[0] != "leaf.sh" {
		t.Error("expected overlay recipes appended")
	}
}

func TestMergeConfigs_NetworksConcat(t *testing.T) {
	base := &Config{Networks: []NetworkConfig{
		{Name: "eth0", IPv4: "10.0.0.1"},
	}}
	overlay := &Config{Networks: []NetworkConfig{
		{Name: "eth1", IPv4: "10.0.0.2"},
	}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.Networks) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(result.Networks))
	}
	if result.Networks[0].Name != "eth0" {
		t.Error("expected base network first")
	}
	if result.Networks[1].Name != "eth1" {
		t.Error("expected overlay network appended")
	}
}

func TestMergeConfigs_GroupsConcat(t *testing.T) {
	base := &Config{Groups: []string{"dev"}}
	overlay := &Config{Groups: []string{"gpu"}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(result.Groups))
	}
	if result.Groups[0] != "dev" || result.Groups[1] != "gpu" {
		t.Errorf("expected [dev gpu], got %v", result.Groups)
	}
}

func TestMergeConfigs_CloudInitIncludeConcat(t *testing.T) {
	base := &Config{CloudInitInclude: []string{"base.yaml"}}
	overlay := &Config{CloudInitInclude: []string{"extra.yaml"}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.CloudInitInclude) != 2 {
		t.Fatalf("expected 2 includes, got %d", len(result.CloudInitInclude))
	}
}

func TestMergeConfigs_IncludeNotMerged(t *testing.T) {
	base := &Config{Include: []string{"base.yaml"}}
	overlay := &Config{Include: []string{"leaf.yaml"}}
	result, _ := MergeConfigs(base, overlay)
	if len(result.Include) != 1 || result.Include[0] != "leaf.yaml" {
		t.Errorf("expected overlay-only include (merge:'-'), got %v", result.Include)
	}
}

func TestMergeConfigs_BaseNotMerged(t *testing.T) {
	base := &Config{Base: true}
	overlay := &Config{Base: false}
	result, _ := MergeConfigs(base, overlay)
	if result.Base {
		t.Error("expected overlay Base (merge:'-') to take effect, got true")
	}
}

func TestMergeConfigs_PresenceTracking(t *testing.T) {
	base := &Config{User: "root"}
	overlay := &Config{User: "ubuntu"}
	res, _ := MergeConfigs(base, overlay)
	if res.User != "ubuntu" {
		t.Errorf("expected overlay user 'ubuntu', got %q", res.User)
	}
}

func TestMergeConfigs_CloudInitConverges(t *testing.T) {
	base := &Config{CloudInit: "packages: [curl]"}
	overlay := &Config{CloudInit: "packages: [wget]"}
	result, _ := MergeConfigs(base, overlay)
	if result.CloudInit != "packages: [wget]" {
		t.Errorf("expected overlay cloud-init, got %q", result.CloudInit)
	}
}

func TestMergeConfigs_EmptyBase(t *testing.T) {
	result, _ := MergeConfigs(&Config{}, &Config{
		Name: "test",
	})
	if result.Name != "test" {
		t.Errorf("expected overlay name, got %q", result.Name)
	}
}

func TestMergeConfigs_EmptyOverlay(t *testing.T) {
	base := &Config{Name: "test"}
	result, _ := MergeConfigs(base, &Config{})
	if result.Name != "test" {
		t.Errorf("expected base name, got %q", result.Name)
	}
}

// Validate base:true tests

func TestValidate_BaseConfig_SkipsNameAndImage(t *testing.T) {
	conf := &Config{Base: true}
	err := conf.Validate("")
	if err != nil {
		t.Fatalf("expected base config to skip name/image validation, got: %v", err)
	}
}

func TestValidate_BaseConfig_WithName_Error(t *testing.T) {
	conf := &Config{Base: true, Name: "should-not-exist"}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error when base config has a name")
	}
	if !strings.Contains(err.Error(), "base") {
		t.Errorf("expected error to mention 'base', got: %v", err)
	}
}

func TestValidate_BaseConfig_WithImage_Error(t *testing.T) {
	conf := &Config{Base: true, Name: "oops", Image: "ubuntu:24.04"}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected error when base config has both name and image")
	}
}

func TestValidate_BaseConfig_AllowsMounts(t *testing.T) {
	dir := t.TempDir()
	conf := &Config{Base: true, Mounts: []Mount{{Source: dir, Path: "/mnt"}}}
	err := conf.Validate("")
	if err != nil {
		t.Fatalf("expected base config with mounts to validate, got: %v", err)
	}
}

func TestValidate_NonBase_RequiresName(t *testing.T) {
	conf := &Config{}
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected non-base config to require name")
	}
}

// Post-merge duplicate detection tests

func TestValidatePostMerge_DuplicateMountPath_Error(t *testing.T) {
	conf := &Config{
		Name:  "test",
		Image: "ubuntu:24.04",
		Mounts: []Mount{
			{Source: "/host/a", Path: "/mnt/shared"},
			{Source: "/host/b", Path: "/mnt/shared"},
		},
	}
	err := ValidatePostMerge(conf)
	if err == nil {
		t.Fatal("expected error for duplicate mount paths")
	}
	if !strings.Contains(err.Error(), "duplicate mount") {
		t.Errorf("expected 'duplicate mount' in error, got: %v", err)
	}
}

func TestValidatePostMerge_DuplicateNetworkName_Error(t *testing.T) {
	conf := &Config{
		Name:  "test",
		Image: "ubuntu:24.04",
		Networks: []NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.1"},
			{Name: "eth0", IPv4: "10.0.0.2"},
		},
	}
	err := ValidatePostMerge(conf)
	if err == nil {
		t.Fatal("expected error for duplicate network names")
	}
	if !strings.Contains(err.Error(), "duplicate network") {
		t.Errorf("expected 'duplicate network' in error, got: %v", err)
	}
}

func TestValidatePostMerge_UniqueMounts_Pass(t *testing.T) {
	conf := &Config{
		Name:  "test",
		Image: "ubuntu:24.04",
		Mounts: []Mount{
			{Source: "/host/a", Path: "/mnt/a"},
			{Source: "/host/b", Path: "/mnt/b"},
		},
	}
	err := ValidatePostMerge(conf)
	if err != nil {
		t.Fatalf("expected no error for unique mounts, got: %v", err)
	}
}

func TestValidatePostMerge_UniqueNetworks_Pass(t *testing.T) {
	conf := &Config{
		Name:  "test",
		Image: "ubuntu:24.04",
		Networks: []NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.1"},
			{Name: "eth1", IPv4: "10.0.0.2"},
		},
	}
	err := ValidatePostMerge(conf)
	if err != nil {
		t.Fatalf("expected no error for unique networks, got: %v", err)
	}
}

// LoadConfig tests

func TestLoadConfig_NoIncludes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(configPath, []byte("name: myapp\nimage: ubuntu:24.04\nuser: deploy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.Name != "myapp" {
		t.Errorf("expected name 'myapp', got %q", conf.Name)
	}
	if conf.User != "deploy" {
		t.Errorf("expected user 'deploy', got %q", conf.User)
	}
}

func TestLoadConfig_OneLevelInclude(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "_base.yaml")
	if err := os.WriteFile(basePath, []byte("base: true\nmounts:\n  - source: /host/shared\n    path: /shared\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _base.yaml\nmounts:\n  - source: /host/data\n    path: /data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conf.Mounts) != 2 {
		t.Fatalf("expected 2 mounts (1 base + 1 leaf), got %d", len(conf.Mounts))
	}
	if conf.Mounts[0].Path != "/shared" {
		t.Errorf("expected base mount first, got %q", conf.Mounts[0].Path)
	}
	if conf.Mounts[1].Path != "/data" {
		t.Errorf("expected leaf mount second, got %q", conf.Mounts[1].Path)
	}
}

func TestLoadConfig_TwoLevelInclude(t *testing.T) {
	dir := t.TempDir()

	grandparentPath := filepath.Join(dir, "_common.yaml")
	if err := os.WriteFile(grandparentPath, []byte("base: true\nuser: common-user\n"), 0644); err != nil {
		t.Fatal(err)
	}

	parentPath := filepath.Join(dir, "_dev.yaml")
	if err := os.WriteFile(parentPath, []byte("base: true\ninclude:\n  - _common.yaml\ngroups:\n  - dev\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _dev.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.User != "common-user" {
		t.Errorf("expected user 'common-user' from grandparent, got %q", conf.User)
	}
	if len(conf.Groups) != 1 || conf.Groups[0] != "dev" {
		t.Errorf("expected groups [dev] from parent, got %v", conf.Groups)
	}
}

func TestLoadConfig_CircularInclude_Detected(t *testing.T) {
	dir := t.TempDir()

	aPath := filepath.Join(dir, "_a.yaml")
	if err := os.WriteFile(aPath, []byte("base: true\ninclude:\n  - _b.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	bPath := filepath.Join(dir, "_b.yaml")
	if err := os.WriteFile(bPath, []byte("base: true\ninclude:\n  - _a.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _a.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(leafPath)
	if err == nil {
		t.Fatal("expected error for circular include")
	}
	if !strings.Contains(err.Error(), "circular") && !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error to mention circular/cycle, got: %v", err)
	}
}

func TestLoadConfig_ScalarOverride_PresenceWins(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "_base.yaml")
	if err := os.WriteFile(basePath, []byte("base: true\nuser: devuser\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _base.yaml\nuser: \"\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.User != "" {
		t.Errorf("expected explicit empty string user '' to win under presence-wins (D5), got %q", conf.User)
	}
}

func TestLoadConfig_BaseTrue_WithoutUnderscore_Warning(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "mybase.yaml")
	if err := os.WriteFile(basePath, []byte("base: true\nuser: deploy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - mybase.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.User != "deploy" {
		t.Errorf("expected user from mybase, got %q", conf.User)
	}
}

func TestLoadConfig_UnderscorePrefix_NoBaseTrue_Error(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "_nobase.yaml")
	if err := os.WriteFile(basePath, []byte("mounts:\n  - source: /host/data\n    path: /data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _nobase.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(leafPath)
	if err == nil {
		t.Fatal("expected error for _-prefixed file without base:true")
	}
}

func TestLoadConfig_DuplicateMount_AfterMerge_Error(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "_base.yaml")
	if err := os.WriteFile(basePath, []byte("base: true\nmounts:\n  - source: /host/a\n    path: /mnt/shared\n"), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _base.yaml\nmounts:\n  - source: /host/b\n    path: /mnt/shared\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(leafPath)
	if err == nil {
		t.Fatal("expected error for duplicate mount paths after merge")
	}
	if !strings.Contains(err.Error(), "duplicate mount") {
		t.Errorf("expected 'duplicate mount' in error, got: %v", err)
	}
}

func TestLoadConfig_MissingIncludeFile_Error(t *testing.T) {
	dir := t.TempDir()

	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte("name: app\nimage: ubuntu:24.04\ninclude:\n  - _nonexistent.yaml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(leafPath)
	if err == nil {
		t.Fatal("expected error for missing include file")
	}
}

func TestIntegration_FullPipeline(t *testing.T) {
	dir := t.TempDir()
	hostShared := t.TempDir()
	hostLogs := t.TempDir()
	hostData := t.TempDir()

	basePath := filepath.Join(dir, "_base.yaml")
	baseYAML := fmt.Sprintf(`base: true
mounts:
  - source: %s
    path: /shared
  - source: %s
    path: /var/log/app
recipes:
  - scripts:
      - recipes/common.sh
groups:
  - dev
`, hostShared, hostLogs)
	if err := os.WriteFile(basePath, []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	leafPath := filepath.Join(dir, "worker.yaml")
	leafYAML := fmt.Sprintf(`name: worker
image: ubuntu:24.04
user: deploy
include:
  - _base.yaml
mounts:
  - source: %s
    path: /data
recipes:
  - run_as: root
    scripts:
      - recipes/app.sh
groups:
  - gpu
`, hostData)
	if err := os.WriteFile(leafPath, []byte(leafYAML), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Verify name/identity from leaf
	if conf.Name != "worker" {
		t.Errorf("expected name 'worker', got %q", conf.Name)
	}
	if conf.Image != "ubuntu:24.04" {
		t.Errorf("expected image 'ubuntu:24.04', got %q", conf.Image)
	}
	if conf.User != "deploy" {
		t.Errorf("expected user 'deploy', got %q", conf.User)
	}

	// Verify mounts concatenated (base first, leaf appended)
	if len(conf.Mounts) != 3 {
		t.Fatalf("expected 3 mounts (2 base + 1 leaf), got %d", len(conf.Mounts))
	}
	if conf.Mounts[0].Path != "/shared" {
		t.Errorf("expected base mount 0 '/shared', got %q", conf.Mounts[0].Path)
	}
	if conf.Mounts[1].Path != "/var/log/app" {
		t.Errorf("expected base mount 1 '/var/log/app', got %q", conf.Mounts[1].Path)
	}
	if conf.Mounts[2].Path != "/data" {
		t.Errorf("expected leaf mount '/data', got %q", conf.Mounts[2].Path)
	}

	// Verify recipes concatenated
	if len(conf.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups, got %d", len(conf.Recipes))
	}
	if len(conf.Recipes[0].Scripts) != 1 || conf.Recipes[0].Scripts[0] != "recipes/common.sh" {
		t.Errorf("expected base recipe 'recipes/common.sh', got %v", conf.Recipes[0].Scripts)
	}
	if conf.Recipes[1].RunAs != "root" {
		t.Errorf("expected leaf recipe run_as 'root', got %q", conf.Recipes[1].RunAs)
	}

	// Verify groups concatenated (base + leaf)
	if len(conf.Groups) != 2 {
		t.Fatalf("expected 2 groups (base dev + leaf gpu), got %v", conf.Groups)
	}
	if conf.Groups[0] != "dev" || conf.Groups[1] != "gpu" {
		t.Errorf("expected groups [dev gpu], got %v", conf.Groups)
	}

	// Verify validation passes on resolved config
	if err := conf.Validate(""); err != nil {
		t.Fatalf("Validate failed on resolved config: %v", err)
	}
}

func TestIntegration_BackwardCompatibility_NoIncludes(t *testing.T) {
	dir := t.TempDir()

	configPath := filepath.Join(dir, "legacy.yaml")
	legacyYAML := `name: legacy-box
image: ubuntu:22.04
user: ubuntu
mounts:
  - source: /host/data
    path: /mnt/data
`
	if err := os.WriteFile(configPath, []byte(legacyYAML), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed on legacy config: %v", err)
	}

	if conf.Name != "legacy-box" {
		t.Errorf("expected name 'legacy-box', got %q", conf.Name)
	}
	if conf.User != "ubuntu" {
		t.Errorf("expected user 'ubuntu', got %q", conf.User)
	}
	if len(conf.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(conf.Mounts))
	}
	if len(conf.Include) != 0 {
		t.Error("expected no includes on legacy config")
	}
	if conf.Base {
		t.Error("expected base=false on legacy config")
	}
}

// YAML round-trip (lxm include) tests

func TestAddIncludeToYAMLFile_AddsToEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.yaml")
	initial := "name: myapp\nimage: ubuntu:24.04\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := AddIncludeToYAMLFile(filePath, "_base.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected file to be modified")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "include") {
		t.Error("expected include key in output")
	}
	if !strings.Contains(content, "_base.yaml") {
		t.Error("expected include path in output")
	}
	if !strings.Contains(content, "name: myapp") {
		t.Error("expected existing keys preserved")
	}
}

func TestAddIncludeToYAMLFile_AppendsToExistingInclude(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.yaml")
	initial := "name: myapp\ninclude:\n  - _common.yaml\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := AddIncludeToYAMLFile(filePath, "_base.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected file to be modified")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "_common.yaml") {
		t.Error("expected existing include preserved")
	}
	if !strings.Contains(content, "_base.yaml") {
		t.Error("expected new include path added")
	}

	// Parse back and verify both includes present
	var conf Config
	if err := yaml.Unmarshal(data, &conf); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(conf.Include) != 2 {
		t.Fatalf("expected 2 includes, got %d: %v", len(conf.Include), conf.Include)
	}
}

func TestAddIncludeToYAMLFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.yaml")
	initial := "name: myapp\ninclude:\n  - _base.yaml\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	// First call
	changed, err := AddIncludeToYAMLFile(filePath, "_base.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no change when include already present (idempotent)")
	}

	// Verify file unchanged
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Errorf("expected file unchanged, got:\n%s", string(data))
	}
}

func TestAddIncludeToYAMLFile_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.yaml")
	initial := "# My container config\nname: myapp\n# Use ubuntu 24.04\nimage: ubuntu:24.04\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := AddIncludeToYAMLFile(filePath, "_base.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected file to be modified")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "My container config") {
		t.Error("expected comment 'My container config' preserved")
	}
	if !strings.Contains(content, "Use ubuntu 24.04") {
		t.Error("expected comment 'Use ubuntu 24.04' preserved")
	}
	if !strings.Contains(content, "include") {
		t.Error("expected include key added")
	}
}

func TestAddIncludeToYAMLFile_NonexistentFile(t *testing.T) {
	_, err := AddIncludeToYAMLFile("/nonexistent/path.yaml", "_base.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRecipeHashKey(t *testing.T) {
	k1 := RecipeHashKey("/etc/lxm/script.sh", "")
	if !strings.HasPrefix(k1, "user.lxm.recipe._etc_lxm_script_sh.hash") {
		t.Errorf("unexpected key format: %q", k1)
	}

	k2 := RecipeHashKey("/etc/lxm/script.sh", "install_nginx")
	if k2 != "user.lxm.recipe.install_nginx.hash" {
		t.Errorf("expected user.lxm.recipe.install_nginx.hash, got %q", k2)
	}
}

func TestMergeConfigs_ListDirectives(t *testing.T) {
	base := &Config{
		Mounts: []Mount{
			{Source: "/host/a", Path: "/mnt/a"},
			{Source: "/host/b", Path: "/mnt/b"},
		},
		Networks: []NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.1"},
			{Name: "eth1", IPv4: "10.0.0.2"},
		},
		Recipes: []RecipeGroup{
			{Scripts: []string{"setup.sh", "cleanup.sh"}},
		},
	}

	overlay := &Config{
		Remove: &RemoveDirective{
			Mounts:   []string{"/mnt/a"},
			Networks: []string{"eth1"},
			Recipes:  []string{"cleanup.sh"},
		},
	}

	res, _ := MergeConfigs(base, overlay)
	if len(res.Mounts) != 1 || res.Mounts[0].Path != "/mnt/b" {
		t.Errorf("remove.mounts failed, got %+v", res.Mounts)
	}
	if len(res.Networks) != 1 || res.Networks[0].Name != "eth0" {
		t.Errorf("remove.networks failed, got %+v", res.Networks)
	}
	if len(res.Recipes) != 1 || res.Recipes[0].Scripts[0] != "setup.sh" {
		t.Errorf("remove.recipes failed, got %+v", res.Recipes)
	}
}

func TestMergeConfigs_ReplaceDirectives(t *testing.T) {
	base := &Config{
		Mounts: []Mount{{Source: "/old", Path: "/old"}},
	}
	overlay := &Config{
		Replace: &ReplaceDirective{
			Mounts: []Mount{{Source: "/new", Path: "/new"}},
		},
	}

	res, _ := MergeConfigs(base, overlay)
	if len(res.Mounts) != 1 || res.Mounts[0].Path != "/new" {
		t.Errorf("replace.mounts failed, got %+v", res.Mounts)
	}
}

func TestMergeConfigs_RemoveDirective_NoMatchError(t *testing.T) {
	base := &Config{
		Mounts: []Mount{{Source: "/old", Path: "/old"}},
	}
	overlay := &Config{
		Remove: &RemoveDirective{
			Mounts: []string{"/nonexistent"},
		},
	}

	_, err := MergeConfigs(base, overlay)
	if err == nil {
		t.Error("expected error when remove directive matches no item, got nil")
	}
}

func TestHasIncludeInYAMLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	_ = os.WriteFile(path, []byte("include:\n  - _base.yaml\n"), 0644)

	has, err := HasIncludeInYAMLFile(path, "_base.yaml")
	if err != nil || !has {
		t.Errorf("expected HasIncludeInYAMLFile true, got %v, err %v", has, err)
	}

	hasNo, _ := HasIncludeInYAMLFile(path, "_other.yaml")
	if hasNo {
		t.Errorf("expected HasIncludeInYAMLFile false for non-matching path")
	}
}

func TestDiscoverHostPrivateKeys(t *testing.T) {
	keys := DiscoverHostPrivateKeys()
	_ = keys
}

func TestExpandTemplates(t *testing.T) {
	t.Setenv("MYENV", "hello_env")
	input := "name: {{ .Name }}\ngroup: {{ .Group }}\nvar: {{ .Vars.foo }}\nenv: {{ .Env.MYENV }}\nescaped: \\{{\\hello\\}}\\n"
	vars := map[string]string{"foo": "bar"}

	res, err := expandTemplates(input, vars, "myname", "mygroup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(res, "name: myname") || !strings.Contains(res, "group: mygroup") || !strings.Contains(res, "var: bar") || !strings.Contains(res, "env: hello_env") {
		t.Errorf("template expansion failed, got: %q", res)
	}

	// Test unbound env error
	_, err3 := expandTemplates("env: {{ .Env.UNBOUND_ENV_VAR }}", vars, "a", "b")
	if err3 == nil {
		t.Errorf("expected error for unbound env")
	}
}

func TestTemplateInConstrainedField_AuthoringValidation(t *testing.T) {
	t.Setenv("LXM_TEST_IMAGE", "ubuntu:22.04")
	dir := t.TempDir()
	file := filepath.Join(dir, "app.yaml")
	content := `schema: lxm/config/v2
name: testbox
image: '{{ .Env.LXM_TEST_IMAGE }}'
`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatalf("failed writing file: %v", err)
	}

	conf, err := LoadConfig(file)
	if err != nil {
		t.Fatalf("expected template in image field to pass authoring validation after expansion, got err: %v", err)
	}
	if conf.Image != "ubuntu:22.04" {
		t.Errorf("expected substituted image ubuntu:22.04, got %q", conf.Image)
	}
}

func TestWaitPolicy_BoolAndStructMerge(t *testing.T) {
	yamlBool := `schema: lxm/config/v2
name: box1
image: ubuntu:22.04
wait: false
`
	dir := t.TempDir()
	fileBool := filepath.Join(dir, "bool.yaml")
	_ = os.WriteFile(fileBool, []byte(yamlBool), 0644)
	confBool, err := LoadConfig(fileBool)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if confBool.WaitPolicy.Required {
		t.Errorf("expected Required false for wait: false")
	}

	yamlMap := `schema: lxm/config/v2
name: box2
image: ubuntu:22.04
wait:
  cloud_init: 20m
  required: false
`
	fileMap := filepath.Join(dir, "map.yaml")
	_ = os.WriteFile(fileMap, []byte(yamlMap), 0644)
	confMap, err := LoadConfig(fileMap)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if confMap.WaitPolicy.CloudInit != "20m" || confMap.WaitPolicy.Required {
		t.Errorf("expected CloudInit 20m and Required false, got %+v", confMap.WaitPolicy)
	}
}

func TestV1Compat_WaitConfigTag(t *testing.T) {
	yamlLegacy := `schema: lxm/config/v1
name: legacybox
image: ubuntu:22.04
wait_config:
  cloud_init: 25m
  required: false
`
	dir := t.TempDir()
	fileLegacy := filepath.Join(dir, "legacy.yaml")
	_ = os.WriteFile(fileLegacy, []byte(yamlLegacy), 0644)
	confLegacy, err := LoadConfig(fileLegacy)
	if err != nil {
		t.Fatalf("LoadConfig legacy error: %v", err)
	}
	if confLegacy.WaitPolicy.CloudInit != "25m" || confLegacy.WaitPolicy.Required {
		t.Errorf("expected v1 wait_config to be loaded cleanly, got %+v", confLegacy.WaitPolicy)
	}
}

func TestResolveCloudInit_RelativePathWithConfigBaseDir(t *testing.T) {
	dir := t.TempDir()
	cloudInitFile := filepath.Join(dir, "user-data.yaml")
	_ = os.WriteFile(cloudInitFile, []byte("#cloud-config\npackages:\n  - curl\n"), 0644)

	yamlManifest := `schema: lxm/config/v2
name: relativebox
image: ubuntu:22.04
cloud-init-file: user-data.yaml
`
	manifestPath := filepath.Join(dir, "app.yaml")
	_ = os.WriteFile(manifestPath, []byte(yamlManifest), 0644)

	conf, err := LoadConfig(manifestPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	cloudInit, err := conf.ResolveCloudInit("")
	if err != nil || !strings.Contains(cloudInit, "curl") {
		t.Errorf("expected cloud-init content to resolve using ConfigBaseDir, got err=%v, content=%q", err, cloudInit)
	}
}

func TestResolveConfigPath(t *testing.T) {
	abs := resolveConfigPath("/abs/path.yaml", "/base")
	if abs != "/abs/path.yaml" {
		t.Errorf("expected /abs/path.yaml, got %q", abs)
	}

	rel := resolveConfigPath("_base.yaml", "/base")
	if rel != "/base/_base.yaml" {
		t.Errorf("expected /base/_base.yaml, got %q", rel)
	}
}

func TestConfig_SecurityPostureDefaults(t *testing.T) {
	dir := t.TempDir()

	// 1. Insecure default manifest (v2 default)
	manifest1 := `schema: lxm/config/v2
name: secure-box
image: ubuntu:24.04
`
	path1 := filepath.Join(dir, "box1.yaml")
	_ = os.WriteFile(path1, []byte(manifest1), 0644)

	conf1, err := LoadConfig(path1)
	if err != nil {
		t.Fatalf("LoadConfig box1 failed: %v", err)
	}
	if conf1.Sudo {
		t.Errorf("expected Sudo: false by default in v2")
	}
	if conf1.InjectSSHKeys {
		t.Errorf("expected InjectSSHKeys: false by default in v2")
	}

	cloudInit1, err := conf1.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("ResolveCloudInit failed: %v", err)
	}
	if strings.Contains(cloudInit1, "NOPASSWD") {
		t.Errorf("cloudInit1 should not contain NOPASSWD by default")
	}
	if strings.Contains(cloudInit1, "ssh_authorized_keys") {
		t.Errorf("cloudInit1 should not contain ssh_authorized_keys by default")
	}

	// 2. Opt-in security fields
	manifest2 := `schema: lxm/config/v2
name: optin-box
image: ubuntu:24.04
sudo: true
inject_ssh_keys: false
ssh_keys:
  - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI123 key1"
`
	path2 := filepath.Join(dir, "box2.yaml")
	_ = os.WriteFile(path2, []byte(manifest2), 0644)

	conf2, err := LoadConfig(path2)
	if err != nil {
		t.Fatalf("LoadConfig box2 failed: %v", err)
	}
	if !conf2.Sudo {
		t.Errorf("expected Sudo: true when opt-in")
	}
	if len(conf2.SSHKeys) != 1 || !strings.Contains(conf2.SSHKeys[0], "key1") {
		t.Errorf("expected SSHKeys to be populated, got %v", conf2.SSHKeys)
	}

	cloudInit2, err := conf2.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("ResolveCloudInit box2 failed: %v", err)
	}
	if !strings.Contains(cloudInit2, "NOPASSWD") {
		t.Errorf("cloudInit2 expected to contain NOPASSWD when sudo: true")
	}
	if !strings.Contains(cloudInit2, "ssh_authorized_keys") || !strings.Contains(cloudInit2, "key1") {
		t.Errorf("cloudInit2 expected to contain explicit ssh_keys")
	}
}

func TestConfig_BaseValidationErrors(t *testing.T) {
	conf := &Config{
		Base: true,
		Name: "named-base",
	}
	if err := conf.Validate(""); err == nil {
		t.Errorf("expected error when Base config has a Name")
	}

	conf = &Config{
		Base:  true,
		Image: "ubuntu:22.04",
	}
	if err := conf.Validate(""); err == nil {
		t.Errorf("expected error when Base config has an Image")
	}
}

func TestConfig_MountShorthandAndYaml(t *testing.T) {
	yamlStr := `
mounts:
  - "/src/host:/dst/container:ro:recursive"
`
	var c Config
	if err := yaml.Unmarshal([]byte(yamlStr), &c); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(c.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(c.Mounts))
	}
	m := c.Mounts[0]
	if m.Source != "/src/host" || m.Path != "/dst/container" || !m.Readonly || !m.Recursive {
		t.Errorf("unexpected mount shorthand unmarshaled: %+v", m)
	}

	invalidYaml := `
mounts:
  - "invalid_mount_no_colon"
`
	var cInvalid Config
	if err := yaml.Unmarshal([]byte(invalidYaml), &cInvalid); err == nil {
		t.Errorf("expected error unmarshaling invalid mount shorthand")
	}
}

func TestConfig_WaitConfigInvalidScalar(t *testing.T) {
	yamlStr := `
wait: "invalid_scalar_string"
`
	var c Config
	if err := yaml.Unmarshal([]byte(yamlStr), &c); err == nil {
		t.Errorf("expected error unmarshaling invalid wait scalar")
	}
}

func TestConfig_RemoveAndReplaceDirectives(t *testing.T) {
	baseConf := &Config{
		Mounts: []Mount{
			{Source: "/src1", Path: "/dst1"},
			{Source: "/src2", Path: "/dst2"},
		},
		Networks: []NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.10"},
			{Name: "eth1", IPv4: "10.0.0.11"},
		},
		Recipes: []RecipeGroup{
			{RunAs: "root", Scripts: []string{"script1.sh"}},
		},
	}

	childConf := &Config{
		Name:  "child",
		Image: "ubuntu:22.04",
		Remove: &RemoveDirective{
			Mounts:   []string{"/dst1"},
			Networks: []string{"eth1"},
			Recipes:  []string{"script1.sh"},
		},
	}

	merged, err := MergeConfigs(baseConf, childConf)
	if err != nil {
		t.Fatalf("MergeConfigs failed: %v", err)
	}
	if len(merged.Mounts) != 1 || merged.Mounts[0].Path != "/dst2" {
		t.Errorf("expected mount /dst1 removed, got %v", merged.Mounts)
	}
	if len(merged.Networks) != 1 || merged.Networks[0].Name != "eth0" {
		t.Errorf("expected network eth1 removed, got %v", merged.Networks)
	}
	if len(merged.Recipes) != 0 {
		t.Errorf("expected recipe script1.sh removed, got %v", merged.Recipes)
	}
}

func TestConfig_ValidationEdgeCases(t *testing.T) {
	// Status absent with State set
	c1 := &Config{Name: "box", Status: "absent", State: "running"}
	if err := c1.Validate(""); err == nil {
		t.Errorf("expected error for status absent with state set")
	}

	// Invalid container destination path
	tmpDir := t.TempDir()
	c2 := &Config{
		Name:   "box",
		Image:  "ubuntu:22.04",
		Mounts: []Mount{{Source: tmpDir, Path: "/proc"}},
	}
	if err := c2.Validate(""); err == nil {
		t.Errorf("expected error for mount to /proc")
	}

	// Invalid IPv4 in network
	c3 := &Config{
		Name:     "box",
		Image:    "ubuntu:22.04",
		Networks: []NetworkConfig{{IPv4: "invalid-ip-addr"}},
	}
	if err := c3.Validate(""); err == nil {
		t.Errorf("expected error for invalid network IPv4")
	}

	// ValidatePostMerge duplicate mount path
	c4 := &Config{
		Mounts: []Mount{
			{Source: tmpDir, Path: "/mnt/data"},
			{Source: tmpDir, Path: "/mnt/data"},
		},
	}
	if err := ValidatePostMerge(c4); err == nil {
		t.Errorf("expected error for duplicate mount path in ValidatePostMerge")
	}

	// ValidatePostMerge duplicate network name
	c5 := &Config{
		Networks: []NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.1"},
			{Name: "eth0", IPv4: "10.0.0.2"},
		},
	}
	if err := ValidatePostMerge(c5); err == nil {
		t.Errorf("expected error for duplicate network name in ValidatePostMerge")
	}
}

func TestConfig_DiscoverHostKeysAndCloudInitInjection(t *testing.T) {
	_ = DiscoverHostPublicKeys()
	_ = DiscoverHostPrivateKeys()

	// InjectSSHKeys true
	conf := &Config{
		Name:          "box",
		Image:         "ubuntu:22.04",
		User:          "ubuntu",
		InjectSSHKeys: true,
		CloudInit:     "users:\n  - existing_user\nwrite_files:\n  - existing_file\n",
	}

	cloudInit, err := conf.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("ResolveCloudInit failed: %v", err)
	}
	if !strings.Contains(cloudInit, "existing_user") || !strings.Contains(cloudInit, "lxm-env.sh") {
		t.Errorf("expected merged cloudInit output, got %s", cloudInit)
	}

	// CloudInitInclude error
	confErr := &Config{
		Name:             "box",
		Image:            "ubuntu:22.04",
		CloudInitInclude: []string{"nonexistent_inc_12345.yaml"},
	}
	if _, err := confErr.ResolveCloudInit(""); err == nil {
		t.Errorf("expected error for nonexistent cloud-init-include file")
	}
}

func TestConfig_RemoveDirectiveErrorsAndSchemaValidation(t *testing.T) {
	dir := t.TempDir()

	// Underscore file without base: true
	invalidBaseFile := filepath.Join(dir, "_invalid_base.yaml")
	_ = os.WriteFile(invalidBaseFile, []byte("schema: lxm/config/v2\nname: test\n"), 0644)
	if _, err := LoadConfig(invalidBaseFile); err == nil {
		t.Errorf("expected error loading underscore file without base: true")
	}

	// Unknown schema version
	unknownSchemaFile := filepath.Join(dir, "unknown_schema.yaml")
	_ = os.WriteFile(unknownSchemaFile, []byte("schema: lxm/config/v999\nname: test\nimage: ubuntu:22.04\n"), 0644)
	if _, err := LoadConfig(unknownSchemaFile); err == nil {
		t.Errorf("expected error loading config with unknown schema version")
	}

	// Remove directive unmatched mount error
	res := &Config{Mounts: []Mount{{Source: "/tmp", Path: "/mnt"}}}
	err := applyRemoveDirectives(res, &RemoveDirective{Mounts: []string{"/nonexistent_mount"}})
	if err == nil {
		t.Errorf("expected error for unmatched remove.mounts")
	}

	// Remove directive unmatched network error
	resNet := &Config{Networks: []NetworkConfig{{Name: "eth0"}}}
	err = applyRemoveDirectives(resNet, &RemoveDirective{Networks: []string{"eth99"}})
	if err == nil {
		t.Errorf("expected error for unmatched remove.networks")
	}

	// Remove directive unmatched recipe error
	resRec := &Config{Recipes: []RecipeGroup{{Scripts: []string{"app.sh"}}}}
	err = applyRemoveDirectives(resRec, &RemoveDirective{Recipes: []string{"nonexistent.sh"}})
	if err == nil {
		t.Errorf("expected error for unmatched remove.recipes")
	}
}

func TestConfig_AddAndHasIncludeYAMLFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "app.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: app\nimage: ubuntu:22.04\n"), 0644)

	// HasInclude on file without includes -> false
	has, err := HasIncludeInYAMLFile(cfgFile, "_base.yaml")
	if err != nil || has {
		t.Fatalf("expected HasInclude false, got %v, err %v", has, err)
	}

	// AddInclude -> true
	added, err := AddIncludeToYAMLFile(cfgFile, "_base.yaml")
	if err != nil || !added {
		t.Fatalf("expected AddInclude true, got %v, err %v", added, err)
	}

	// AddInclude again -> false (already present)
	addedAgain, err := AddIncludeToYAMLFile(cfgFile, "_base.yaml")
	if err != nil || addedAgain {
		t.Fatalf("expected AddInclude false on duplicate, got %v, err %v", addedAgain, err)
	}

	hasNow, err := HasIncludeInYAMLFile(cfgFile, "_base.yaml")
	if err != nil || !hasNow {
		t.Fatalf("expected HasInclude true after addition, got %v, err %v", hasNow, err)
	}
}

func TestConfig_TemplateErrorsAndCircularInclude(t *testing.T) {
	dir := t.TempDir()

	// Unbound .Vars error
	unboundVarsFile := filepath.Join(dir, "unbound_vars.yaml")
	_ = os.WriteFile(unboundVarsFile, []byte("name: '{{ .Vars.missing }}'\nimage: ubuntu:22.04\n"), 0644)
	if _, err := LoadConfig(unboundVarsFile); err == nil {
		t.Errorf("expected error for unbound .Vars variable")
	}

	// Unbound .Env error
	unboundEnvFile := filepath.Join(dir, "unbound_env.yaml")
	_ = os.WriteFile(unboundEnvFile, []byte("name: '{{ .Env.UNBOUND_ENV_VAR_999 }}'\nimage: ubuntu:22.04\n"), 0644)
	if _, err := LoadConfig(unboundEnvFile); err == nil {
		t.Errorf("expected error for unbound .Env variable")
	}

	// Circular include
	fileA := filepath.Join(dir, "a.yaml")
	fileB := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(fileA, []byte("include: ['b.yaml']\nname: a\nimage: ubuntu:22.04\n"), 0644)
	_ = os.WriteFile(fileB, []byte("include: ['a.yaml']\nname: b\nimage: ubuntu:22.04\n"), 0644)
	if _, err := LoadConfig(fileA); err == nil {
		t.Errorf("expected error for circular include")
	}
}

func TestConfig_ValidationAndMountShorthandErrors(t *testing.T) {
	// Base with name
	b1 := &Config{Base: true, Name: "invalid_base_name"}
	if err := b1.Validate(""); err == nil {
		t.Errorf("expected error for base config with name")
	}

	// Base with image
	b2 := &Config{Base: true, Image: "ubuntu:22.04"}
	if err := b2.Validate(""); err == nil {
		t.Errorf("expected error for base config with image")
	}

	// Status absent with State
	c1 := &Config{Name: "box", Status: "absent", State: "running"}
	if err := c1.Validate(""); err == nil {
		t.Errorf("expected error for absent status with state")
	}

	// Invalid status
	c2 := &Config{Name: "box", Status: "invalid_status"}
	if err := c2.Validate(""); err == nil {
		t.Errorf("expected error for invalid status")
	}

	// Invalid mount shorthand
	var m Mount
	if err := yaml.Unmarshal([]byte("single_part_mount"), &m); err == nil {
		t.Errorf("expected error for invalid mount shorthand")
	}

	// Mount shorthand with options
	if err := yaml.Unmarshal([]byte("/tmp:/mnt:ro:recursive"), &m); err != nil || !m.Readonly || !m.Recursive {
		t.Errorf("expected read-only and recursive mount shorthand options, got %v, err %v", m, err)
	}

	// Invalid destination mount paths
	for _, badDst := range []string{"/", "/proc", "/sys", "/dev"} {
		badConf := &Config{
			Name:   "box",
			Image:  "ubuntu:22.04",
			Mounts: []Mount{{Source: t.TempDir(), Path: badDst}},
		}
		if err := badConf.Validate(""); err == nil {
			t.Errorf("expected error for invalid destination mount path %q", badDst)
		}
	}

	// Invalid IPv4 in NetworkConfig
	badNetConf := &Config{
		Name:     "box",
		Image:    "ubuntu:22.04",
		Networks: []NetworkConfig{{Name: "eth0", IPv4: "invalid_ip"}},
	}
	if err := badNetConf.Validate(""); err == nil {
		t.Errorf("expected error for invalid IPv4 address")
	}
}

func TestConfig_DeepMergeAndCloudInitFile(t *testing.T) {
	dir := t.TempDir()

	// CloudInitFile valid
	ciFile := filepath.Join(dir, "user_data.yaml")
	_ = os.WriteFile(ciFile, []byte("packages:\n  - curl\n"), 0644)
	confCI := &Config{
		Name:          "box",
		Image:         "ubuntu:22.04",
		User:          "ubuntu",
		CloudInitFile: ciFile,
	}
	resCI, err := confCI.ResolveCloudInit("")
	if err != nil || !strings.Contains(resCI, "curl") {
		t.Fatalf("ResolveCloudInit with CloudInitFile failed: %v, output: %s", err, resCI)
	}

	// CloudInitFile invalid YAML
	ciBadFile := filepath.Join(dir, "bad_user_data.yaml")
	_ = os.WriteFile(ciBadFile, []byte("packages: [unclosed_slice"), 0644)
	confBadCI := &Config{
		Name:          "box",
		Image:         "ubuntu:22.04",
		CloudInitFile: ciBadFile,
	}
	if _, err := confBadCI.ResolveCloudInit(""); err == nil {
		t.Errorf("expected error for invalid YAML in CloudInitFile")
	}

	// ReplaceDirective merging
	baseCfg := &Config{
		Mounts:   []Mount{{Source: "/tmp", Path: "/mnt1"}},
		Networks: []NetworkConfig{{Name: "eth0"}},
		Recipes:  []RecipeGroup{{Scripts: []string{"base.sh"}}},
	}
	overCfg := &Config{
		Replace: &ReplaceDirective{
			Mounts:   []Mount{{Source: "/tmp2", Path: "/mnt2"}},
			Networks: []NetworkConfig{{Name: "eth1"}},
			Recipes:  []RecipeGroup{{Scripts: []string{"over.sh"}}},
		},
	}
	merged, err := MergeConfigs(baseCfg, overCfg)
	if err != nil {
		t.Fatalf("MergeConfigs failed: %v", err)
	}
	if len(merged.Mounts) != 1 || merged.Mounts[0].Path != "/mnt2" {
		t.Errorf("expected replaced mount /mnt2, got %v", merged.Mounts)
	}
	if len(merged.Networks) != 1 || merged.Networks[0].Name != "eth1" {
		t.Errorf("expected replaced network eth1, got %v", merged.Networks)
	}
	if len(merged.Recipes) != 1 || merged.Recipes[0].Scripts[0] != "over.sh" {
		t.Errorf("expected replaced recipe over.sh, got %v", merged.Recipes)
	}

	// Nil MergeConfigs
	if m, _ := MergeConfigs(nil, baseCfg); m != baseCfg {
		t.Errorf("expected overlay config when base is nil")
	}
	if m, _ := MergeConfigs(baseCfg, nil); m != baseCfg {
		t.Errorf("expected base config when overlay is nil")
	}

	// AddIncludeToYAMLFile errors
	if _, err := AddIncludeToYAMLFile(filepath.Join(dir, "nonexistent.yaml"), "inc.yaml"); err == nil {
		t.Errorf("expected error for nonexistent file in AddIncludeToYAMLFile")
	}

	scalarFile := filepath.Join(dir, "scalar.yaml")
	_ = os.WriteFile(scalarFile, []byte("just_a_string\n"), 0644)
	if _, err := AddIncludeToYAMLFile(scalarFile, "inc.yaml"); err == nil {
		t.Errorf("expected error for scalar root node in AddIncludeToYAMLFile")
	}

	nonSeqFile := filepath.Join(dir, "non_seq.yaml")
	_ = os.WriteFile(nonSeqFile, []byte("include: not_a_sequence\n"), 0644)
	if _, err := AddIncludeToYAMLFile(nonSeqFile, "inc.yaml"); err == nil {
		t.Errorf("expected error for non-sequence include in AddIncludeToYAMLFile")
	}
}

func TestConfig_CUEValidationAndTemplateEscapes(t *testing.T) {
	dir := t.TempDir()

	// Invalid v2 schema (CUE validation error)
	invalidV2File := filepath.Join(dir, "invalid_v2.yaml")
	_ = os.WriteFile(invalidV2File, []byte("schema: lxm/config/v2\nname: test\nsudo: \"invalid_bool_string\"\n"), 0644)
	if _, err := LoadConfig(invalidV2File); err == nil {
		t.Errorf("expected error for CUE authoring schema validation failure")
	}

	// Tilde mount expansion & template escapes
	t.Setenv("MY_TEST_ENV", "env_val_123")
	tplFile := filepath.Join(dir, "tpl.yaml")
	tplContent := `schema: lxm/config/v2
name: "{{ .Name }}"
image: ubuntu:22.04
vars:
  my_var: "var_val_456"
mounts:
  - source: "~/my_data"
    path: "/mnt/data"
cloud-init: |
  #cloud-config
  echo "\{{\ .Vars.escaped \}}\"
  echo "{{ .Vars.my_var }}"
  echo "{{ .Env.MY_TEST_ENV }}"
`
	_ = os.WriteFile(tplFile, []byte(tplContent), 0644)
	loaded, err := LoadConfig(tplFile)
	if err != nil {
		t.Fatalf("LoadConfig with templates failed: %v", err)
	}
	if !filepath.IsAbs(loaded.Mounts[0].Source) {
		t.Errorf("expected tilde source to be expanded to absolute path, got %s", loaded.Mounts[0].Source)
	}
	if !strings.Contains(loaded.CloudInit, "{{ .Vars.escaped }}") {
		t.Errorf("expected escaped template tag to be restored, got %s", loaded.CloudInit)
	}
	if !strings.Contains(loaded.CloudInit, "var_val_456") || !strings.Contains(loaded.CloudInit, "env_val_123") {
		t.Errorf("expected substituted vars and env in cloud-init, got %s", loaded.CloudInit)
	}
}

func TestConfig_RemoveDirectivesAndHostKeys(t *testing.T) {
	// 1. RemoveDirectives success & error paths
	baseCfg := &Config{
		Mounts:   []Mount{{Source: "/src1", Path: "/mnt1"}, {Source: "/src2", Path: "/mnt2"}},
		Networks: []NetworkConfig{{Name: "eth0"}, {Name: "eth1"}},
		Recipes:  []RecipeGroup{{Scripts: []string{"setup.sh", "cleanup.sh"}}},
	}
	removeCfg := &Config{
		Remove: &RemoveDirective{
			Mounts:   []string{"/mnt1"},
			Networks: []string{"eth0"},
			Recipes:  []string{"setup.sh"},
		},
	}
	merged, err := MergeConfigs(baseCfg, removeCfg)
	if err != nil {
		t.Fatalf("MergeConfigs with Remove failed: %v", err)
	}
	if len(merged.Mounts) != 1 || merged.Mounts[0].Path != "/mnt2" {
		t.Errorf("expected mount /mnt1 removed, got %v", merged.Mounts)
	}
	if len(merged.Networks) != 1 || merged.Networks[0].Name != "eth1" {
		t.Errorf("expected network eth0 removed, got %v", merged.Networks)
	}
	if len(merged.Recipes) != 1 || merged.Recipes[0].Scripts[0] != "cleanup.sh" {
		t.Errorf("expected recipe setup.sh removed, got %v", merged.Recipes)
	}

	// Remove unmatched errors
	unmatchedMount := &Config{Remove: &RemoveDirective{Mounts: []string{"/nonexistent"}}}
	if _, err := MergeConfigs(baseCfg, unmatchedMount); err == nil {
		t.Errorf("expected error for unmatched remove mount")
	}

	unmatchedNet := &Config{Remove: &RemoveDirective{Networks: []string{"eth99"}}}
	if _, err := MergeConfigs(baseCfg, unmatchedNet); err == nil {
		t.Errorf("expected error for unmatched remove network")
	}

	unmatchedRec := &Config{Remove: &RemoveDirective{Recipes: []string{"nonexistent.sh"}}}
	if _, err := MergeConfigs(baseCfg, unmatchedRec); err == nil {
		t.Errorf("expected error for unmatched remove recipe")
	}

	// 2. DiscoverHostPublicKeys and DiscoverHostPrivateKeys with mock HOME
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	sshDir := filepath.Join(homeDir, ".ssh")
	_ = os.MkdirAll(sshDir, 0700)
	_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@host\n"), 0644)
	_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0600)

	pubKeys := DiscoverHostPublicKeys()
	if len(pubKeys) != 1 || !strings.Contains(pubKeys[0], "ssh-ed25519") {
		t.Errorf("expected 1 discovered public key, got %v", pubKeys)
	}

	privKeys := DiscoverHostPrivateKeys()
	if len(privKeys) != 1 || !strings.HasSuffix(privKeys[0], "id_ed25519") {
		t.Errorf("expected 1 discovered private key, got %v", privKeys)
	}

	// 3. ResolveCloudInit with Sudo, InjectSSHKeys, and existing users/write_files
	confInject := &Config{
		Name:          "inject-box",
		User:          "ubuntu",
		Sudo:          true,
		InjectSSHKeys: true,
		CloudInit:     "users:\n  - default\nwrite_files:\n  - path: /tmp/test\n",
	}
	resCI, err := confInject.ResolveCloudInit("")
	if err != nil {
		t.Fatalf("ResolveCloudInit with InjectSSHKeys failed: %v", err)
	}
	if !strings.Contains(resCI, "NOPASSWD:ALL") || !strings.Contains(resCI, "ssh-ed25519") || !strings.Contains(resCI, "LXM_USER=ubuntu") {
		t.Errorf("expected sudo, ssh_keys, and write_files in cloud-init output, got:\n%s", resCI)
	}

	// 4. ValidatePostMerge duplicate mounts and networks & Validate absent state error
	confDupMount := &Config{
		Mounts: []Mount{
			{Source: "/tmp/a", Path: "/mnt/dup"},
			{Source: "/tmp/b", Path: "/mnt/dup"},
		},
	}
	if err := ValidatePostMerge(confDupMount); err == nil {
		t.Errorf("expected error for duplicate mount paths in ValidatePostMerge")
	}

	confDupNet := &Config{
		Networks: []NetworkConfig{
			{Name: "eth0"},
			{Name: "eth0"},
		},
	}
	if err := ValidatePostMerge(confDupNet); err == nil {
		t.Errorf("expected error for duplicate network names in ValidatePostMerge")
	}

	confAbsentState := &Config{
		Name:   "box",
		Status: "absent",
		State:  "running",
	}
	if err := confAbsentState.Validate(""); err == nil {
		t.Errorf("expected error when status is 'absent' and state is specified")
	}

	// 5. HasIncludeInYAMLFile tests
	incTestFile := filepath.Join(t.TempDir(), "inc_test.yaml")
	_ = os.WriteFile(incTestFile, []byte("include: [a.yaml, b.yaml]\n"), 0644)

	hasA, err := HasIncludeInYAMLFile(incTestFile, "a.yaml")
	if err != nil || !hasA {
		t.Errorf("expected HasIncludeInYAMLFile true for a.yaml, got %v, err %v", hasA, err)
	}

	hasC, err := HasIncludeInYAMLFile(incTestFile, "c.yaml")
	if err != nil || hasC {
		t.Errorf("expected HasIncludeInYAMLFile false for c.yaml, got %v, err %v", hasC, err)
	}

	if _, err := HasIncludeInYAMLFile(filepath.Join(t.TempDir(), "missing.yaml"), "a.yaml"); err == nil {
		t.Errorf("expected error for missing file in HasIncludeInYAMLFile")
	}

	badIncFile := filepath.Join(t.TempDir(), "bad_inc.yaml")
	_ = os.WriteFile(badIncFile, []byte("include: [unclosed_list\n"), 0644)
	if _, err := HasIncludeInYAMLFile(badIncFile, "a.yaml"); err == nil {
		t.Errorf("expected error for invalid YAML in HasIncludeInYAMLFile")
	}

	// 6. validateCommon errors (empty mount source, non-absolute mount path, invalid cloud-init file)
	confEmptySrc := &Config{
		Name:   "box",
		Image:  "ubuntu:22.04",
		Mounts: []Mount{{Source: "", Path: "/mnt"}},
	}
	if err := confEmptySrc.Validate(""); err == nil {
		t.Errorf("expected error for empty mount source")
	}

	confRelDst := &Config{
		Name:   "box",
		Image:  "ubuntu:22.04",
		Mounts: []Mount{{Source: t.TempDir(), Path: "relative/path"}},
	}
	if err := confRelDst.Validate(""); err == nil {
		t.Errorf("expected error for relative mount destination path")
	}

	confBadCIFile := &Config{
		Name:          "box",
		Image:         "ubuntu:22.04",
		CloudInitFile: "nonexistent_ci_file_99.yaml",
	}
	if err := confBadCIFile.Validate(t.TempDir()); err == nil {
		t.Errorf("expected error for nonexistent cloud-init file in validateCommon")
	}
}

// ---------------------------------------------------------------------------
// Authoring-shorthand normalization (lxm_mount_bug.md §6.1/§6.2, test plan §8)
// ---------------------------------------------------------------------------

func TestConfig_MountsUnmarshal_MapForm(t *testing.T) {
	yamlStr := `
mounts:
  /var/log: /tmp/host-logs
  /srv/app:
    source: /tmp/app-src
    readonly: true
`
	var c Config
	if err := yaml.Unmarshal([]byte(yamlStr), &c); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(c.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(c.Mounts))
	}
	m0, m1 := c.Mounts[0], c.Mounts[1]
	if m0.Source != "/tmp/host-logs" || m0.Path != "/var/log" {
		t.Errorf("scalar map mount wrong: %+v", m0)
	}
	if m1.Source != "/tmp/app-src" || m1.Path != "/srv/app" || !m1.Readonly {
		t.Errorf("object map mount wrong: %+v", m1)
	}
}

func TestConfig_RecipesUnmarshal_Shorthands(t *testing.T) {
	yamlStr := `
recipes:
  - recipes/bootstrap.sh
  - root:
      - recipes/setup.sh
  - scripts:
      - recipes/legacy.sh
  - run_as: dev
    scripts:
      - recipes/user.sh
  - run-as: deploy
    scripts:
      - recipes/other.sh
`
	var c Config
	if err := yaml.Unmarshal([]byte(yamlStr), &c); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	want := []RecipeGroup{
		{RunAs: "root", Scripts: []string{"recipes/bootstrap.sh"}},
		{RunAs: "root", Scripts: []string{"recipes/setup.sh"}},
		{RunAs: "root", Scripts: []string{"recipes/legacy.sh"}},
		{RunAs: "dev", Scripts: []string{"recipes/user.sh"}},
		{RunAs: "deploy", Scripts: []string{"recipes/other.sh"}},
	}
	if len(c.Recipes) != len(want) {
		t.Fatalf("expected %d recipe groups, got %d: %+v", len(want), len(c.Recipes), c.Recipes)
	}
	for i, w := range want {
		got := c.Recipes[i]
		if got.RunAs != w.RunAs {
			t.Errorf("group %d: expected run_as %q, got %q", i, w.RunAs, got.RunAs)
		}
		if len(got.Scripts) != len(w.Scripts) || got.Scripts[0] != w.Scripts[0] {
			t.Errorf("group %d: expected scripts %v, got %v", i, w.Scripts, got.Scripts)
		}
	}
}

func TestConfig_RecipesUnmarshal_InvalidKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"mounts as scalar", "mounts: /a:/b\n"},
		{"recipes as scalar", "recipes: recipes/x.sh\n"},
		{"recipe group as sequence", "recipes:\n  - - a\n    - b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if err := yaml.Unmarshal([]byte(tc.yaml), &c); err == nil {
				t.Errorf("expected unmarshal error for %s", tc.name)
			}
		})
	}
}

func TestLoadConfig_MapFormMounts(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "map-mounts.yaml")
	content := `schema: lxm/config/v2
name: map-mounts
image: ubuntu:24.04
user: ubuntu
status: present
mounts:
  /var/log: /tmp/host-logs
  /srv/app:
    source: /tmp/app-src
    path: /srv/app
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("map-form mounts failed to load: %v", err)
	}
	if len(conf.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(conf.Mounts))
	}
	m0, m1 := conf.Mounts[0], conf.Mounts[1]
	if m0.Source != "/tmp/host-logs" || m0.Path != "/var/log" {
		t.Errorf("scalar map mount wrong: %+v", m0)
	}
	if m1.Source != "/tmp/app-src" || m1.Path != "/srv/app" {
		t.Errorf("object map mount wrong: %+v", m1)
	}
}

func TestLoadConfig_MapFormMounts_ProcKeyRejected(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "proc-mount.yaml")
	content := `schema: lxm/config/v2
name: proc-mount
image: ubuntu:24.04
user: ubuntu
status: present
mounts:
  /proc: /tmp/host-logs
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(configPath); err == nil {
		t.Fatalf("expected /proc map key to be rejected by #CleanMountPath, got nil error")
	}
}

func TestLoadConfig_RecipeShorthands(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "recipes.yaml")
	content := `schema: lxm/config/v2
name: recipes
image: ubuntu:24.04
user: ubuntu
status: present
recipes:
  - recipes/bootstrap.sh
  - root:
      - recipes/setup.sh
  - scripts:
      - recipes/legacy.sh
  - run_as: dev
    scripts:
      - recipes/user.sh
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("recipe shorthands failed to load: %v", err)
	}
	if len(conf.Recipes) != 4 {
		t.Fatalf("expected 4 recipe groups, got %d: %+v", len(conf.Recipes), conf.Recipes)
	}
	want := []RecipeGroup{
		{RunAs: "root", Scripts: []string{"recipes/bootstrap.sh"}},
		{RunAs: "root", Scripts: []string{"recipes/setup.sh"}},
		{RunAs: "root", Scripts: []string{"recipes/legacy.sh"}},
		{RunAs: "dev", Scripts: []string{"recipes/user.sh"}},
	}
	for i, w := range want {
		got := conf.Recipes[i]
		if got.RunAs != w.RunAs || len(got.Scripts) != 1 || got.Scripts[0] != w.Scripts[0] {
			t.Errorf("group %d: expected %+v, got %+v", i, w, got)
		}
	}
}

func TestLoadConfig_MapFormMounts_ObjectWithoutPath(t *testing.T) {
	// Style 2 map form: the map key supplies the container path when the
	// object value omits path (#MountMapObjAuthoring + loader key-default).
	dir := t.TempDir()
	configPath := filepath.Join(dir, "map-pathless.yaml")
	content := `schema: lxm/config/v2
name: map-pathless
image: ubuntu:24.04
user: ubuntu
status: present
mounts:
  /srv/app:
    source: /tmp/app-src
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("pathless map-form object mount failed to load: %v", err)
	}
	if len(conf.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(conf.Mounts))
	}
	if conf.Mounts[0].Source != "/tmp/app-src" || conf.Mounts[0].Path != "/srv/app" {
		t.Errorf("map key must supply the path, got %+v", conf.Mounts[0])
	}
}

func TestLoadConfig_RecipeAliasGroup(t *testing.T) {
	// Anchored recipe groups (YAML aliases) must still load; yaml.v3's
	// built-in slice decode resolved them before the Recipes type existed.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "alias-recipes.yaml")
	content := `schema: lxm/config/v2
name: alias-recipes
image: ubuntu:24.04
user: ubuntu
status: present
recipes:
  - &grp
    run_as: root
    scripts:
      - recipes/setup.sh
  - *grp
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("anchored recipe groups failed to load: %v", err)
	}
	if len(conf.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups, got %d: %+v", len(conf.Recipes), conf.Recipes)
	}
	for i, rg := range conf.Recipes {
		if rg.RunAs != "root" || len(rg.Scripts) != 1 || rg.Scripts[0] != "recipes/setup.sh" {
			t.Errorf("group %d wrong after alias resolution: %+v", i, rg)
		}
	}
}

func TestConfig_RecipesUnmarshal_PrunesEmptyAndCommentOnly(t *testing.T) {
	// Empty/comment-only shorthands and groups are pruned at load time,
	// mirroring the migrator's Transform 8 (lxm_mount_bug.md §8 item 2).
	yamlStr := `
recipes:
  - ""
  - "# just a comment"
  - recipes/real.sh
  - run_as: root
    scripts:
      - "# commented out"
  - run_as: root
    scripts:
      - "# header"
      - recipes/real2.sh
`
	var c Config
	if err := yaml.Unmarshal([]byte(yamlStr), &c); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(c.Recipes) != 2 {
		t.Fatalf("expected 2 surviving recipe groups, got %d: %+v", len(c.Recipes), c.Recipes)
	}
	if c.Recipes[0].Scripts[0] != "recipes/real.sh" {
		t.Errorf("expected real.sh first, got %+v", c.Recipes[0])
	}
	if c.Recipes[1].RunAs != "root" || len(c.Recipes[1].Scripts) != 1 || c.Recipes[1].Scripts[0] != "recipes/real2.sh" {
		t.Errorf("expected comment entry filtered from mixed group, got %+v", c.Recipes[1])
	}
}

func TestLoadConfig_EmptyRecipeGroup_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		recipes string
	}{
		{"explicit empty scripts", "  - run_as: root\n    scripts: []\n"},
		{"missing scripts key", "  - run_as: root\n"},
		{"comment-only scripts", "  - run_as: root\n    scripts:\n      - \"# just a comment\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "empty.yaml")
			content := "schema: lxm/config/v2\nname: empty-recipes\nimage: ubuntu:24.04\nuser: ubuntu\nstatus: present\nrecipes:\n" + tc.recipes
			if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(configPath); err == nil {
				t.Errorf("expected empty recipe group to be rejected (%s)", tc.name)
			}
		})
	}
}

func TestLoadConfig_ShorthandIncludeMerge(t *testing.T) {
	// Map-form mounts and scalar recipes in a base must merge (concat) with
	// object forms in the leaf, preserving base-first ordering.
	dir := t.TempDir()
	basePath := filepath.Join(dir, "_base.yaml")
	if err := os.WriteFile(basePath, []byte(`schema: lxm/config/v2
base: true
user: ubuntu
mounts:
  /shared: /host/shared
recipes:
  - recipes/common.sh
`), 0644); err != nil {
		t.Fatal(err)
	}
	leafPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(leafPath, []byte(`schema: lxm/config/v2
name: app
image: ubuntu:24.04
include:
  - _base.yaml
mounts:
  - source: /host/data
    path: /data
recipes:
  - run_as: dev
    scripts:
      - recipes/user.sh
`), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(leafPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(conf.Mounts) != 2 {
		t.Fatalf("expected 2 mounts after merge, got %d", len(conf.Mounts))
	}
	if conf.Mounts[0].Source != "/host/shared" || conf.Mounts[0].Path != "/shared" {
		t.Errorf("expected base map-form mount first, got %+v", conf.Mounts[0])
	}
	if conf.Mounts[1].Source != "/host/data" || conf.Mounts[1].Path != "/data" {
		t.Errorf("expected leaf object mount second, got %+v", conf.Mounts[1])
	}
	if len(conf.Recipes) != 2 {
		t.Fatalf("expected 2 recipe groups after merge, got %d", len(conf.Recipes))
	}
	if conf.Recipes[0].RunAs != "root" || conf.Recipes[0].Scripts[0] != "recipes/common.sh" {
		t.Errorf("expected base scalar recipe first, got %+v", conf.Recipes[0])
	}
	if conf.Recipes[1].RunAs != "dev" || conf.Recipes[1].Scripts[0] != "recipes/user.sh" {
		t.Errorf("expected leaf run_as recipe second, got %+v", conf.Recipes[1])
	}
}

func TestCPUCount_Unmarshal(t *testing.T) {
	tests := []struct {
		input   string
		valid   bool
		wantVal string
	}{
		{"4", true, "4"},
		{"\"4\"", true, "4"},
		{"\"0-3\"", true, "0-3"},
		{"\"0,2-3\"", true, "0,2-3"},
		{"0", false, ""},
		{"\"0\"", false, ""},
		{"\"invalid\"", false, ""},
	}

	for _, tt := range tests {
		var c CPUCount
		err := yaml.Unmarshal([]byte(tt.input), &c)
		if tt.valid && err != nil {
			t.Errorf("expected valid for %q, got error: %v", tt.input, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("expected error for %q, got success: %s", tt.input, string(c))
		}
		if tt.valid && string(c) != tt.wantVal {
			t.Errorf("for input %q, expected %q, got %q", tt.input, tt.wantVal, string(c))
		}
	}
}

func TestMergeConfigs_VM_Limits_WaitAgent(t *testing.T) {
	base := &Config{
		Type: "virtual-machine",
		Limits: &LimitsConfig{
			CPU:      "4",
			Memory:   "8GiB",
			Disk:     "50GiB",
			Presence: map[string]bool{"cpu": true, "memory": true, "disk": true},
		},
		VM: &VMConfig{
			BootMode:  "uefi-secureboot",
			Hugepages: false,
			Presence:  map[string]bool{"boot_mode": true, "hugepages": true},
		},
		WaitPolicy: WaitConfig{
			Agent:    "2m",
			Required: true,
			Presence: map[string]bool{"agent": true, "required": true},
		},
		presence: map[string]bool{"type": true, "limits": true, "vm": true, "wait": true},
	}

	overlay := &Config{
		Limits: &LimitsConfig{
			CPU:      "8",
			Presence: map[string]bool{"cpu": true},
		},
		VM: &VMConfig{
			RawQEMU:  "-cpu host",
			Presence: map[string]bool{"raw_qemu": true},
		},
		WaitPolicy: WaitConfig{
			Agent:    "3m",
			Presence: map[string]bool{"agent": true},
		},
		presence: map[string]bool{"limits": true, "vm": true, "wait": true},
	}

	merged, err := MergeConfigs(base, overlay)
	if err != nil {
		t.Fatalf("MergeConfigs failed: %v", err)
	}

	if merged.Type != "virtual-machine" {
		t.Errorf("expected Type virtual-machine, got %q", merged.Type)
	}
	if merged.Limits.CPU != "8" {
		t.Errorf("expected CPU 8, got %q", merged.Limits.CPU)
	}
	if merged.Limits.Memory != "8GiB" {
		t.Errorf("expected inherited Memory 8GiB, got %q", merged.Limits.Memory)
	}
	if merged.Limits.Disk != "50GiB" {
		t.Errorf("expected inherited Disk 50GiB, got %q", merged.Limits.Disk)
	}
	if merged.VM.BootMode != "uefi-secureboot" {
		t.Errorf("expected inherited BootMode uefi-secureboot, got %q", merged.VM.BootMode)
	}
	if merged.VM.RawQEMU != "-cpu host" {
		t.Errorf("expected RawQEMU -cpu host, got %q", merged.VM.RawQEMU)
	}
	if merged.WaitPolicy.Agent != "3m" {
		t.Errorf("expected WaitPolicy.Agent 3m, got %q", merged.WaitPolicy.Agent)
	}
}

func TestLoadConfig_VM_Normalization(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "vm.yaml")

	content := `schema: lxm/config/v2
name: test-vm
type: vm
image: ubuntu:24.04
user: ubuntu
status: present
limits:
  cpu: 4
  memory: 8GiB
  disk: 50GiB
vm:
  secureboot: false
mounts:
  - source: ` + dir + `
    path: /mnt/test
    shift: false
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfig(manifestPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if conf.Type != "virtual-machine" {
		t.Errorf("expected normalized Type virtual-machine, got %q", conf.Type)
	}
	if conf.VM == nil || conf.VM.BootMode != "uefi-nosecureboot" {
		t.Errorf("expected normalized BootMode uefi-nosecureboot, got %+v", conf.VM)
	}
	if conf.WaitPolicy.Agent != "2m" {
		t.Errorf("expected default VM wait.agent 2m, got %q", conf.WaitPolicy.Agent)
	}
	if len(conf.Mounts) != 1 || conf.Mounts[0].Shift == nil || *conf.Mounts[0].Shift != false {
		t.Errorf("expected mount shift false, got %+v", conf.Mounts[0])
	}
}

