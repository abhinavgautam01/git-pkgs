package cmd

import (
	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
)

const (
	versionStatusYanked    = "yanked"
	versionStatusRetracted = "retracted"
)

func isWithdrawnVersionStatus(status string) bool {
	return status == versionStatusYanked || status == versionStatusRetracted
}

func enrichmentVersionStatus(version enrichment.VersionInfo) string {
	if version.Status != "" {
		return version.Status
	}
	if version.Yanked {
		return versionStatusYanked
	}
	return ""
}

func hasCachedVersionStatuses(versions []database.CachedVersion) bool {
	if len(versions) == 0 {
		return false
	}
	for _, version := range versions {
		if version.StatusCheckedAt.IsZero() {
			return false
		}
	}
	return true
}
