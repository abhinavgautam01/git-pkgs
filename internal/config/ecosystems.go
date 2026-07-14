package config

import (
	"errors"
	"os/exec"
	"sort"
	"strings"

	"github.com/git-pkgs/purl"
)

const (
	EcosystemsKey        = "pkgs.ecosystems"
	IgnoredEcosystemsKey = "pkgs.ignoredEcosystems"
)

type EcosystemFilter struct {
	allowed map[string]bool
	ignored map[string]bool
}

func LoadEcosystemFilter(dir string) (EcosystemFilter, error) {
	allowed, err := gitConfigValues(dir, EcosystemsKey)
	if err != nil {
		return EcosystemFilter{}, err
	}
	ignored, err := gitConfigValues(dir, IgnoredEcosystemsKey)
	if err != nil {
		return EcosystemFilter{}, err
	}

	return NewEcosystemFilter(allowed, ignored), nil
}

func NewEcosystemFilter(allowed, ignored []string) EcosystemFilter {
	return EcosystemFilter{
		allowed: ecosystemSet(allowed),
		ignored: ecosystemSet(ignored),
	}
}

func (f EcosystemFilter) Empty() bool {
	return len(f.allowed) == 0 && len(f.ignored) == 0
}

func (f EcosystemFilter) Allows(ecosystem string) bool {
	normalized := normalizeEcosystem(ecosystem)
	if normalized == "" {
		return true
	}
	if f.ignored[normalized] {
		return false
	}
	return len(f.allowed) == 0 || f.allowed[normalized]
}

// Values returns the canonical ecosystem values configured for the allow and
// ignore lists. The returned slices are sorted for deterministic query inputs.
func (f EcosystemFilter) Values() (allowed, ignored []string) {
	allowed = filterValues(f.allowed)
	ignored = filterValues(f.ignored)
	return allowed, ignored
}

func filterValues(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func gitConfigValues(dir, key string) ([]string, error) {
	cmd := exec.Command("git", "config", "--get-all", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return splitConfigValues(string(out)), nil
}

func splitConfigValues(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == ',' || r == ' ' || r == '\t'
	})
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func ecosystemSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if normalized := normalizeEcosystem(value); normalized != "" {
			set[normalized] = true
		}
	}
	return set
}

func normalizeEcosystem(ecosystem string) string {
	ecosystem = strings.ToLower(strings.TrimSpace(ecosystem))
	if ecosystem == "" {
		return ""
	}
	if ecosystem == "go" {
		return "golang"
	}
	if mapped := purl.PURLTypeToEcosystem(ecosystem); mapped != "" {
		return strings.ToLower(mapped)
	}
	return ecosystem
}
