package cmd_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestReindex(t *testing.T) {
	t.Run("reindexes a tracked branch", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "go.mod", "module example.com/test\n\ngo 1.21\n", "Initial commit")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		_, _, err := runCmd(t, "init")
		if err != nil {
			t.Fatalf("init failed: %v", err)
		}

		// Add a new commit after init
		addFileAndCommit(t, repoDir, "go.mod", "module example.com/test\n\ngo 1.22\n", "Bump go version")

		stdout, _, err := runCmd(t, "reindex")
		if err != nil {
			t.Fatalf("reindex failed: %v", err)
		}

		if !strings.Contains(stdout, "Analyzed") && !strings.Contains(stdout, "Already up to date") {
			t.Errorf("unexpected output: %s", stdout)
		}
	})

	t.Run("auto-adds untracked branch with -b flag", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "go.mod", "module example.com/test\n\ngo 1.21\n", "Initial commit")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		_, _, err := runCmd(t, "init")
		if err != nil {
			t.Fatalf("init failed: %v", err)
		}

		// Create an orphan branch so it has no shared commits with main
		gitCmd := exec.Command("git", "checkout", "--orphan", "feature")
		gitCmd.Dir = repoDir
		if err := gitCmd.Run(); err != nil {
			t.Fatalf("failed to create branch: %v", err)
		}
		// Remove staged files from main
		gitCmd = exec.Command("git", "rm", "-rf", ".")
		gitCmd.Dir = repoDir
		if err := gitCmd.Run(); err != nil {
			t.Fatalf("failed to clean staged files: %v", err)
		}
		addFileAndCommit(t, repoDir, "new.txt", "hello", "Add file on feature branch")

		stdout, _, err := runCmd(t, "reindex", "-b", "feature")
		if err != nil {
			t.Fatalf("reindex with -b failed: %v", err)
		}

		if !strings.Contains(stdout, "Added branch") {
			t.Errorf("expected output to indicate branch was added, got: %s", stdout)
		}

		// Verify branch is now tracked
		stdout, _, err = runCmd(t, "branch", "list")
		if err != nil {
			t.Fatalf("branch list failed: %v", err)
		}
		if !strings.Contains(stdout, "feature") {
			t.Errorf("expected feature branch to be tracked, got: %s", stdout)
		}
	})

	t.Run("fails without init", func(t *testing.T) {
		repoDir := createTestRepo(t)
		addFileAndCommit(t, repoDir, "README.md", "# Test", "Initial commit")

		cleanup := chdir(t, repoDir)
		defer cleanup()

		_, _, err := runCmd(t, "reindex")
		if err == nil {
			t.Fatal("expected error without init")
		}

		if !strings.Contains(err.Error(), "init") {
			t.Errorf("expected error to mention init, got: %v", err)
		}
	})
}
