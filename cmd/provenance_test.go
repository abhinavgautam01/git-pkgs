package cmd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
)

type fakeProvenanceHTTPClient struct {
	responses map[string]string
	requests  []string
}

func (f *fakeProvenanceHTTPClient) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req.URL.String())
	body, ok := f.responses[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader(req.URL.String())),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestLookupNPMProvenanceTrustedPublishing(t *testing.T) {
	dep := database.Dependency{
		Name:        "@scope/pkg",
		Ecosystem:   "npm",
		Requirement: "1.2.3",
	}
	client := &fakeProvenanceHTTPClient{
		responses: map[string]string{
			"https://registry.npmjs.org/@scope%2Fpkg/1.2.3": `{
				"dist": {
					"attestations": {"provenance": {"url": "https://registry.npmjs.org/-/npm/v1/attestations/%40scope/pkg@1.2.3"}},
					"signatures": [{"keyid": "SHA256:test", "sig": "abc"}]
				}
			}`,
		},
	}

	got := lookupNPMProvenance(context.Background(), client, dep)
	if got.Status != provenanceStatusTrustedPublishing {
		t.Fatalf("status = %q, want %q; error = %q; requests = %#v",
			got.Status, provenanceStatusTrustedPublishing, got.Error, client.requests)
	}
	if !got.TrustedPublishing {
		t.Fatal("trusted publishing = false, want true")
	}
	if got.RegistrySignatures != 1 {
		t.Fatalf("registry signatures = %d, want 1", got.RegistrySignatures)
	}
	if len(got.Evidence) == 0 {
		t.Fatal("expected provenance evidence")
	}
}

func TestLookupNPMProvenanceSignedOnly(t *testing.T) {
	dep := database.Dependency{
		Name:        "lodash",
		Ecosystem:   "npm",
		Requirement: "4.17.21",
	}
	client := &fakeProvenanceHTTPClient{
		responses: map[string]string{
			"https://registry.npmjs.org/lodash/4.17.21": `{
				"dist": {
					"signatures": [{"keyid": "SHA256:test", "sig": "abc"}]
				}
			}`,
		},
	}

	got := lookupNPMProvenance(context.Background(), client, dep)
	if got.Status != provenanceStatusSigned {
		t.Fatalf("status = %q, want %q", got.Status, provenanceStatusSigned)
	}
	if got.TrustedPublishing {
		t.Fatal("trusted publishing = true, want false")
	}
	if got.RegistrySignatures != 1 {
		t.Fatalf("registry signatures = %d, want 1", got.RegistrySignatures)
	}
}

func TestSelectProvenanceDependencies(t *testing.T) {
	deps := []database.Dependency{
		{
			Name:         "lodash",
			Ecosystem:    "npm",
			Requirement:  "^4.17.21",
			ManifestKind: "manifest",
		},
		{
			Name:         "lodash",
			Ecosystem:    "npm",
			Requirement:  "4.17.21",
			ManifestKind: manifestKindLockfile,
		},
		{
			Name:         "golang.org/x/mod",
			Ecosystem:    "golang",
			Requirement:  "v0.32.0",
			ManifestKind: "manifest",
		},
	}

	resolved, unresolved := selectProvenanceDependencies(deps)
	if len(resolved) != 2 {
		t.Fatalf("resolved length = %d, want 2", len(resolved))
	}
	if unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", unresolved)
	}
}

func TestBuildProvenanceResultMissingFilter(t *testing.T) {
	deps := []database.Dependency{
		{
			Name:         "trusted",
			Ecosystem:    "npm",
			Requirement:  "1.0.0",
			ManifestPath: "package-lock.json",
			ManifestKind: manifestKindLockfile,
		},
		{
			Name:         "signed",
			Ecosystem:    "npm",
			Requirement:  "1.0.0",
			ManifestPath: "package-lock.json",
			ManifestKind: manifestKindLockfile,
		},
		{
			Name:         "serde",
			Ecosystem:    "cargo",
			Requirement:  "1.0.0",
			ManifestPath: "Cargo.lock",
			ManifestKind: manifestKindLockfile,
		},
	}
	lookupData := map[string]provenanceLookupData{
		"pkg:npm/trusted@1.0.0": {
			Status:            provenanceStatusTrustedPublishing,
			TrustedPublishing: true,
			Evidence:          []string{"attestations"},
		},
		"pkg:npm/signed@1.0.0": {
			Status:             provenanceStatusSigned,
			RegistrySignatures: 1,
		},
		"pkg:cargo/serde@1.0.0": {
			Status:   provenanceStatusUnsupported,
			Evidence: []string{"provenance lookup is only supported for npm, pypi, and rubygems"},
		},
	}

	result := buildProvenanceResult(deps, 1, lookupData, true)
	if result.Summary.TotalDependencies != 3 {
		t.Fatalf("total dependencies = %d, want 3", result.Summary.TotalDependencies)
	}
	if result.Summary.TrustedPublishing != 1 {
		t.Fatalf("trusted publishing = %d, want 1", result.Summary.TrustedPublishing)
	}
	if result.Summary.RegistrySignatures != 1 {
		t.Fatalf("registry signatures = %d, want 1", result.Summary.RegistrySignatures)
	}
	if result.Summary.WithoutProvenance != 1 {
		t.Fatalf("without provenance = %d, want 1", result.Summary.WithoutProvenance)
	}
	if result.Summary.UnsupportedEcosystems != 1 {
		t.Fatalf("unsupported ecosystems = %d, want 1", result.Summary.UnsupportedEcosystems)
	}
	if result.Summary.UnresolvedDependencies != 1 {
		t.Fatalf("unresolved dependencies = %d, want 1", result.Summary.UnresolvedDependencies)
	}
	if len(result.Dependencies) != 2 {
		t.Fatalf("dependencies length = %d, want 2", len(result.Dependencies))
	}
	for _, dep := range result.Dependencies {
		if dep.TrustedPublishing {
			t.Fatalf("trusted dependency should be filtered from --missing result: %#v", dep)
		}
		if dep.Status == string(provenanceStatusUnsupported) && dep.Error != "" {
			t.Fatalf("unsupported dependency should not be reported as error: %#v", dep)
		}
	}
}

func TestLookupProvenanceUnsupportedUsesEvidence(t *testing.T) {
	got := lookupProvenance(context.Background(), &fakeProvenanceHTTPClient{}, database.Dependency{
		Name:        "serde",
		Ecosystem:   "cargo",
		Requirement: "1.0.0",
	})
	if got.Status != provenanceStatusUnsupported {
		t.Fatalf("status = %q, want %q", got.Status, provenanceStatusUnsupported)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want empty", got.Error)
	}
	if len(got.Evidence) == 0 {
		t.Fatal("expected explanatory evidence for unsupported ecosystem")
	}
}
