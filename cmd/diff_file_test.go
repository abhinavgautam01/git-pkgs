package cmd

import (
	"slices"
	"testing"
)

func TestSortedUniqueLicenses(t *testing.T) {
	got := sortedUniqueLicenses([]string{
		" MIT ",
		"MIT License",
		"Apache 2.0",
		"Apache-2.0",
		"Custom License",
		" Custom License ",
		"",
		"   ",
	})
	want := []string{"Apache-2.0", "Custom License", "MIT"}
	if !slices.Equal(got, want) {
		t.Fatalf("sortedUniqueLicenses() = %q, want %q", got, want)
	}
}
