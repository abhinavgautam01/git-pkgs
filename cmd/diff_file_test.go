package cmd

import (
	"slices"
	"testing"
)

func TestSortedUniqueLicenses(t *testing.T) {
	got := sortedUniqueLicenses([]string{"MIT", "Apache-2.0", "MIT", ""})
	want := []string{"Apache-2.0", "MIT"}
	if !slices.Equal(got, want) {
		t.Fatalf("sortedUniqueLicenses() = %q, want %q", got, want)
	}
}
