package provenance

import (
	"context"
	"fmt"
	"net/url"
)

const pypiIntegrityAccept = "application/vnd.pypi.integrity.v1+json"

type pypiRelease struct {
	URLs []struct {
		Filename string `json:"filename"`
	} `json:"urls"`
}

type pypiProvenance struct {
	AttestationBundles []any `json:"attestation_bundles"`
}

func (c *Client) lookupPyPI(ctx context.Context, dep Dependency) Result {
	// The release JSON is used only to enumerate the release artifacts. Attestation
	// data itself is retrieved from PyPI's per-file Integrity API.
	releaseEndpoint := "https://pypi.org/pypi/" + url.PathEscape(dep.Name) + "/" + url.PathEscape(dep.Version) + "/json"
	var release pypiRelease
	if err := c.fetchJSON(ctx, releaseEndpoint, "application/json", &release); err != nil {
		return Result{Status: StatusError, Error: err.Error()}
	}
	if len(release.URLs) == 0 {
		return Result{Status: StatusError, Error: "PyPI release has no files"}
	}

	attested := 0
	lookupFailures := 0
	for _, file := range release.URLs {
		if file.Filename == "" {
			lookupFailures++
			continue
		}
		endpoint := "https://pypi.org/integrity/" + url.PathEscape(dep.Name) + "/" + url.PathEscape(dep.Version) + "/" + url.PathEscape(file.Filename) + "/provenance"
		var provenance pypiProvenance
		err := c.fetchJSON(ctx, endpoint, pypiIntegrityAccept, &provenance)
		switch {
		case err == nil && len(provenance.AttestationBundles) > 0:
			attested++
		case err == nil || isNotFound(err):
			// A missing provenance resource means this release file is unattested.
		default:
			lookupFailures++
		}
	}

	if lookupFailures > 0 {
		return Result{
			Status:   StatusError,
			Evidence: []string{fmt.Sprintf("PyPI attestations found for %d/%d release files", attested, len(release.URLs))},
			Error:    fmt.Sprintf("could not check provenance for %d release files", lookupFailures),
		}
	}
	if attested == len(release.URLs) {
		return Result{
			Status:            StatusTrustedPublishing,
			TrustedPublishing: true,
			Evidence:          []string{fmt.Sprintf("PyPI attestations for all %d release files", attested)},
		}
	}
	return Result{
		Status:   StatusMissing,
		Evidence: []string{fmt.Sprintf("PyPI attestations found for %d/%d release files", attested, len(release.URLs))},
	}
}
