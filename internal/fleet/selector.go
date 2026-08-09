package fleet

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aiyor/lxm/internal/config"
)

// Selector options for fleet targeting.
type SelectorOpts struct {
	Groups        []string
	ExcludeGroups []string
	Name          string
}

// Selector matches containers based on group membership and name patterns.
type Selector struct {
	groupSet        map[string]bool
	excludeGroupSet map[string]bool
	nameReg         *regexp.Regexp
}

// NewSelector constructs a Selector from options.
func NewSelector(opts SelectorOpts) (*Selector, error) {
	sel := &Selector{
		groupSet:        make(map[string]bool),
		excludeGroupSet: make(map[string]bool),
	}

	// 1. Process groups (OR across group values)
	for _, g := range opts.Groups {
		parts := strings.Split(g, ",")
		for _, p := range parts {
			clean := strings.TrimSpace(p)
			if clean != "" {
				sel.groupSet[clean] = true
			}
		}
	}

	// 2. Process exclude groups
	for _, eg := range opts.ExcludeGroups {
		parts := strings.Split(eg, ",")
		for _, p := range parts {
			clean := strings.TrimSpace(p)
			if clean != "" {
				sel.excludeGroupSet[clean] = true
			}
		}
	}

	// 3. Process name pattern (AND with group set)
	if opts.Name != "" {
		pat := opts.Name
		if !strings.HasPrefix(pat, "^") && !strings.HasSuffix(pat, "$") {
			pat = "^" + pat + "$"
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid name selector pattern %q: %w", opts.Name, err)
		}
		sel.nameReg = re
	}

	return sel, nil
}

// Matches returns true if the container satisfies selector criteria.
func (s *Selector) Matches(name string, groups []string) bool {
	// Exclude group check
	if len(s.excludeGroupSet) > 0 {
		for _, g := range groups {
			if s.excludeGroupSet[g] {
				return false
			}
		}
	}

	// Name match check (AND condition)
	if s.nameReg != nil {
		if !s.nameReg.MatchString(name) {
			return false
		}
	}

	// Group match check (OR condition across specified groups)
	if len(s.groupSet) > 0 {
		matchedGroup := false
		for _, g := range groups {
			if s.groupSet[g] {
				matchedGroup = true
				break
			}
		}
		if !matchedGroup {
			return false
		}
	}

	return true
}

// FilterConfigs filters a slice of manifests by the selector criteria.
// Returns an error with exit code 5 (TARGET_NOT_FOUND) if the result set is empty.
func (s *Selector) FilterConfigs(configs []*config.Config) ([]*config.Config, error) {
	var filtered []*config.Config
	for _, c := range configs {
		if s.Matches(c.Name, c.Groups) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("target not found: no manifests found matching filter criteria")
	}
	return filtered, nil
}
