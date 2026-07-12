package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/cmd"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

func TestIntegrityCommand(t *testing.T) {
	t.Run("shows integrity hashes", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add package.json")
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("integrity failed: %v", err)
		}

		output := stdout.String()
		// Should show packages with integrity hashes
		if !strings.Contains(output, "sha512") {
			t.Errorf("expected sha512 hash in output, got: %s", output)
		}
		if !strings.Contains(output, "express") {
			t.Errorf("expected 'express' in output, got: %s", output)
		}
	})

	t.Run("drift flag filters output", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity", "--drift"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("integrity --drift failed: %v", err)
		}

		output := stdout.String()
		// Should report no drift in a single lockfile
		if !strings.Contains(output, "No integrity drift") && !strings.Contains(output, "drift") {
			t.Errorf("expected drift-related output, got: %s", output)
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("integrity failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		if len(result) == 0 {
			t.Error("expected at least one package in JSON output")
		}

		// Validate structure
		first := result[0]
		if _, ok := first["name"]; !ok {
			t.Error("expected 'name' field in integrity JSON")
		}
		if _, ok := first["integrity"]; !ok {
			t.Error("expected 'integrity' field in integrity JSON")
		}
	})

	t.Run("on-demand indexing without init", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		// Don't init - should create database on demand
		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("integrity with on-demand indexing failed: %v", err)
		}
	})

	t.Run("registry check reuses cached integrity", func(t *testing.T) {
		mock, restore := setMockEnrichmentWithVersionInfos(nil, map[string]*enrichment.VersionInfo{
			"pkg:npm/express@4.18.2": {
				Number:    "4.18.2",
				Integrity: "sha512-5/PsL6iGPdfQ/lKM1UuielYgv3BUoJfz1aUwU9vHZ+J7gyvwdQXFEBIEIaxeGf0GIcreATNyBExtalisDbuMqQ==",
			},
			"pkg:npm/lodash@4.17.21": {
				Number:    "4.17.21",
				Integrity: "sha512-v2kDEe57lecTulaDIuNTPy3Ry4gLGJ6Z1O3vE1krgXZNrsQ+LFTGHVxVjcXPs17LhbZVGedAJv8XZ1tvj5FvSg==",
			},
			"pkg:npm/jest@29.7.0": {
				Number:    "29.7.0",
				Integrity: "sha512-NIy3oAFp9shda19ez4HgzXfkzNkFXGj2V8m5xk6xWe/5ESrq7+IzhPRXbqAIEr5E0F5FDp8w1DQFV8+SqGbNwg==",
			},
		})
		defer restore()

		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		publishedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		db, err := database.Open(filepath.Join(repoDir, ".git", "pkgs.sqlite3"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.SaveVersions([]database.CachedVersion{{
			PURL:        "pkg:npm/express@4.18.2",
			PackagePURL: "pkg:npm/express",
			License:     "MIT",
			PublishedAt: publishedAt,
			Integrity:   "sha512-5/PsL6iGPdfQ/lKM1UuielYgv3BUoJfz1aUwU9vHZ+J7gyvwdQXFEBIEIaxeGf0GIcreATNyBExtalisDbuMqQ==",
		}}); err != nil {
			_ = db.Close()
			t.Fatalf("seed cached integrity: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity", "--registry", "--format", "json"})
		rootCmd.SetOut(&stdout)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("first registry check failed: %v", err)
		}

		var first cmd.RegistryCheckResult
		if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
			t.Fatalf("parse first registry result: %v", err)
		}
		if first.Checked != 3 || first.Skipped != 0 {
			t.Fatalf("first registry summary = checked %d, skipped %d; want 3, 0", first.Checked, first.Skipped)
		}

		mock.mu.Lock()
		firstCalls := mock.getVersionCalls
		mock.mu.Unlock()
		if firstCalls != 2 {
			t.Fatalf("first registry check made %d version calls, want 2 cache misses", firstCalls)
		}

		db, err = database.Open(filepath.Join(repoDir, ".git", "pkgs.sqlite3"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.SaveVersions([]database.CachedVersion{{
			PURL:        "pkg:npm/express@4.18.2",
			PackagePURL: "pkg:npm/express",
		}}); err != nil {
			_ = db.Close()
			t.Fatalf("update cached version without integrity: %v", err)
		}
		versions, err := db.GetCachedVersions("pkg:npm/express", 24*time.Hour)
		if err != nil {
			_ = db.Close()
			t.Fatalf("read cached version: %v", err)
		}
		if len(versions) != 1 {
			_ = db.Close()
			t.Fatalf("cached version count = %d, want 1", len(versions))
		}
		if got := versions[0]; got.License != "MIT" || !got.PublishedAt.Equal(publishedAt) || got.Integrity == "" {
			_ = db.Close()
			t.Fatalf("partial version update lost cached fields: %+v", got)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		mock.mu.Lock()
		mock.getVersionCalls = 0
		mock.mu.Unlock()

		stdout.Reset()
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"integrity", "--registry", "--format", "json"})
		rootCmd.SetOut(&stdout)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("cached registry check failed: %v", err)
		}

		var cached cmd.RegistryCheckResult
		if err := json.Unmarshal(stdout.Bytes(), &cached); err != nil {
			t.Fatalf("parse cached registry result: %v", err)
		}
		if cached.Checked != 3 || cached.Skipped != 0 {
			t.Fatalf("cached registry summary = checked %d, skipped %d; want 3, 0", cached.Checked, cached.Skipped)
		}

		mock.mu.Lock()
		cachedCalls := mock.getVersionCalls
		mock.mu.Unlock()
		if cachedCalls != 0 {
			t.Fatalf("cached registry check made %d version calls, want 0", cachedCalls)
		}
	})
}

