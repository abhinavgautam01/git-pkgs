package cmd

import (
	"bytes"
	"crypto/sha256"
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
	"github.com/git-pkgs/spdx"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const spdxRootPackageID = "SPDXRef-Package-root"

type projectLicenses struct {
	Expression string
	Names      []string
}

func (l projectLicenses) empty() bool {
	return l.Expression == "" && len(l.Names) == 0
}

func projectLicensesAtRevision(repo *git.Repository, revision string) (projectLicenses, error) {
	if revision == "" {
		revision = "HEAD"
	}

	hash, err := repo.ResolveRevision(revision)
	if err != nil {
		return projectLicenses{}, fmt.Errorf("resolving %q: %w", revision, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return projectLicenses{}, fmt.Errorf("getting commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return projectLicenses{}, fmt.Errorf("getting commit tree: %w", err)
	}

	var declaredLicenses []string
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
		declaredLicenses = append(declaredLicenses, result.Licenses...)
		return nil
	})
	if err != nil {
		return projectLicenses{}, err
	}
	return normalizeProjectLicenses(declaredLicenses), nil
}

func normalizeProjectLicenses(values []string) projectLicenses {
	expressionSet := make(map[string]bool)
	nameSet := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		normalized, err := spdx.NormalizeExpressionLax(value)
		if err != nil || !spdx.Valid(normalized) {
			normalized, err = spdx.Normalize(value)
		}
		if err == nil && spdx.Valid(normalized) {
			expressionSet[normalized] = true
		} else {
			nameSet[value] = true
		}
	}

	expressions := sortedKeys(expressionSet)
	names := sortedKeys(nameSet)
	return projectLicenses{
		Expression: joinSPDXExpressions(expressions),
		Names:      names,
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func joinSPDXExpressions(expressions []string) string {
	if len(expressions) == 0 {
		return ""
	}
	if len(expressions) == 1 {
		return expressions[0]
	}
	for i, expression := range expressions {
		if strings.Contains(expression, " AND ") || strings.Contains(expression, " OR ") {
			expressions[i] = "(" + expression + ")"
		}
	}
	joined, err := spdx.NormalizeExpression(strings.Join(expressions, " OR "))
	if err != nil {
		return ""
	}
	return joined
}

func encodeSBOMWithRootLicenses(
	w io.Writer,
	document *sbom.SBOM,
	format sbom.Format,
	licenses projectLicenses,
) error {
	if licenses.empty() {
		return sbom.Encode(w, document, format)
	}

	var encoded bytes.Buffer
	if err := sbom.Encode(&encoded, document, format); err != nil {
		return err
	}

	switch format {
	case sbom.FormatCycloneDXJSON, sbom.FormatSPDXJSON:
		data, err := addRootLicensesJSON(encoded.Bytes(), format, licenses)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	case sbom.FormatCycloneDXXML:
		return addCycloneDXRootLicensesXML(w, encoded.Bytes(), licenses)
	default:
		return fmt.Errorf("unsupported SBOM format %d", format)
	}
}

func addRootLicensesJSON(data []byte, format sbom.Format, licenses projectLicenses) ([]byte, error) {
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
		component["licenses"] = cycloneDXLicenseChoices(licenses)
	case sbom.FormatSPDXJSON:
		packages, ok := document["packages"].([]any)
		if !ok {
			return nil, errors.New("generated SPDX SBOM has no packages")
		}
		found := false
		for _, value := range packages {
			pkg, ok := value.(map[string]any)
			if ok && pkg["SPDXID"] == spdxRootPackageID {
				pkg["licenseDeclared"] = licenses.spdxExpression()
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("generated SPDX SBOM has no root package")
		}
		if len(licenses.Names) > 0 {
			document["hasExtractedLicensingInfos"] = extractedLicenseInfo(licenses.Names)
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

func cycloneDXLicenseChoices(licenses projectLicenses) []any {
	choices := make([]any, 0, 1+len(licenses.Names))
	if licenses.Expression != "" {
		choices = append(choices, map[string]any{"expression": licenses.Expression})
	}
	for _, name := range licenses.Names {
		choices = append(choices, map[string]any{"license": map[string]any{"name": name}})
	}
	return choices
}

func (l projectLicenses) spdxExpression() string {
	parts := make([]string, 0, 1+len(l.Names))
	if l.Expression != "" {
		parts = append(parts, l.Expression)
	}
	for _, name := range l.Names {
		parts = append(parts, licenseRefForName(name))
	}
	return joinSPDXExpressions(parts)
}

func licenseRefForName(name string) string {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("LicenseRef-Manifest-%x", digest[:8])
}

func extractedLicenseInfo(names []string) []any {
	infos := make([]any, 0, len(names))
	for _, name := range names {
		infos = append(infos, map[string]any{
			"licenseId":     licenseRefForName(name),
			"name":          name,
			"extractedText": name,
		})
	}
	return infos
}

func addCycloneDXRootLicensesXML(w io.Writer, data []byte, licenses projectLicenses) error {
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
				if err := encodeCycloneDXLicensesXML(encoder, licenses); err != nil {
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

func encodeCycloneDXLicensesXML(encoder *xml.Encoder, licenses projectLicenses) error {
	licensesElement := xml.StartElement{Name: xml.Name{Local: "licenses"}}
	if err := encoder.EncodeToken(licensesElement); err != nil {
		return fmt.Errorf("encoding CycloneDX root licenses: %w", err)
	}
	if licenses.Expression != "" {
		if err := encodeXMLElement(encoder, "expression", licenses.Expression); err != nil {
			return err
		}
	}
	for _, name := range licenses.Names {
		licenseElement := xml.StartElement{Name: xml.Name{Local: "license"}}
		if err := encoder.EncodeToken(licenseElement); err != nil {
			return fmt.Errorf("encoding CycloneDX root licenses: %w", err)
		}
		if err := encodeXMLElement(encoder, "name", name); err != nil {
			return err
		}
		if err := encoder.EncodeToken(licenseElement.End()); err != nil {
			return fmt.Errorf("encoding CycloneDX root licenses: %w", err)
		}
	}
	if err := encoder.EncodeToken(licensesElement.End()); err != nil {
		return fmt.Errorf("encoding CycloneDX root licenses: %w", err)
	}
	return nil
}

func encodeXMLElement(encoder *xml.Encoder, name, value string) error {
	element := xml.StartElement{Name: xml.Name{Local: name}}
	if err := encoder.EncodeElement(value, element); err != nil {
		return fmt.Errorf("encoding CycloneDX root licenses: %w", err)
	}
	return nil
}
