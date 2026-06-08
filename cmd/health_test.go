package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

type mockHealthEnrichmentClient struct {
	packages map[string]*enrichment.PackageInfo
}

func (m *mockHealthEnrichmentClient) BulkLookup(_ context.Context, purls []string) (map[string]*enrichment.PackageInfo, error) {
	result := make(map[string]*enrichment.PackageInfo)
	for _, purlStr := range purls {
		if pkg, ok := m.packages[purlStr]; ok {
			result[purlStr] = pkg
		}
	}
	return result, nil
}

func (m *mockHealthEnrichmentClient) GetVersions(_ context.Context, _ string) ([]enrichment.VersionInfo, error) {
	return nil, errors.New("not implemented")
}

func (m *mockHealthEnrichmentClient) GetVersion(_ context.Context, _ string) (*enrichment.VersionInfo, error) {
	return nil, errors.New("not implemented")
}

func setHealthMockEnrichment(packages map[string]*enrichment.PackageInfo) func() {
	orig := NewEnrichmentClient
	NewEnrichmentClient = func(opts ...enrichment.Option) (enrichment.Client, error) {
		return &mockHealthEnrichmentClient{packages: packages}, nil
	}
	return func() { NewEnrichmentClient = orig }
}

func TestBuildHealthResult(t *testing.T) {
	deps := []database.Dependency{
		{
			Name:         "express",
			Ecosystem:    "npm",
			Requirement:  "^4.18.0",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
		{
			Name:         "left-pad",
			Ecosystem:    "npm",
			Requirement:  "^1.3.0",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
	}
	packageData := map[string]*healthPackageData{
		"pkg:npm/express": {
			MaintainerCount:        4,
			Downloads:              1000000,
			DownloadsPeriod:        "last-month",
			DependentPackagesCount: 500,
			DependentReposCount:    10000,
		},
		"pkg:npm/left-pad": {},
	}

	result := buildHealthResult(deps, packageData, 70)
	if result.Summary.TotalDependencies != 2 {
		t.Fatalf("total dependencies = %d, want 2", result.Summary.TotalDependencies)
	}
	if result.Summary.CheckedDependencies != 2 {
		t.Fatalf("checked dependencies = %d, want 2", result.Summary.CheckedDependencies)
	}
	if result.Summary.AtRiskDependencies != 1 {
		t.Fatalf("at-risk dependencies = %d, want 1", result.Summary.AtRiskDependencies)
	}
	if result.Summary.AverageScore != 77 {
		t.Fatalf("average score = %d, want 77", result.Summary.AverageScore)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("displayed dependencies = %d, want 1", len(result.Dependencies))
	}

	entry := result.Dependencies[0]
	if entry.Name != "left-pad" {
		t.Fatalf("dependency = %q, want left-pad", entry.Name)
	}
	if entry.Score != 55 {
		t.Fatalf("score = %d, want 55", entry.Score)
	}
	if entry.Risk != "medium" {
		t.Fatalf("risk = %q, want medium", entry.Risk)
	}

	result = buildHealthResult(deps, packageData, 50)
	if result.Summary.AtRiskDependencies != 0 {
		t.Fatalf("at-risk dependencies at threshold 50 = %d, want 0", result.Summary.AtRiskDependencies)
	}

	boundaryDeps := []database.Dependency{
		{
			Name:         "boundary",
			Ecosystem:    "npm",
			Requirement:  "^1.0.0",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
	}
	boundaryData := map[string]*healthPackageData{
		"pkg:npm/boundary": {
			MaintainerCount:        0,
			Downloads:              100,
			DependentPackagesCount: 20,
			DependentReposCount:    20,
		},
	}
	result = buildHealthResult(boundaryDeps, boundaryData, 70)
	if result.Summary.AtRiskDependencies != 1 {
		t.Fatalf("at-risk dependencies at threshold boundary = %d, want 1", result.Summary.AtRiskDependencies)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("displayed dependencies at threshold boundary = %d, want 1", len(result.Dependencies))
	}
	if result.Dependencies[0].Score != 70 {
		t.Fatalf("boundary score = %d, want 70", result.Dependencies[0].Score)
	}
}

func TestFetchHealthPackageDataUsesCache(t *testing.T) {
	db, err := database.Create(filepath.Join(t.TempDir(), "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer func() { _ = db.Close() }()

	err = db.SavePackageHealthBatch([]database.PackageHealthData{
		{
			PURL:                   "pkg:npm/express",
			Ecosystem:              "npm",
			Name:                   "express",
			Downloads:              5000,
			DownloadsPeriod:        "last-month",
			DependentPackagesCount: 20,
			DependentReposCount:    40,
		},
	})
	if err != nil {
		t.Fatalf("save health: %v", err)
	}
	err = db.SavePackageMaintainersBatch([]database.PackageMaintainersData{
		{
			PURL:        "pkg:npm/express",
			Ecosystem:   "npm",
			Name:        "express",
			Maintainers: `[{"login":"alice"},{"login":"bob"}]`,
		},
	})
	if err != nil {
		t.Fatalf("save maintainers: %v", err)
	}

	deps := []database.Dependency{
		{Name: "express", Ecosystem: "npm", Requirement: "^4.18.0", ManifestKind: "manifest"},
	}
	got, err := fetchHealthPackageData(db, deps)
	if err != nil {
		t.Fatalf("fetch health: %v", err)
	}

	data := got["pkg:npm/express"]
	if data == nil {
		t.Fatal("expected cached health data")
	}
	if data.DependentReposCount != 40 {
		t.Fatalf("dependent repos count = %d, want 40", data.DependentReposCount)
	}
	if data.MaintainerCount != 2 {
		t.Fatalf("maintainer count = %d, want 2", data.MaintainerCount)
	}
}

func TestFetchHealthPackageDataUsesEnrichment(t *testing.T) {
	restore := setHealthMockEnrichment(map[string]*enrichment.PackageInfo{
		"pkg:npm/express": {
			Downloads:              1000,
			DownloadsPeriod:        "last-month",
			DependentPackagesCount: 50,
			DependentReposCount:    100,
			Maintainers: []enrichment.Maintainer{
				{Login: "alice"},
				{Login: "bob"},
			},
		},
	})
	defer restore()

	deps := []database.Dependency{
		{Name: "express", Ecosystem: "npm", Requirement: "^4.18.0", ManifestKind: "manifest"},
		{Name: "missing", Ecosystem: "npm", Requirement: "^1.0.0", ManifestKind: "manifest"},
	}
	got, err := fetchHealthPackageData(nil, deps)
	if err != nil {
		t.Fatalf("fetch health: %v", err)
	}

	express := got["pkg:npm/express"]
	if express == nil {
		t.Fatal("expected express health data")
	}
	if express.MaintainerCount != 2 {
		t.Fatalf("maintainer count = %d, want 2", express.MaintainerCount)
	}
	if express.Downloads != 1000 {
		t.Fatalf("downloads = %d, want 1000", express.Downloads)
	}
	if got["pkg:npm/missing"] != nil {
		t.Fatal("did not expect missing package data")
	}
}

func TestFetchHealthPackageDataCachedScoreMatchesColdLookup(t *testing.T) {
	restore := setHealthMockEnrichment(map[string]*enrichment.PackageInfo{
		"pkg:npm/express": {
			Downloads:              1000,
			DownloadsPeriod:        "last-month",
			DependentPackagesCount: 50,
			DependentReposCount:    100,
			Maintainers: []enrichment.Maintainer{
				{Login: "alice"},
				{Login: "bob"},
			},
		},
	})
	defer restore()

	db, err := database.Create(filepath.Join(t.TempDir(), "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer func() { _ = db.Close() }()

	deps := []database.Dependency{
		{
			Name:         "express",
			Ecosystem:    "npm",
			Requirement:  "^4.18.0",
			ManifestPath: "package.json",
			ManifestKind: "manifest",
		},
	}

	coldData, err := fetchHealthPackageData(db, deps)
	if err != nil {
		t.Fatalf("fetch cold health: %v", err)
	}
	cold := buildHealthResult(deps, coldData, 100)

	warmData, err := fetchHealthPackageData(db, deps)
	if err != nil {
		t.Fatalf("fetch cached health: %v", err)
	}
	warm := buildHealthResult(deps, warmData, 100)

	if len(cold.Dependencies) != 1 || len(warm.Dependencies) != 1 {
		t.Fatalf("dependency counts cold=%d warm=%d, want 1 each", len(cold.Dependencies), len(warm.Dependencies))
	}
	if cold.Dependencies[0].MaintainerCount != warm.Dependencies[0].MaintainerCount {
		t.Fatalf(
			"maintainer count changed after cache hit: cold=%d warm=%d",
			cold.Dependencies[0].MaintainerCount,
			warm.Dependencies[0].MaintainerCount,
		)
	}
	if cold.Dependencies[0].Score != warm.Dependencies[0].Score {
		t.Fatalf("score changed after cache hit: cold=%d warm=%d", cold.Dependencies[0].Score, warm.Dependencies[0].Score)
	}
}
