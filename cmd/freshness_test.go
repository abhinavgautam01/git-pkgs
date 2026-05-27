package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/cmd"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

func TestFreshnessCommand(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	rootCmd := cmd.NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	seedFreshnessVersions(t, repoDir)

	t.Run("shows freshness summary", func(t *testing.T) {
		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"freshness"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("freshness failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "Average days behind latest: 40") {
			t.Errorf("expected average freshness metric, got: %s", output)
		}
		if !strings.Contains(output, "Maximum days behind latest: 91") {
			t.Errorf("expected max freshness metric, got: %s", output)
		}
		if !strings.Contains(output, "jest") || !strings.Contains(output, "express") {
			t.Errorf("expected lagging dependencies in output, got: %s", output)
		}
	})

	t.Run("outputs json with limit", func(t *testing.T) {
		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"freshness", "--format", "json", "--limit", "1"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("freshness json failed: %v", err)
		}

		var result cmd.FreshnessResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}

		if result.Summary.MeasuredDependencies != 3 {
			t.Fatalf("measured dependencies = %d, want 3", result.Summary.MeasuredDependencies)
		}
		if result.Summary.LaggingDependencies != 2 {
			t.Fatalf("lagging dependencies = %d, want 2", result.Summary.LaggingDependencies)
		}
		if result.Summary.AverageDaysBehindLatest != 40 {
			t.Fatalf("average days behind latest = %d, want 40", result.Summary.AverageDaysBehindLatest)
		}
		if len(result.Dependencies) != 1 {
			t.Fatalf("dependencies length = %d, want 1", len(result.Dependencies))
		}
		if result.Dependencies[0].Name != "jest" {
			t.Fatalf("first dependency = %q, want jest", result.Dependencies[0].Name)
		}
		if result.Dependencies[0].DaysBehindLatest != 91 {
			t.Fatalf("jest days behind latest = %d, want 91", result.Dependencies[0].DaysBehindLatest)
		}
	})
}

type failingFreshnessClient struct{}

func (f failingFreshnessClient) BulkLookup(_ context.Context, _ []string) (map[string]*enrichment.PackageInfo, error) {
	return nil, errors.New("metadata unavailable")
}

func (f failingFreshnessClient) GetVersions(_ context.Context, _ string) ([]enrichment.VersionInfo, error) {
	return nil, errors.New("metadata unavailable")
}

func (f failingFreshnessClient) GetVersion(_ context.Context, _ string) (*enrichment.VersionInfo, error) {
	return nil, errors.New("metadata unavailable")
}

func TestFreshnessCommandFailsWhenAllMetadataFetchesFail(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	rootCmd := cmd.NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	orig := cmd.NewEnrichmentClient
	cmd.NewEnrichmentClient = func(opts ...enrichment.Option) (enrichment.Client, error) {
		return failingFreshnessClient{}, nil
	}
	defer func() { cmd.NewEnrichmentClient = orig }()

	rootCmd = cmd.NewRootCmd()
	rootCmd.SetArgs([]string{"freshness"})
	rootCmd.SetOut(&bytes.Buffer{})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected freshness to fail when all metadata fetches fail")
	}
	if !strings.Contains(err.Error(), "fetching freshness metadata failed for all") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func seedFreshnessVersions(t *testing.T, repoDir string) {
	t.Helper()

	db, err := database.Open(filepath.Join(repoDir, ".git", "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer func() { _ = db.Close() }()

	mustTime := func(s string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parsing time %q: %v", s, err)
		}
		return parsed
	}

	versions := []database.CachedVersion{
		{PURL: "pkg:npm/express@4.18.2", PackagePURL: "pkg:npm/express", PublishedAt: mustTime("2023-01-01T00:00:00Z")},
		{PURL: "pkg:npm/express@4.19.0", PackagePURL: "pkg:npm/express", PublishedAt: mustTime("2023-01-31T00:00:00Z")},
		{PURL: "pkg:npm/lodash@4.17.21", PackagePURL: "pkg:npm/lodash", PublishedAt: mustTime("2021-02-20T00:00:00Z")},
		{PURL: "pkg:npm/jest@29.7.0", PackagePURL: "pkg:npm/jest", PublishedAt: mustTime("2023-01-01T00:00:00Z")},
		{PURL: "pkg:npm/jest@30.0.0", PackagePURL: "pkg:npm/jest", PublishedAt: mustTime("2023-04-02T00:00:00Z")},
	}
	if err := db.SaveVersions(versions); err != nil {
		t.Fatalf("saving versions: %v", err)
	}
}
