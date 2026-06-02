package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/enrichment/scorecard"
)

type mockPackageLookup struct {
	packages map[string]*enrichment.PackageInfo
}

func (m mockPackageLookup) BulkLookup(_ context.Context, purls []string) (map[string]*enrichment.PackageInfo, error) {
	result := make(map[string]*enrichment.PackageInfo)
	for _, purlStr := range purls {
		if pkg, ok := m.packages[purlStr]; ok {
			result[purlStr] = pkg
		}
	}
	return result, nil
}

type mockScoreLookup struct {
	results map[string]*scorecard.Result
	errors  map[string]error
}

func (m mockScoreLookup) GetScore(_ context.Context, repoURL string) (*scorecard.Result, error) {
	if err := m.errors[repoURL]; err != nil {
		return nil, err
	}
	return m.results[repoURL], nil
}

func withMockLookups(packages map[string]*enrichment.PackageInfo, scores map[string]*scorecard.Result, errors map[string]error) func() {
	origPackageLookup := newPackageLookup
	origScoreLookup := newScoreLookup
	newPackageLookup = func() (packageLookup, error) {
		return mockPackageLookup{packages: packages}, nil
	}
	newScoreLookup = func() scoreLookup {
		return mockScoreLookup{results: scores, errors: errors}
	}
	return func() {
		newPackageLookup = origPackageLookup
		newScoreLookup = origScoreLookup
	}
}

func TestNormalizeRepository(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/lodash/lodash.git", "github.com/lodash/lodash"},
		{"github.com/expressjs/express", "github.com/expressjs/express"},
		{"https://gitlab.com/example/project", "gitlab.com/example/project"},
		{"https://github.com/owner", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeRepository(tt.input)
			if got != tt.want {
				t.Fatalf("normalizeRepository(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScorecardUserAgentShape(t *testing.T) {
	got := scorecardUserAgent()
	if !strings.HasPrefix(got, "git-pkgs/") {
		t.Fatalf("scorecard user agent = %q, want git-pkgs/<version>", got)
	}
}

func TestRunScorecardFiltersBelowAndChecks(t *testing.T) {
	restore := withMockLookups(
		map[string]*enrichment.PackageInfo{
			"pkg:npm/lodash":     {Repository: "https://github.com/lodash/lodash"},
			"pkg:npm/express":    {Repository: "https://github.com/expressjs/express"},
			"pkg:npm/gitlab-lib": {Repository: "https://gitlab.com/example/gitlab-lib"},
			"pkg:npm/no-repo":    {},
			"pkg:npm/bitbucket":  {Repository: "https://bitbucket.org/example/project"},
		},
		map[string]*scorecard.Result{
			"github.com/lodash/lodash": {
				Score: 8.5,
				Date:  "2026-01-01",
				Checks: []scorecard.Check{
					{Name: "Maintained", Score: 2, Reason: "low maintenance signal"},
					{Name: "Dangerous-Workflow", Score: 10},
				},
			},
			"github.com/expressjs/express": {
				Score: 6.1,
				Date:  "2026-01-01",
				Checks: []scorecard.Check{
					{Name: "Maintained", Score: 9},
				},
			},
			"gitlab.com/example/gitlab-lib": {
				Score: 9.9,
				Date:  "2026-01-01",
			},
		},
		nil,
	)
	defer restore()

	result, err := runScorecard([]dependency{
		{Name: "lodash", Ecosystem: "npm", Requirement: "4.17.21"},
		{Name: "express", Ecosystem: "npm", Requirement: "4.18.2"},
		{Name: "gitlab-lib", Ecosystem: "npm", Requirement: "1.0.0"},
		{Name: "no-repo", Ecosystem: "npm", Requirement: "1.0.0"},
		{Name: "bitbucket", Ecosystem: "npm", Requirement: "1.0.0"},
	}, options{
		below:  3,
		checks: map[string]bool{"maintained": true},
	})
	if err != nil {
		t.Fatalf("run scorecard: %v", err)
	}

	if result.Summary.TotalDependencies != 5 {
		t.Fatalf("total dependencies = %d, want 5", result.Summary.TotalDependencies)
	}
	if result.Summary.PackagesWithRepos != 3 {
		t.Fatalf("packages with repos = %d, want 3", result.Summary.PackagesWithRepos)
	}
	if result.Summary.SkippedNoRepository != 1 {
		t.Fatalf("skipped no repository = %d, want 1", result.Summary.SkippedNoRepository)
	}
	if result.Summary.SkippedUnsupportedRepo != 1 {
		t.Fatalf("skipped unsupported repository = %d, want 1", result.Summary.SkippedUnsupportedRepo)
	}
	if result.Summary.PolicyFailures != 1 {
		t.Fatalf("policy failures = %d, want 1", result.Summary.PolicyFailures)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("dependencies length = %d, want 1: %#v", len(result.Dependencies), result.Dependencies)
	}

	entry := result.Dependencies[0]
	if entry.Name != "lodash" {
		t.Fatalf("entry name = %q, want lodash", entry.Name)
	}
	if len(entry.Checks) != 1 || entry.Checks[0].Name != "Maintained" {
		t.Fatalf("checks = %#v, want Maintained only", entry.Checks)
	}
}

func TestRunScorecardReportsLookupErrors(t *testing.T) {
	restore := withMockLookups(
		map[string]*enrichment.PackageInfo{
			"pkg:npm/lodash": {Repository: "https://github.com/lodash/lodash"},
		},
		nil,
		map[string]error{
			"github.com/lodash/lodash": errors.New("scorecard unavailable"),
		},
	)
	defer restore()

	result, err := runScorecard([]dependency{
		{Name: "lodash", Ecosystem: "npm", Requirement: "4.17.21"},
	}, options{below: -1})
	if err != nil {
		t.Fatalf("run scorecard: %v", err)
	}

	if result.Summary.LookupErrors != 1 {
		t.Fatalf("lookup errors = %d, want 1", result.Summary.LookupErrors)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("dependencies length = %d, want 1", len(result.Dependencies))
	}
	if result.Dependencies[0].LookupError != "scorecard unavailable" {
		t.Fatalf("lookup error = %q, want scorecard unavailable", result.Dependencies[0].LookupError)
	}
	if result.Summary.PolicyFailures != 0 {
		t.Fatalf("policy failures = %d, want 0", result.Summary.PolicyFailures)
	}
}

func TestOutputTextAppliesBelowToChecksWithoutCheckFilter(t *testing.T) {
	result := &scorecardResult{
		Dependencies: []scorecardEntry{
			{
				Name:       "lodash",
				Ecosystem:  "npm",
				Repository: "github.com/lodash/lodash",
				Score:      8.5,
				Checks: []scorecardCheck{
					{Name: "Maintained", Score: 2},
					{Name: "Dangerous-Workflow", Score: 10},
				},
			},
		},
	}

	var out bytes.Buffer
	outputText(&out, result, options{below: 3})
	got := out.String()
	if !strings.Contains(got, "Maintained: 2") {
		t.Fatalf("expected low check in output: %q", got)
	}
	if strings.Contains(got, "Dangerous-Workflow: 10") {
		t.Fatalf("high-scoring check should be hidden by --below: %q", got)
	}
}

func TestPackagePURLForDependencyStripsVersion(t *testing.T) {
	got := packagePURLForDependency(dependency{
		Name:      "lodash",
		Ecosystem: "npm",
		PURL:      "pkg:npm/lodash@4.17.21",
	})
	if got != "pkg:npm/lodash" {
		t.Fatalf("package PURL = %q, want pkg:npm/lodash", got)
	}
}
