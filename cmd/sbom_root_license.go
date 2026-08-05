package cmd

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/git-pkgs/git-pkgs/internal/git"
	"github.com/git-pkgs/manifests"
	"github.com/git-pkgs/sbom"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const spdxRootPackageID = "SPDXRef-Package-root"

func projectLicenseAtRevision(repo *git.Repository, revision string) (string, error) {
	if revision == "" {
		revision = "HEAD"
	}

	hash, err := repo.ResolveRevision(revision)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", revision, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return "", fmt.Errorf("getting commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", fmt.Errorf("getting commit tree: %w", err)
	}

	var licenses []string
	seen := make(map[string]bool)
	err = tree.Files().ForEach(func(file *object.File) error {
		if path.Dir(file.Name) != "." {
			return nil
		}
		_, kind, ok := manifests.Identify(file.Name)
		if !ok || kind != manifests.Manifest {
			return nil
		}

		content, err := file.Contents()
		if err != nil {
			return fmt.Errorf("reading %s: %w", file.Name, err)
		}
		result, err := manifests.Parse(file.Name, []byte(content))
		if err != nil {
			return nil
		}
		for _, declared := range result.Licenses {
			declared = strings.TrimSpace(declared)
			if declared == "" || seen[declared] {
				continue
			}
			seen[declared] = true
			licenses = append(licenses, declared)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(licenses)
	return strings.Join(licenses, " OR "), nil
}

func encodeSBOMWithRootLicense(w io.Writer, document *sbom.SBOM, format sbom.Format, license string) error {
	if license == "" {
		return sbom.Encode(w, document, format)
	}

	var encoded bytes.Buffer
	if err := sbom.Encode(&encoded, document, format); err != nil {
		return err
	}

	switch format {
	case sbom.FormatCycloneDXJSON, sbom.FormatSPDXJSON:
		data, err := addRootLicenseJSON(encoded.Bytes(), format, license)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	case sbom.FormatCycloneDXXML:
		return addCycloneDXRootLicenseXML(w, encoded.Bytes(), license)
	default:
		return fmt.Errorf("unsupported SBOM format %d", format)
	}
}

func addRootLicenseJSON(data []byte, format sbom.Format, license string) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decoding generated SBOM: %w", err)
	}

	switch format {
	case sbom.FormatCycloneDXJSON:
		metadata, ok := document["metadata"].(map[string]any)
		if !ok {
			return nil, errors.New("generated CycloneDX SBOM has no metadata")
		}
		component, ok := metadata["component"].(map[string]any)
		if !ok {
			return nil, errors.New("generated CycloneDX SBOM has no root component")
		}
		component["licenses"] = []any{map[string]any{"expression": license}}
	case sbom.FormatSPDXJSON:
		packages, ok := document["packages"].([]any)
		if !ok {
			return nil, errors.New("generated SPDX SBOM has no packages")
		}
		found := false
		for _, value := range packages {
			pkg, ok := value.(map[string]any)
			if ok && pkg["SPDXID"] == spdxRootPackageID {
				pkg["licenseDeclared"] = license
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("generated SPDX SBOM has no root package")
		}
	default:
		return nil, fmt.Errorf("unsupported JSON SBOM format %d", format)
	}

	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding generated SBOM: %w", err)
	}
	return append(encoded, '\n'), nil
}

func addCycloneDXRootLicenseXML(w io.Writer, data []byte, license string) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	encoder := xml.NewEncoder(w)
	metadataDepth := -1
	componentDepth := -1
	depth := 0
	found := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decoding generated CycloneDX XML: %w", err)
		}

		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if element.Name.Local == "metadata" && metadataDepth == -1 {
				metadataDepth = depth
			} else if element.Name.Local == "component" && depth == metadataDepth+1 {
				componentDepth = depth
			}
		case xml.EndElement:
			if element.Name.Local == "component" && depth == componentDepth {
				if err := encodeCycloneDXLicenseXML(encoder, license); err != nil {
					return err
				}
				found = true
				componentDepth = -1
			}
		}

		if err := encoder.EncodeToken(token); err != nil {
			return fmt.Errorf("encoding CycloneDX XML: %w", err)
		}
		if _, ok := token.(xml.EndElement); ok {
			if depth == metadataDepth {
				metadataDepth = -1
			}
			depth--
		}
	}
	if !found {
		return errors.New("generated CycloneDX SBOM has no root component")
	}
	if err := encoder.Flush(); err != nil {
		return fmt.Errorf("encoding CycloneDX XML: %w", err)
	}
	return nil
}

func encodeCycloneDXLicenseXML(encoder *xml.Encoder, license string) error {
	licenses := xml.StartElement{Name: xml.Name{Local: "licenses"}}
	expression := xml.StartElement{Name: xml.Name{Local: "expression"}}
	for _, token := range []xml.Token{
		licenses,
		expression,
		xml.CharData(license),
		expression.End(),
		licenses.End(),
	} {
		if err := encoder.EncodeToken(token); err != nil {
			return fmt.Errorf("encoding CycloneDX root license: %w", err)
		}
	}
	return nil
}
