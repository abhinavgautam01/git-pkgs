package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
	"github.com/git-pkgs/sbom"
)

type sbomEnrichmentClient struct {
	packages        map[string]*enrichment.PackageInfo
	versions        map[string]*enrichment.VersionInfo
	getVersionCalls int
	bulkLookupCalls int
}

func (c *sbomEnrichmentClient) BulkLookup(
	_ context.Context,
	purls []string,
) (map[string]*enrichment.PackageInfo, error) {
	c.bulkLookupCalls++
	result := make(map[string]*enrichment.PackageInfo)
	for _, purlStr := range purls {
		if pkg := c.packages[purlStr]; pkg != nil {
			result[purlStr] = pkg
		}
	}
	return result, nil
}

func (c *sbomEnrichmentClient) GetVersions(_ context.Context, _ string) ([]enrichment.VersionInfo, error) {
	return nil, nil
}

func (c *sbomEnrichmentClient) GetVersion(_ context.Context, purlStr string) (*enrichment.VersionInfo, error) {
	c.getVersionCalls++
	return c.versions[purlStr], nil
}

func TestBuildSBOM(t *testing.T) {
	deps := []database.Dependency{
		{
			Ecosystem:    "npm",
			Name:         "lodash",
			Requirement:  "4.17.21",
			PURL:         "pkg:npm/lodash",
			ManifestKind: manifestKindLockfile,
		},
		{Ecosystem: "npm", Name: "react", Requirement: "18.2.0"},
	}
	licenses := map[string]string{"pkg:npm/lodash@4.17.21": "MIT"}

	doc := buildSBOM(deps, licenses, "demo", "1.0.0")

	if len(doc.Packages) != 2 {
		t.Fatalf("Packages = %d, want 2", len(doc.Packages))
	}
	if doc.Document.Component.Name != "demo" || doc.Document.Component.Version != "1.0.0" {
		t.Errorf("component = %+v", doc.Document.Component)
	}
	lodash := doc.Packages[0]
	if lodash.PURL() != "pkg:npm/lodash@4.17.21" {
		t.Errorf("lodash purl = %q", lodash.PURL())
	}
	if lodash.LicenseDeclared != licenses["pkg:npm/lodash@4.17.21"] {
		t.Errorf("lodash license = %q", lodash.LicenseDeclared)
	}
	react := doc.Packages[1]
	if react.PURL() == "" {
		t.Errorf("react purl should be synthesised from ecosystem/name/version")
	}

	// Round-trip through the encoder so output remains parseable.
	for _, f := range []sbom.Format{sbom.FormatCycloneDXJSON, sbom.FormatSPDXJSON} {
		var buf bytes.Buffer
		if err := sbom.Encode(&buf, doc, f); err != nil {
			t.Fatalf("Encode(%d): %v", f, err)
		}
		if _, err := sbom.Parse(buf.Bytes()); err != nil {
			t.Fatalf("Parse(%d): %v\n%s", f, err, buf.String())
		}
		if !strings.Contains(buf.String(), "pkg:npm/lodash@4.17.21") {
			t.Errorf("encoded output missing purl:\n%s", buf.String())
		}
	}
}

func TestSelectSBOMDependenciesPrefersResolvedVersions(t *testing.T) {
	direct := database.Dependency{
		Ecosystem:    "npm",
		Name:         "ua-parser-js",
		Requirement:  "^1.0.41",
		PURL:         "pkg:npm/ua-parser-js",
		ManifestPath: "package.json",
		ManifestKind: "manifest",
	}
	resolved := database.Dependency{
		Ecosystem:    "npm",
		Name:         "ua-parser-js",
		Requirement:  "1.0.41",
		PURL:         "pkg:npm/ua-parser-js",
		ManifestPath: "package-lock.json",
		ManifestKind: manifestKindLockfile,
	}
	nested := resolved
	nested.Requirement = "2.0.10"

	selected := selectSBOMDependencies([]database.Dependency{direct, resolved, nested, resolved})
	if len(selected) != 2 {
		t.Fatalf("selected dependencies = %d, want 2", len(selected))
	}
	if got := sbomPURLForDependency(selected[0]); got != "pkg:npm/ua-parser-js@1.0.41" {
		t.Fatalf("first PURL = %q, want version 1.0.41", got)
	}
	if got := sbomPURLForDependency(selected[1]); got != "pkg:npm/ua-parser-js@2.0.10" {
		t.Fatalf("second PURL = %q, want version 2.0.10", got)
	}
}

