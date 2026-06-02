package cmd

import (
	"testing"
)

func TestPagerFlagAccepted(t *testing.T) {
	commands := []string{
		"blame",
		"show",
		"tree",
		"search",
		"diff",
		"where",
		"why",
		"stats",
		"stale",
		"freshness",
		"deprecated",
		"funding",
		"log",
		"list",
		"history",
	}

	for _, name := range commands {
		t.Run(name, func(t *testing.T) {
			root := NewRootCmd()
			root.SetArgs([]string{name, "--pager", "--help"})
			err := root.Execute()
			if err != nil {
				t.Fatalf("%s --pager --help failed: %v", name, err)
			}
		})
	}
}

func TestUsePagerSetByFlag(t *testing.T) {
	// Reset global state
	UsePager = false

	root := NewRootCmd()
	// list without a repo will fail in RunE, but PersistentPreRun still runs
	root.SetArgs([]string{"--pager", "list"})
	_ = root.Execute()

	if !UsePager {
		t.Error("expected UsePager to be true after --pager flag")
	}

	// Clean up
	UsePager = false
}

func TestCleanupOutputSafe(t *testing.T) {
	// CleanupOutput should be safe to call when no pager is active
	pagerCleanup = nil
	CleanupOutput()

	// And when a cleanup function is set, it should be called and nilled
	called := false
	pagerCleanup = func() { called = true }
	CleanupOutput()

	if !called {
		t.Error("expected pagerCleanup to be called")
	}
	if pagerCleanup != nil {
		t.Error("expected pagerCleanup to be nil after CleanupOutput")
	}
}