func TestStatsCommand(t *testing.T) {
	t.Run("shows statistics", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", `{"dependencies":{"a":"1.0.0"}}`, "Add a")
		addFileAndCommit(t, repoDir, "package.json", `{"dependencies":{"b":"1.0.0"}}`, "Add b")
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add more")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stats"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stats failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "Dependency Statistics") {
			t.Error("expected statistics header in output")
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stats", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stats failed: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		// Check for expected fields
		if _, ok := result["branch"]; !ok {
			t.Error("expected 'branch' field in JSON output")
		}
		if _, ok := result["current_deps"]; !ok {
			t.Error("expected 'current_deps' field in JSON output")
		}
		if _, ok := result["deps_by_ecosystem"]; !ok {
			t.Error("expected 'deps_by_ecosystem' field in JSON output")
		}
	})

	t.Run("by-author flag works", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stats", "--by-author"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stats --by-author failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "Test User") {
			t.Errorf("expected 'Test User' in by-author output, got: %s", output)
		}
	})
}

func TestSearchCommand(t *testing.T) {
	t.Run("finds packages by pattern", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"search", "express"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("search failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "express") {
			t.Error("expected to find 'express' in search results")
		}
	})

	t.Run("wildcard pattern works", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"search", "lod%"}) // SQL LIKE pattern
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("search failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "lodash") {
			t.Error("expected to find 'lodash' with wildcard pattern")
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"search", "express", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("search failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		if len(result) == 0 {
			t.Error("expected at least one result in JSON output")
		}

		// Verify express was found
		foundExpress := false
		for _, item := range result {
			if name, _ := item["name"].(string); name == "express" {
				foundExpress = true
				break
			}
		}
		if !foundExpress {
			t.Error("expected 'express' in search results")
		}
	})
}

func TestTreeCommand(t *testing.T) {
	t.Run("shows dependency tree", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"tree"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("tree failed: %v", err)
		}

		output := stdout.String()
		// Should show tree structure
		if !strings.Contains(output, "package.json") {
			t.Errorf("expected 'package.json' in tree output, got: %s", output)
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"tree", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("tree failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		if len(result) == 0 {
			t.Error("expected at least one manifest in tree JSON output")
		}

		// Validate tree structure
		first := result[0]
		if _, ok := first["name"]; !ok {
			t.Error("expected 'name' field in tree JSON")
		}
		if _, ok := first["type"]; !ok {
			t.Error("expected 'type' field in tree JSON")
		}
	})
}

