package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
	"github.com/git-pkgs/registries"
	"github.com/spf13/cobra"
)

func TestMaintainerPURLForDependency(t *testing.T) {
	t.Run("builds package PURL without version", func(t *testing.T) {
		got := maintainerPURLForDependency(database.Dependency{
			Name:        "serde",
			Ecosystem:   "cargo",
			Requirement: "1.0.0",
		})
		if got != "pkg:cargo/serde" {
			t.Fatalf("maintainer PURL = %q, want pkg:cargo/serde", got)
		}
	})

	t.Run("strips version from existing PURL", func(t *testing.T) {
		got := maintainerPURLForDependency(database.Dependency{
			PURL:        "pkg:npm/%40scope/pkg@1.0.0?repository_url=https://registry.example.test",
			Name:        "@scope/pkg",
			Ecosystem:   "npm",
			Requirement: "1.0.0",
		})
		want := "pkg:npm/%40scope/pkg?repository_url=https:%2F%2Fregistry.example.test"
		if got != want {
			t.Fatalf("maintainer PURL = %q, want %q", got, want)
		}
	})
}

func TestSelectMaintainerDependencies(t *testing.T) {
	deps := []database.Dependency{
		{Name: "express", ManifestKind: "manifest"},
		{Name: "accepts", Requirement: "1.3.8", ManifestKind: manifestKindLockfile},
		{Name: "go", Ecosystem: "golang", Requirement: "1.0.0", ManifestKind: "manifest"},
	}

	direct := selectMaintainerDependencies(deps, false)
	if len(direct) != 2 {
		t.Fatalf("direct deps length = %d, want 2", len(direct))
	}
	if direct[0].Name != "express" || direct[1].Name != "go" {
		t.Fatalf("direct deps = %#v, want express and go", direct)
	}

	all := selectMaintainerDependencies(deps, true)
	if len(all) != 3 {
		t.Fatalf("all deps length = %d, want 3", len(all))
	}
	if all[0].Name != "express" || all[1].Name != "accepts" || all[2].Name != "go" {
		t.Fatalf("all deps = %#v, want express, accepts, and go", all)
	}
}

func TestBuildMaintainersResult(t *testing.T) {
	deps := []database.Dependency{
		{
			Name:         "express",
			Ecosystem:    "npm",
			Requirement:  "4.18.2",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
		{
			Name:         "lodash",
			Ecosystem:    "npm",
			Requirement:  "4.17.21",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
		{
			Name:         "broken",
			Ecosystem:    "npm",
			Requirement:  "1.0.0",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
		{
			Name:         "express",
			Ecosystem:    "npm",
			Requirement:  "4.18.2",
			ManifestPath: "other/package.json",
			ManifestKind: "manifest",
		},
	}
	lookup := map[string]maintainerLookupResult{
		"pkg:npm/express": {
			Maintainers: []registries.Maintainer{
				{Login: "alice"},
				{Login: "alice"},
			},
		},
		"pkg:npm/lodash": {},
		"pkg:npm/broken": {Error: "unsupported ecosystem"},
	}

	t.Run("all packages", func(t *testing.T) {
		result := buildMaintainersResult(deps, lookup, false)
		if result.Summary.TotalDependencies != 4 {
			t.Fatalf("total dependencies = %d, want 4", result.Summary.TotalDependencies)
		}
		if result.Summary.QueriedDependencies != 4 {
			t.Fatalf("queried dependencies = %d, want 4", result.Summary.QueriedDependencies)
		}
		if result.Summary.WithMaintainers != 2 {
			t.Fatalf("with maintainers = %d, want 2", result.Summary.WithMaintainers)
		}
		if result.Summary.WithoutMaintainers != 1 {
			t.Fatalf("without maintainers = %d, want 1", result.Summary.WithoutMaintainers)
		}
		if result.Summary.SingleMaintainer != 2 {
			t.Fatalf("single maintainer = %d, want 2", result.Summary.SingleMaintainer)
		}
		if result.Summary.LookupErrors != 1 {
			t.Fatalf("lookup errors = %d, want 1", result.Summary.LookupErrors)
		}
		if len(result.Dependencies) != 4 {
			t.Fatalf("dependencies length = %d, want 4", len(result.Dependencies))
		}

		manifestPaths := map[string]bool{}
		for _, dep := range result.Dependencies {
			if dep.Name == "express" {
				manifestPaths[dep.ManifestPath] = true
			}
		}
		if !manifestPaths["package.json"] || !manifestPaths["other/package.json"] {
			t.Fatalf("expected express entries for both manifests, got %#v", result.Dependencies)
		}
	})

	t.Run("single maintainer only", func(t *testing.T) {
		result := buildMaintainersResult(deps, lookup, true)
		if len(result.Dependencies) != 2 {
			t.Fatalf("dependencies length = %d, want 2", len(result.Dependencies))
		}
		if result.Dependencies[0].Name != "express" {
			t.Fatalf("dependency = %q, want express", result.Dependencies[0].Name)
		}
		if result.Dependencies[0].MaintainerCount != 1 {
			t.Fatalf("maintainer count = %d, want 1", result.Dependencies[0].MaintainerCount)
		}
	})
}

func TestFetchMaintainerDataUsesCache(t *testing.T) {
	db, err := database.Create(filepath.Join(t.TempDir(), "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer func() { _ = db.Close() }()

	maintainers := []registries.Maintainer{{Login: "alice"}}
	raw, err := json.Marshal(maintainers)
	if err != nil {
		t.Fatalf("marshal maintainers: %v", err)
	}
	err = db.SavePackageMaintainersBatch([]database.PackageMaintainersData{
		{
			PURL:        "pkg:npm/express",
			Ecosystem:   "npm",
			Name:        "express",
			Maintainers: string(raw),
		},
	})
	if err != nil {
		t.Fatalf("save maintainers: %v", err)
	}

	deps := []database.Dependency{
		{Name: "express", Ecosystem: "npm", ManifestKind: "manifest"},
	}
	got := fetchMaintainerData(context.Background(), db, deps, []string{"pkg:npm/express"})
	result := got["pkg:npm/express"]
	if result.Error != "" {
		t.Fatalf("lookup error = %q, want none", result.Error)
	}
	if len(result.Maintainers) != 1 || result.Maintainers[0].Login != "alice" {
		t.Fatalf("maintainers = %#v, want alice", result.Maintainers)
	}
}

func TestOutputMaintainersJSONEmptyResult(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	if err := outputMaintainersJSON(cmd, emptyMaintainersResult()); err != nil {
		t.Fatalf("output json: %v", err)
	}
	if !strings.Contains(out.String(), `"dependencies": []`) {
		t.Fatalf("json output = %q, want empty dependencies array", out.String())
	}
}

func TestFormatMaintainerNames(t *testing.T) {
	got := formatMaintainerNames([]MaintainerInfo{
		{Login: "alice", Role: "owner"},
		{Name: "Bob"},
	})
	want := "alice (owner), Bob"
	if got != want {
		t.Fatalf("maintainer names = %q, want %q", got, want)
	}
}
