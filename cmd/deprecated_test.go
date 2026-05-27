package cmd

import (
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
	"github.com/git-pkgs/registries"
)

func TestVersionedPURLForDependency(t *testing.T) {
	t.Run("builds versioned PURL from dependency", func(t *testing.T) {
		got := versionedPURLForDependency(database.Dependency{
			Name:        "lodash",
			Ecosystem:   "npm",
			Requirement: "4.17.21",
		})
		if got != "pkg:npm/lodash@4.17.21" {
			t.Fatalf("versioned PURL = %q, want pkg:npm/lodash@4.17.21", got)
		}
	})

	t.Run("preserves existing versioned PURL", func(t *testing.T) {
		got := versionedPURLForDependency(database.Dependency{
			PURL:        "pkg:npm/%40scope/pkg@1.0.0?repository_url=https://registry.example.test",
			Name:        "@scope/pkg",
			Ecosystem:   "npm",
			Requirement: "1.0.0",
		})
		if got != "pkg:npm/%40scope/pkg@1.0.0?repository_url=https://registry.example.test" {
			t.Fatalf("versioned PURL = %q, want existing PURL", got)
		}
	})
}

func TestDeprecatedPackages(t *testing.T) {
	purlToDeps := map[string][]database.Dependency{
		"pkg:npm/request@2.88.2": {
			{
				Name:         "request",
				Ecosystem:    "npm",
				Requirement:  "2.88.2",
				ManifestPath: "package-lock.json",
			},
		},
		"pkg:npm/lodash@4.17.21": {
			{
				Name:         "lodash",
				Ecosystem:    "npm",
				Requirement:  "4.17.21",
				ManifestPath: "package-lock.json",
			},
		},
	}
	versionData := map[string]*registries.Version{
		"pkg:npm/request@2.88.2": {
			Number: "2.88.2",
			Status: registries.StatusDeprecated,
			Metadata: map[string]any{
				"deprecated": "request has been deprecated",
			},
		},
		"pkg:npm/lodash@4.17.21": {
			Number: "4.17.21",
			Status: registries.StatusNone,
		},
	}

	got := deprecatedPackages(purlToDeps, versionData)
	if len(got) != 1 {
		t.Fatalf("deprecated count = %d, want 1", len(got))
	}
	if got[0].Name != "request" {
		t.Fatalf("deprecated package = %q, want request", got[0].Name)
	}
	if got[0].Message != "request has been deprecated" {
		t.Fatalf("message = %q, want deprecation message", got[0].Message)
	}
}