func TestBlameCommand(t *testing.T) {
	t.Run("attributes dependencies to commits", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"blame"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("blame failed: %v", err)
		}

		output := stdout.String()
		// Should show commit attribution
		if !strings.Contains(output, "express") {
			t.Errorf("expected 'express' in blame output, got: %s", output)
		}
		// Should show the author who added the dependency
		if !strings.Contains(output, "Test User") {
			t.Errorf("expected 'Test User' in blame output, got: %s", output)
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"blame", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("blame failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		if len(result) == 0 {
			t.Error("expected at least one entry in blame JSON output")
		}

		// Validate structure
		first := result[0]
		if _, ok := first["name"]; !ok {
			t.Error("expected 'name' field in blame JSON")
		}
		if _, ok := first["author_name"]; !ok {
			t.Error("expected 'author_name' field in blame JSON")
		}
		if _, ok := first["sha"]; !ok {
			t.Error("expected 'sha' field in blame JSON")
		}
	})
}

func TestWhyCommand(t *testing.T) {
	t.Run("finds when package was added", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "README.md", "# Test", "Initial")
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"why", "express"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("why failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "express") {
			t.Error("expected to find 'express' in why output")
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"why", "express", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("why failed: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		// Should contain the package name
		if name, _ := result["name"].(string); name != "express" {
			t.Errorf("expected name 'express', got %q", name)
		}
		// Should have commit info
		if _, ok := result["sha"]; !ok {
			t.Error("expected 'sha' field in why JSON")
		}
		if _, ok := result["message"]; !ok {
			t.Error("expected 'message' field in why JSON")
		}
	})
}

func TestWhereCommand(t *testing.T) {
	t.Run("finds package in manifest", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"where", "express"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("where failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "package.json") {
			t.Error("expected to find package.json in where output")
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package.json", packageJSON, "Add deps")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"where", "express", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("where failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		if len(result) == 0 {
			t.Error("expected at least one location in where JSON output")
		}

		// Validate structure
		first := result[0]
		if _, ok := first["file_path"]; !ok {
			t.Error("expected 'file_path' field in where JSON")
		}
		if _, ok := first["ecosystem"]; !ok {
			t.Error("expected 'ecosystem' field in where JSON")
		}
	})
}

func TestWhereCommandWithWorkflows(t *testing.T) {
	t.Run("finds actions in github workflow files", func(t *testing.T) {
		repoDir := createTestRepo(t)

		workflow := `name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
`
		addFileAndCommit(t, repoDir, ".github/workflows/ci.yml", workflow, "Add workflow")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"where", "actions/checkout"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("where failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, ".github/workflows/ci.yml") {
			t.Errorf("expected to find .github/workflows/ci.yml in where output, got: %s", output)
		}
	})
}

func TestDiffFileCommand(t *testing.T) {
	t.Run("compares two package files", func(t *testing.T) {
		repoDir := createTestRepo(t)

		cleanup := chdir(t, repoDir)
		defer cleanup()

		// Create two files to compare
		writeFile(t, repoDir, "old.json", `{"dependencies":{"lodash":"^4.17.0"}}`)
		writeFile(t, repoDir, "new.json", `{"dependencies":{"lodash":"^4.17.21","express":"^4.18.0"}}`)

		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"diff-file", "old.json", "new.json", "--filename", "package.json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("diff-file failed: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "express") {
			t.Errorf("expected 'express' in diff output, got: %s", output)
		}
	})

	t.Run("compares workflow files with filename flag", func(t *testing.T) {
		repoDir := createTestRepo(t)

		cleanup := chdir(t, repoDir)
		defer cleanup()

		oldWorkflow := `name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
`
		newWorkflow := `name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
`
		writeFile(t, repoDir, "old.yml", oldWorkflow)
		writeFile(t, repoDir, "new.yml", newWorkflow)

		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"diff-file", "old.yml", "new.yml", "--filename", ".github/workflows/ci.yml"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("diff-file failed: %v", err)
		}

		output := stdout.String()
		// Should detect actions/setup-node as added
		if !strings.Contains(output, "actions/setup-node") {
			t.Errorf("expected 'actions/setup-node' in diff output, got: %s", output)
		}
	})
}

