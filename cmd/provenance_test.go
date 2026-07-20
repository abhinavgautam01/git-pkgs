package cmd

import (
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
)

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
			Name:         "attested",
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
		"pkg:npm/attested@1.0.0": {
			Status:             provenanceStatusAttested,
			RegistrySignatures: 1,
			Evidence:           []string{"npm registry attestation; publishing authentication is not verifiable"},
		},
		"pkg:cargo/serde@1.0.0": {
			Status:   provenanceStatusUnsupported,
			Evidence: []string{"provenance lookup is only supported for npm, pypi, and rubygems"},
		},
	}

	result := buildProvenanceResult(deps, 1, lookupData, true)
	if result.Summary.TotalDependencies != 4 {
		t.Fatalf("total dependencies = %d, want 4", result.Summary.TotalDependencies)
	}
	if result.Summary.TrustedPublishing != 1 {
		t.Fatalf("trusted publishing = %d, want 1", result.Summary.TrustedPublishing)
	}
	if result.Summary.AttestedDependencies != 1 {
		t.Fatalf("attested dependencies = %d, want 1", result.Summary.AttestedDependencies)
	}
	if result.Summary.RegistrySignatures != 2 {
		t.Fatalf("registry signatures = %d, want 2", result.Summary.RegistrySignatures)
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
		if dep.Name == "attested" {
			t.Fatalf("attested dependency should be filtered from --missing result: %#v", dep)
		}
	}
}