func TestSelectSBOMDependenciesKeepsDependenciesWithoutPURL(t *testing.T) {
	dep := database.Dependency{Name: "local-tool", Requirement: "1.0.0"}
	otherVersion := dep
	otherVersion.Requirement = "2.0.0"

	selected := selectSBOMDependencies([]database.Dependency{dep, dep, otherVersion})
	if len(selected) != 2 {
		t.Fatalf("selected dependencies = %d, want 2", len(selected))
	}
	for _, selectedDep := range selected {
		if got := sbomPURLForDependency(selectedDep); got != "" {
			t.Fatalf("PURL = %q, want empty", got)
		}
	}
}

func TestEnrichLicensesUsesVersionMetadata(t *testing.T) {
	for _, tt := range []struct {
		name           string
		versionLicense string
		wantLicense    string
		wantFallbacks  int
	}{
		{
			name:           "version license",
			versionLicense: "MIT",
			wantLicense:    "MIT",
		},
		{
			name:          "package fallback",
			wantLicense:   "AGPL-3.0-or-later",
			wantFallbacks: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &sbomEnrichmentClient{
				packages: map[string]*enrichment.PackageInfo{
					"pkg:npm/ua-parser-js": {License: "AGPL-3.0-or-later"},
				},
				versions: map[string]*enrichment.VersionInfo{
					"pkg:npm/ua-parser-js@1.0.41": {Number: "1.0.41", License: tt.versionLicense},
				},
			}
			original := NewEnrichmentClient
			NewEnrichmentClient = func(...enrichment.Option) (enrichment.Client, error) {
				return client, nil
			}
			defer func() { NewEnrichmentClient = original }()

			deps := []database.Dependency{
				{
					Ecosystem:    "npm",
					Name:         "ua-parser-js",
					Requirement:  "1.0.41",
					PURL:         "pkg:npm/ua-parser-js",
					ManifestKind: manifestKindLockfile,
				},
			}
			licenses, fallbacks, err := enrichLicenses(nil, deps)
			if err != nil {
				t.Fatalf("enrich licenses: %v", err)
			}
			if got := licenses["pkg:npm/ua-parser-js@1.0.41"]; got != tt.wantLicense {
				t.Fatalf("license = %q, want %q", got, tt.wantLicense)
			}
			if fallbacks != tt.wantFallbacks {
				t.Fatalf("package fallbacks = %d, want %d", fallbacks, tt.wantFallbacks)
			}
			if client.bulkLookupCalls != 1 || client.getVersionCalls != 1 {
				t.Fatalf("enrichment calls: BulkLookup=%d GetVersion=%d, want 1 each",
					client.bulkLookupCalls, client.getVersionCalls)
			}
		})
	}
}

func TestSBOMFormat(t *testing.T) {
	tests := []struct {
		typ, fmt string
		want     sbom.Format
		wantErr  bool
	}{
		{"cyclonedx", "json", sbom.FormatCycloneDXJSON, false},
		{"cyclonedx", "xml", sbom.FormatCycloneDXXML, false},
		{"spdx", "json", sbom.FormatSPDXJSON, false},
		{"spdx", "xml", 0, true},
		{"", "", sbom.FormatCycloneDXJSON, false},
	}
	for _, tt := range tests {
		got, err := sbomFormat(tt.typ, tt.fmt)
		if (err != nil) != tt.wantErr {
			t.Errorf("sbomFormat(%s,%s) err = %v", tt.typ, tt.fmt, err)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("sbomFormat(%s,%s) = %d, want %d", tt.typ, tt.fmt, got, tt.want)
		}
	}
}
