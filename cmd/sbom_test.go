package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/git-pkgs/internal/database"
	gitpkg "github.com/git-pkgs/git-pkgs/internal/git"
	"github.com/git-pkgs/sbom"
	"github.com/git-pkgs/spdx"
	gitgo "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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

func TestProjectLicenseAtRevision(t *testing.T) {
	repoDir := t.TempDir()
	repository, err := gitgo.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	first := commitSBOMFile(t, repository, repoDir, "package.json", `{
  "name": "demo",
  "version": "1.0.0",
  "license": "MIT"
}`, "add package manifest")
	commitSBOMFile(t, repository, repoDir, "package.json", `{
  "name": "demo",
  "version": "1.1.0",
  "license": "Apache-2.0"
}`, "change project license")
	commitSBOMFile(t, repository, repoDir, "packages/nested/package.json", `{
  "name": "nested",
  "version": "1.0.0",
  "license": "GPL-3.0-only"
}`, "add nested package")

	repo, err := gitpkg.OpenRepository(repoDir)
	if err != nil {
		t.Fatalf("OpenRepository: %v", err)
	}

	got, err := projectLicensesAtRevision(repo, first.String())
	if err != nil {
		t.Fatalf("projectLicensesAtRevision(first): %v", err)
	}
	if got.Expression != "MIT" || len(got.Names) != 0 {
		t.Fatalf("first revision licenses = %+v, want MIT", got)
	}

	got, err = projectLicensesAtRevision(repo, "HEAD")
	if err != nil {
		t.Fatalf("projectLicensesAtRevision(HEAD): %v", err)
	}
	if got.Expression != "Apache-2.0" || len(got.Names) != 0 {
		t.Fatalf("HEAD licenses = %+v, want Apache-2.0", got)
	}
}

func TestNormalizeProjectLicensesSeparatesNonSPDXNames(t *testing.T) {
	licenses := normalizeProjectLicenses([]string{
		"BSD-3-Clause",
		"License :: OSI Approved :: BSD License",
		"Acme Internal Terms",
	})

	if licenses.Expression == "" || !spdx.Valid(licenses.Expression) {
		t.Fatalf("normalized expression = %q, want valid SPDX", licenses.Expression)
	}
	if strings.Contains(licenses.Expression, "License ::") {
		t.Fatalf("normalized expression contains raw classifier: %q", licenses.Expression)
	}
	if len(licenses.Names) != 1 || licenses.Names[0] != "Acme Internal Terms" {
		t.Fatalf("non-SPDX names = %q, want [Acme Internal Terms]", licenses.Names)
	}
	if expression := licenses.spdxExpression(); !spdx.Valid(expression) {
		t.Fatalf("SPDX expression with LicenseRef = %q, want valid SPDX", expression)
	}
}

func TestProjectLicenseFileAtRevision(t *testing.T) {
	repoDir := t.TempDir()
	repository, err := gitgo.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	commitSBOMFile(t, repository, repoDir, "LICENSE.custom", "original license terms\n", "add license file")
	manifestRevision := commitSBOMFile(t, repository, repoDir, "Cargo.toml", `[package]
name = "demo"
version = "1.0.0"
license-file = "LICENSE.custom"
`, "add cargo manifest")
	commitSBOMFile(t, repository, repoDir, "LICENSE.custom", "updated license terms\n", "update license file")

	repo, err := gitpkg.OpenRepository(repoDir)
	if err != nil {
		t.Fatalf("OpenRepository: %v", err)
	}

	licenses, err := projectLicensesAtRevision(repo, manifestRevision.String())
	if err != nil {
		t.Fatalf("projectLicensesAtRevision: %v", err)
	}
	if licenses.Expression != "" || len(licenses.Names) != 0 || len(licenses.Files) != 1 {
		t.Fatalf("project licenses = %+v, want one declared file", licenses)
	}
	if file := licenses.Files[0]; file.Path != "LICENSE.custom" || file.Text != "original license terms\n" {
		t.Fatalf("project license file = %+v, want original revision content", file)
	}
}

func testRootLicenses() (*sbom.SBOM, projectLicenses) {
	document := buildSBOM(nil, nil, "demo", "1.0.0")
	licenses := projectLicenses{
		Expression: "MIT OR Apache-2.0",
		Names:      []string{"Acme Internal Terms"},
		Files: []projectLicenseFile{{
			Path: "LICENSE.custom",
			Text: "Custom file terms\n",
		}},
	}
	return document, licenses
}

