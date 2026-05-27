package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ecosyste-ms/ecosystems-go/packages"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

func TestFundingPURLForDependency(t *testing.T) {
	t.Run("builds package PURL without version", func(t *testing.T) {
		got := fundingPURLForDependency(database.Dependency{
			Name:        "lodash",
			Ecosystem:   "npm",
			Requirement: "4.17.21",
		})
		if got != "pkg:npm/lodash" {
			t.Fatalf("funding PURL = %q, want pkg:npm/lodash", got)
		}
	})

	t.Run("strips version from existing PURL", func(t *testing.T) {
		got := fundingPURLForDependency(database.Dependency{
			PURL:        "pkg:npm/%40scope/pkg@1.0.0?repository_url=https://registry.example.test",
			Name:        "@scope/pkg",
			Ecosystem:   "npm",
			Requirement: "1.0.0",
		})
		want := "pkg:npm/%40scope/pkg?repository_url=https:%2F%2Fregistry.example.test"
		if got != want {
			t.Fatalf("funding PURL = %q, want %q", got, want)
		}
	})
}

func TestOutputFundingEmptyNoMetadata(t *testing.T) {
	result := &FundingResult{
		Summary: FundingSummary{
			TotalDependencies:     2,
			UnresolvedPackageData: 2,
		},
	}

	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)

	outputFundingEmpty(root, result, true)

	got := buf.String()
	if strings.Contains(got, "All checked dependencies have funding links") {
		t.Fatalf("expected no definitive funding message, got %q", got)
	}
	if !strings.Contains(got, "No package metadata resolved for 2 dependencies") {
		t.Fatalf("expected unresolved metadata message, got %q", got)
	}
}

func TestBuildFundingResult(t *testing.T) {
	deps := []database.Dependency{
		{
			Name:         "express",
			Ecosystem:    "npm",
			Requirement:  "4.18.2",
			ManifestPath: "package-lock.json",
			ManifestKind: manifestKindLockfile,
		},
		{
			Name:         "lodash",
			Ecosystem:    "npm",
			Requirement:  "4.17.21",
			ManifestPath: "package-lock.json",
			ManifestKind: manifestKindLockfile,
		},
	}
	packageData := map[string]*packages.PackageWithRegistry{
		"pkg:npm/express": {
			FundingLinks: []string{
				"https://opencollective.com/express",
				"https://opencollective.com/express",
				"https://github.com/sponsors/expressjs",
			},
		},
		"pkg:npm/lodash": {},
	}

	t.Run("with funding", func(t *testing.T) {
		result := buildFundingResult(deps, packageData, false)
		if result.Summary.TotalDependencies != 2 {
			t.Fatalf("total dependencies = %d, want 2", result.Summary.TotalDependencies)
		}
		if result.Summary.CheckedDependencies != 2 {
			t.Fatalf("checked dependencies = %d, want 2", result.Summary.CheckedDependencies)
		}
		if result.Summary.WithFunding != 1 {
			t.Fatalf("with funding = %d, want 1", result.Summary.WithFunding)
		}
		if result.Summary.WithoutFunding != 1 {
			t.Fatalf("without funding = %d, want 1", result.Summary.WithoutFunding)
		}
		if len(result.Dependencies) != 1 {
			t.Fatalf("dependencies length = %d, want 1", len(result.Dependencies))
		}
		if result.Dependencies[0].Name != "express" {
			t.Fatalf("dependency = %q, want express", result.Dependencies[0].Name)
		}
		if len(result.Dependencies[0].FundingLinks) != 2 {
			t.Fatalf("funding links length = %d, want 2", len(result.Dependencies[0].FundingLinks))
		}
	})

	t.Run("missing funding", func(t *testing.T) {
		result := buildFundingResult(deps, packageData, true)
		if len(result.Dependencies) != 1 {
			t.Fatalf("dependencies length = %d, want 1", len(result.Dependencies))
		}
		if result.Dependencies[0].Name != "lodash" {
			t.Fatalf("dependency = %q, want lodash", result.Dependencies[0].Name)
		}
	})
}
