package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

type mockFundingEnrichmentClient struct {
	packages map[string]*enrichment.PackageInfo
}

func (m *mockFundingEnrichmentClient) BulkLookup(_ context.Context, purls []string) (map[string]*enrichment.PackageInfo, error) {
	result := make(map[string]*enrichment.PackageInfo)
	for _, purlStr := range purls {
		if pkg, ok := m.packages[purlStr]; ok {
			result[purlStr] = pkg
		}
	}
	return result, nil
}

func (m *mockFundingEnrichmentClient) GetVersions(_ context.Context, _ string) ([]enrichment.VersionInfo, error) {
	return nil, errors.New("not implemented")
}

func (m *mockFundingEnrichmentClient) GetVersion(_ context.Context, _ string) (*enrichment.VersionInfo, error) {
	return nil, errors.New("not implemented")
}

func setFundingMockEnrichment(packages map[string]*enrichment.PackageInfo) func() {
	orig := NewEnrichmentClient
	NewEnrichmentClient = func(opts ...enrichment.Option) (enrichment.Client, error) {
		return &mockFundingEnrichmentClient{packages: packages}, nil
	}
	return func() { NewEnrichmentClient = orig }
}

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
	packageData := map[string]*fundingPackageData{
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

func TestFetchFundingPackageDataUsesCache(t *testing.T) {
	db, err := database.Create(filepath.Join(t.TempDir(), "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer func() { _ = db.Close() }()

	err = db.SavePackageFundingBatch([]database.PackageFundingData{
		{
			PURL:      "pkg:npm/express",
			Ecosystem: "npm",
			Name:      "express",
			FundingLinks: []string{
				"https://opencollective.com/express",
			},
		},
	})
	if err != nil {
		t.Fatalf("save funding: %v", err)
	}

	deps := []database.Dependency{
		{Name: "express", Ecosystem: "npm", Requirement: "4.18.2", ManifestKind: manifestKindLockfile},
	}
	got, err := fetchFundingPackageData(db, deps)
	if err != nil {
		t.Fatalf("fetch funding: %v", err)
	}

	data := got["pkg:npm/express"]
	if data == nil {
		t.Fatal("expected cached funding data")
	}
	if len(data.FundingLinks) != 1 || data.FundingLinks[0] != "https://opencollective.com/express" {
		t.Fatalf("funding links = %#v, want cached link", data.FundingLinks)
	}
}

func TestFetchFundingPackageDataUsesEnrichmentFundingLinks(t *testing.T) {
	restore := setFundingMockEnrichment(map[string]*enrichment.PackageInfo{
		"pkg:npm/express": {
			FundingLinks: []string{
				"https://opencollective.com/express",
				"https://opencollective.com/express",
			},
		},
		"pkg:npm/private": {},
	})
	defer restore()

	deps := []database.Dependency{
		{Name: "express", Ecosystem: "npm", Requirement: "4.18.2", ManifestKind: manifestKindLockfile},
		{Name: "private", Ecosystem: "npm", Requirement: "1.0.0", ManifestKind: manifestKindLockfile},
		{Name: "missing", Ecosystem: "npm", Requirement: "1.0.0", ManifestKind: manifestKindLockfile},
	}
	got, err := fetchFundingPackageData(nil, deps)
	if err != nil {
		t.Fatalf("fetch funding: %v", err)
	}

	express := got["pkg:npm/express"]
	if express == nil {
		t.Fatal("expected express funding data")
	}
	if len(express.FundingLinks) != 1 || express.FundingLinks[0] != "https://opencollective.com/express" {
		t.Fatalf("express funding links = %#v, want deduped funding link", express.FundingLinks)
	}
	private := got["pkg:npm/private"]
	if private == nil {
		t.Fatal("expected private package data with empty funding links")
	}
	if len(private.FundingLinks) != 0 {
		t.Fatalf("private funding links = %#v, want empty", private.FundingLinks)
	}
	if got["pkg:npm/missing"] != nil {
		t.Fatalf("missing package data = %#v, want nil", got["pkg:npm/missing"])
	}
}

func TestOutputFundingJSONEmptyResult(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)

	if err := outputFundingJSON(root, emptyFundingResult()); err != nil {
		t.Fatalf("output json: %v", err)
	}

	var result FundingResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("parse json: %v; output: %s", err, out.String())
	}
	if result.Dependencies == nil {
		t.Fatal("dependencies = nil, want empty array")
	}
}