func TestDiffDriverCommand(t *testing.T) {
	t.Run("converts lockfile to sorted list", func(t *testing.T) {
		repoDir := createTestRepo(t)

		cleanup := chdir(t, repoDir)
		defer cleanup()

		writeFile(t, repoDir, "package-lock.json", packageLockJSON)

		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"diff-driver", "package-lock.json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("diff-driver failed: %v", err)
		}

		output := stdout.String()
		// Should show sorted dependency list
		if !strings.Contains(output, "express") {
			t.Errorf("expected 'express' in diff-driver output, got: %s", output)
		}
	})

	t.Run("converts workflow file when path indicates workflow", func(t *testing.T) {
		repoDir := createTestRepo(t)

		cleanup := chdir(t, repoDir)
		defer cleanup()

		workflow := `name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
`
		// Create the file in the workflow directory to test path-based identification
		writeFile(t, repoDir, ".github/workflows/ci.yml", workflow)

		var stdout bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"diff-driver", ".github/workflows/ci.yml"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("diff-driver failed: %v", err)
		}

		output := stdout.String()
		// Should show sorted action list (parsed format, not raw YAML)
		// If parsing works, output should be "actions/checkout v4\nactions/setup-node v4\n"
		// If parsing fails and falls back to raw, output would contain "name: CI", "jobs:", etc.
		if strings.Contains(output, "name: CI") || strings.Contains(output, "jobs:") {
			t.Errorf("diff-driver fell back to raw output instead of parsing workflow, got: %s", output)
		}
		if !strings.Contains(output, "actions/checkout") {
			t.Errorf("expected 'actions/checkout' in diff-driver output, got: %s", output)
		}
	})
}

func TestStaleCommand(t *testing.T) {
	t.Run("finds stale dependencies", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stale", "--days", "0"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stale failed: %v", err)
		}

		// With --days 0, all lockfile deps should be stale
		output := stdout.String()
		// Should show dependencies from the lockfile as stale
		if !strings.Contains(output, "express") && !strings.Contains(output, "lodash") {
			t.Errorf("expected stale dependencies in output, got: %s", output)
		}
	})

	t.Run("outputs json format", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "package-lock.json", packageLockJSON, "Add lockfile")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stale", "--days", "0", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stale failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v", err)
		}

		// With --days 0, should have stale packages
		if len(result) == 0 {
			t.Error("expected stale packages in JSON output with --days 0")
		}

		// Validate structure
		first := result[0]
		if _, ok := first["name"]; !ok {
			t.Error("expected 'name' field in stale JSON")
		}
	})

	t.Run("computes days since correctly for old commits", func(t *testing.T) {
		repoDir := createTestRepo(t)

		// Create lockfile with a backdated commit (90 days ago)
		fullPath := filepath.Join(repoDir, "package-lock.json")
		if err := os.WriteFile(fullPath, []byte(packageLockJSON), 0644); err != nil {
			t.Fatal(err)
		}
		gitCmd := exec.Command("git", "add", "package-lock.json")
		gitCmd.Dir = repoDir
		if err := gitCmd.Run(); err != nil {
			t.Fatal(err)
		}
		gitCmd = exec.Command("git", "commit", "-m", "Add lockfile")
		gitCmd.Dir = repoDir
		gitCmd.Env = append(os.Environ(),
			"GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
			"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z",
		)
		if err := gitCmd.Run(); err != nil {
			t.Fatal(err)
		}

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stale", "--days", "0", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stale failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}

		if len(result) == 0 {
			t.Fatal("expected stale packages")
		}

		// The commit is from 2020, so days_since should be well over 1000
		for _, entry := range result {
			days, ok := entry["days_since"].(float64)
			if !ok {
				t.Fatalf("days_since not a number: %v", entry["days_since"])
			}
			if days < 1000 {
				t.Errorf("expected days_since > 1000 for a 2020 commit, got %v for %v", days, entry["name"])
			}
		}
	})

	t.Run("includes go.mod dependencies", func(t *testing.T) {
		repoDir := createTestRepo(t)

		goMod, err := os.ReadFile("testdata/ades-go.mod")
		if err != nil {
			t.Fatalf("failed to read fixture: %v", err)
		}
		addFileAndCommit(t, repoDir, "go.mod", string(goMod), "Add go.mod")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"init"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}

		var stdout bytes.Buffer
		rootCmd = cmd.NewRootCmd()
		rootCmd.SetArgs([]string{"stale", "--days", "0", "--format", "json"})
		rootCmd.SetOut(&stdout)

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("stale failed: %v", err)
		}

		var result []map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}

		if len(result) == 0 {
			t.Fatal("expected Go dependencies in stale output, got none")
		}

		// Verify a known go.mod dependency appears
		found := false
		for _, entry := range result {
			if name, ok := entry["name"].(string); ok && name == "golang.org/x/mod" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected golang.org/x/mod in stale output")
		}
	})
}
