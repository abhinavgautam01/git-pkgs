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

func TestFormatDeclaredLicensesSanitizesControlCharacters(t *testing.T) {
	got := formatDeclaredLicenses([]string{"MIT\x1b[31m", "Custom\x00License"})
	want := "MIT[31m, CustomLicense"
	if got != want {
		t.Fatalf("formatDeclaredLicenses() = %q, want %q", got, want)
	}
}
