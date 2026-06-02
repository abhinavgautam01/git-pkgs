package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakePopularPackageSource struct {
	packages map[string][]string
}

func (f fakePopularPackageSource) PopularPackages(_ context.Context, ecosystem string, _ int) ([]string, error) {
	return f.packages[ecosystem], nil
}

type fakePublicRegistryLookup struct {
	exists map[string]bool
}

func (f fakePublicRegistryLookup) PublicPackageExists(_ context.Context, dep dependency) (bool, string, error) {
	publicPURL := publicPURLForDependency(dep)
	return f.exists[dep.Name], publicPURL, nil
}

type fakeNPMManifestFetcher struct {
	scripts map[string]map[string]string
}

func (f fakeNPMManifestFetcher) NPMScripts(_ context.Context, name, _ string) (map[string]string, error) {
	scripts := f.scripts[name]
	if scripts == nil {
		return nil, errPackageNotFound
	}
	return scripts, nil
}

func TestDetectTyposquattingUsesPopularSource(t *testing.T) {
	deps := []dependency{
		{Name: "loadsh", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: "package.json"},
		{Name: "cross_env", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: "package.json"},
		{Name: "lodash", Ecosystem: "npm", Requirement: "4.17.21", ManifestPath: "package.json"},
	}

	issues, err := detectTyposquatting(context.Background(), deps, fakePopularPackageSource{
		packages: map[string][]string{"npm": {"lodash", "cross-env"}},
	}, 10)
	if err != nil {
		t.Fatalf("detect typosquatting: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues length = %d, want 2: %#v", len(issues), issues)
	}

	byName := make(map[string]riskIssue)
	for _, issue := range issues {
		byName[issue.Name] = issue
	}
	if byName["loadsh"].SimilarTo != "lodash" {
		t.Fatalf("loadsh similar_to = %q, want lodash", byName["loadsh"].SimilarTo)
	}
	if byName["cross_env"].SimilarTo != "cross-env" {
		t.Fatalf("cross_env similar_to = %q, want cross-env", byName["cross_env"].SimilarTo)
	}
}

func TestDetectDependencyConfusionRequiresPublicCollision(t *testing.T) {
	deps := []dependency{
		{
			Name:      "internal-tool",
			Ecosystem: "npm",
			PURL:      "pkg:npm/internal-tool@1.0.0?repository_url=https:%2F%2Fnpm.example.test",
		},
		{
			Name:      "private-only",
			Ecosystem: "npm",
			PURL:      "pkg:npm/private-only@1.0.0?repository_url=https:%2F%2Fnpm.example.test",
		},
		{
			Name:      "@company/safe",
			Ecosystem: "npm",
			PURL:      "pkg:npm/%40company/safe@1.0.0?repository_url=https:%2F%2Fnpm.example.test",
		},
		{
			Name:      "express",
			Ecosystem: "npm",
			PURL:      "pkg:npm/express@4.18.2",
		},
	}

	issues, err := detectDependencyConfusion(context.Background(), deps, fakePublicRegistryLookup{
		exists: map[string]bool{"internal-tool": true},
	})
	if err != nil {
		t.Fatalf("detect dependency confusion: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1: %#v", len(issues), issues)
	}
	if issues[0].Name != "internal-tool" {
		t.Fatalf("issue name = %q, want internal-tool", issues[0].Name)
	}
	if issues[0].Severity != "high" {
		t.Fatalf("severity = %q, want high", issues[0].Severity)
	}
	if issues[0].Evidence == "" {
		t.Fatalf("evidence is empty")
	}
}

func TestDetectInstallScriptsFromLockfileRegistryAndLocalFiles(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "package-lock.json")
	lockfile := npmLockfile{
		Packages: map[string]npmLockPackage{
			"node_modules/esbuild": {
				HasInstallScript: true,
			},
			"node_modules/safe": {},
		},
	}
	raw, err := json.Marshal(lockfile)
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(lockPath, raw, 0644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	rubyManifest := filepath.Join(dir, "Gemfile")
	if err := os.WriteFile(rubyManifest, []byte("source 'https://rubygems.org'\n"), 0644); err != nil {
		t.Fatalf("write Gemfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extconf.rb"), []byte("create_makefile('native')\n"), 0644); err != nil {
		t.Fatalf("write extconf.rb: %v", err)
	}

	deps := []dependency{
		{Name: "esbuild", Ecosystem: "npm", Requirement: "0.19.0", ManifestPath: lockPath},
		{Name: "native-npm", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: "package.json"},
		{Name: "safe", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: lockPath},
		{Name: "pkg-with-setup", Ecosystem: "pypi", Requirement: "1.0.0", ManifestPath: "setup.py"},
		{Name: "native-gem", Ecosystem: "rubygems", Requirement: "1.0.0", ManifestPath: rubyManifest},
	}

	issues, err := detectInstallScripts(context.Background(), deps, fakeNPMManifestFetcher{
		scripts: map[string]map[string]string{
			"native-npm": {"postinstall": "node build.js"},
		},
	})
	if err != nil {
		t.Fatalf("detect install scripts: %v", err)
	}

	byName := make(map[string]riskIssue)
	for _, issue := range issues {
		byName[issue.Name] = issue
	}
	for _, name := range []string{"esbuild", "native-npm", "pkg-with-setup", "native-gem"} {
		if byName[name].Name == "" {
			t.Fatalf("missing install-script issue for %s: %#v", name, issues)
		}
		if byName[name].Severity != "high" {
			t.Fatalf("%s severity = %q, want high", name, byName[name].Severity)
		}
	}
	if byName["safe"].Name != "" {
		t.Fatalf("unexpected install-script issue for safe: %#v", byName["safe"])
	}
}

func TestNPMManifestFetcherNotFound(t *testing.T) {
	_, err := fakeNPMManifestFetcher{}.NPMScripts(context.Background(), "missing", "")
	if !errors.Is(err, errPackageNotFound) {
		t.Fatalf("err = %v, want errPackageNotFound", err)
	}
}

func TestRunRiskChecksSummary(t *testing.T) {
	deps := []dependency{
		{Name: "loadsh", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: "package.json"},
		{Name: "loadsh", Ecosystem: "npm", Requirement: "1.0.0", ManifestPath: "package.json"},
	}
	result, err := runRiskChecks(context.Background(), deps, options{
		checks:       map[string]bool{checkTyposquat: true},
		popularLimit: 10,
	}, riskServices{
		popularPackages: fakePopularPackageSource{packages: map[string][]string{"npm": {"lodash"}}},
	})
	if err != nil {
		t.Fatalf("run risk checks: %v", err)
	}

	if result.Summary.TotalDependencies != 1 {
		t.Fatalf("total dependencies = %d, want 1", result.Summary.TotalDependencies)
	}
	if result.Summary.ByCheck[checkTyposquat] != 1 {
		t.Fatalf("typosquat issue count = %d, want 1", result.Summary.ByCheck[checkTyposquat])
	}
}
