package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func discoverYAMLFiles(target string, includeHidden bool, logger *slog.Logger) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{target}, nil
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		if strings.HasPrefix(name, "_") && !includeHidden {
			if logger != nil {
				logger.Warn("Skipping base config file; remove _ prefix or add 'base: true' if intended as a container", "file", name)
			}
			continue
		}
		files = append(files, filepath.Join(target, name))
	}
	return files, nil
}

func hasAnyGroup(groups []string, targets []string) bool {
	for _, g := range groups {
		for _, t := range targets {
			if g == t {
				return true
			}
		}
	}
	return false
}

// manifestProbe captures the minimal top-level keys needed to classify a YAML
// file during doctor's un-migrated scan.
type manifestProbe struct {
	Schema string `yaml:"schema"`
	Base   bool   `yaml:"base"`
	Name   string `yaml:"name"`
	Image  string `yaml:"image"`
}

// probeManifestFile lightly parses a YAML file's top-level manifest keys. It
// deliberately does not use config.LoadConfig, which requires a fully resolved
// manifest and therefore fails on base files (no name) by design — exactly the
// files doctor must not misreport as un-migrated.
func probeManifestFile(path string) (*manifestProbe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe manifestProbe
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	return &probe, nil
}

func shouldSkipByGroup(groups []string, groupFilters, excludeFilters []string) bool {
	if len(groupFilters) > 0 && !hasAnyGroup(groups, groupFilters) {
		return true
	}
	if len(excludeFilters) > 0 && hasAnyGroup(groups, excludeFilters) {
		return true
	}
	return false
}
