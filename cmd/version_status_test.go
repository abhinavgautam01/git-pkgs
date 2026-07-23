package cmd

import (
	"testing"
	"time"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

func TestEnrichmentVersionStatus(t *testing.T) {
	if got := enrichmentVersionStatus(enrichment.VersionInfo{Status: versionStatusRetracted, Yanked: true}); got != versionStatusRetracted {
		t.Fatalf("full status = %q, want retracted", got)
	}
	if got := enrichmentVersionStatus(enrichment.VersionInfo{Yanked: true}); got != versionStatusYanked {
		t.Fatalf("legacy yanked status = %q, want yanked", got)
	}
}

func TestHasCachedVersionStatuses(t *testing.T) {
	checkedAt := time.Now()
	if !hasCachedVersionStatuses([]database.CachedVersion{{StatusCheckedAt: checkedAt}, {StatusCheckedAt: checkedAt}}) {
		t.Fatal("expected complete version status cache")
	}
	if hasCachedVersionStatuses([]database.CachedVersion{{StatusCheckedAt: checkedAt}, {}}) {
		t.Fatal("expected incomplete version status cache")
	}
}

func TestLatestAvailableVersionAtExcludesWithdrawnVersions(t *testing.T) {
	atTime := time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC)
	versions := []database.CachedVersion{
		{PURL: "pkg:cargo/example@1.0.0", PublishedAt: time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{PURL: "pkg:cargo/example@1.1.0", PublishedAt: time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC), Status: versionStatusRetracted},
		{PURL: "pkg:cargo/example@1.2.0", PublishedAt: time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC), Status: versionStatusYanked},
	}

	if got := latestAvailableVersionAt(versions, atTime); got != "1.0.0" {
		t.Fatalf("latest available version = %q, want 1.0.0", got)
	}
}