func TestEncodeSBOMWithRootLicensesCycloneDXJSON(t *testing.T) {
	document, licenses := testRootLicenses()
	var output bytes.Buffer
	if err := encodeSBOMWithRootLicenses(&output, document, sbom.FormatCycloneDXJSON, licenses); err != nil {
		t.Fatalf("encodeSBOMWithRootLicenses: %v", err)
	}
	var result struct {
		Metadata struct {
			Component struct {
				Licenses []struct {
					Expression string `json:"expression"`
					License    *struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"license"`
				} `json:"licenses"`
			} `json:"component"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, output.String())
	}
	got := result.Metadata.Component.Licenses
	if len(got) != 3 {
		t.Fatalf("root licenses = %+v, want three license entries", got)
	}
	wantNames := []string{licenses.Expression, licenses.Names[0], licenses.Files[0].Path}
	for i, choice := range got {
		if choice.Expression != "" || choice.License == nil || choice.License.Name != wantNames[i] {
			t.Fatalf("root license choice %d = %+v, want named license %q", i, choice, wantNames[i])
		}
	}
}

func TestEncodeSBOMWithRootLicensesCycloneDXXML(t *testing.T) {
	document, licenses := testRootLicenses()
	var output bytes.Buffer
	if err := encodeSBOMWithRootLicenses(&output, document, sbom.FormatCycloneDXXML, licenses); err != nil {
		t.Fatalf("encodeSBOMWithRootLicenses: %v", err)
	}
	var result struct {
		Metadata struct {
			Component struct {
				LicenseExpression string   `xml:"licenses>expression"`
				LicenseNames      []string `xml:"licenses>license>name"`
			} `xml:"component"`
		} `xml:"metadata"`
	}
	if err := xml.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, output.String())
	}
	component := result.Metadata.Component
	wantNames := []string{licenses.Expression, licenses.Names[0], licenses.Files[0].Path}
	if component.LicenseExpression != "" || !slices.Equal(component.LicenseNames, wantNames) {
		t.Fatalf("root licenses = %+v, want named licenses %q", component, wantNames)
	}
}

func TestEncodeSBOMWithRootLicensesSPDXJSON(t *testing.T) {
	document, licenses := testRootLicenses()
	var output bytes.Buffer
	if err := encodeSBOMWithRootLicenses(&output, document, sbom.FormatSPDXJSON, licenses); err != nil {
		t.Fatalf("encodeSBOMWithRootLicenses: %v", err)
	}
	var result struct {
		Packages []struct {
			SPDXID          string `json:"SPDXID"`
			LicenseDeclared string `json:"licenseDeclared"`
		} `json:"packages"`
		ExtractedLicensingInfos []struct {
			LicenseID     string `json:"licenseId"`
			Name          string `json:"name"`
			ExtractedText string `json:"extractedText"`
		} `json:"hasExtractedLicensingInfos"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, output.String())
	}
	rootLicense := findRootPackageLicense(result.Packages)
	if !spdx.Valid(rootLicense) ||
		!strings.Contains(rootLicense, licenseRefForName(licenses.Names[0])) ||
		!strings.Contains(rootLicense, licenseRefForFile(licenses.Files[0])) {
		t.Fatalf("root license = %q, want valid SPDX expression with both LicenseRefs", rootLicense)
	}
	if len(result.ExtractedLicensingInfos) != 2 {
		t.Fatalf("extracted licensing infos = %+v, want two", result.ExtractedLicensingInfos)
	}
	fileInfo := result.ExtractedLicensingInfos[1]
	if fileInfo.LicenseID != licenseRefForFile(licenses.Files[0]) ||
		fileInfo.Name != licenses.Files[0].Path || fileInfo.ExtractedText != licenses.Files[0].Text {
		t.Fatalf("file extracted licensing info = %+v", fileInfo)
	}
}

func findRootPackageLicense(packages []struct {
	SPDXID          string `json:"SPDXID"`
	LicenseDeclared string `json:"licenseDeclared"`
}) string {
	for _, pkg := range packages {
		if pkg.SPDXID == spdxRootPackageID {
			return pkg.LicenseDeclared
		}
	}
	return ""
}

func commitSBOMFile(
	t *testing.T,
	repository *gitgo.Repository,
	repoDir, name, content, message string,
) plumbing.Hash {
	t.Helper()
	fullPath := filepath.Join(repoDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("Add: %v", err)
	}
	when := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	hash, err := worktree.Commit(message, &gitgo.CommitOptions{
		Author: &object.Signature{Name: "Test User", Email: "test@example.com", When: when},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return hash
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
