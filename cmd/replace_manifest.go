package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var regexpHexSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func replaceInCargoManifest(cmd *cobra.Command, path string, opts replaceOptions) error {
	entry, err := replacementEntry(opts, "path", "git")
	if err != nil {
		return err
	}
	return replaceTOMLEntry(cmd, path, "[patch.crates-io]", opts.Package, entry, opts)
}

func replaceInUVManifest(cmd *cobra.Command, path string, opts replaceOptions) error {
	entry, err := replacementEntry(opts, "path", "git")
	if err != nil {
		return err
	}
	if opts.Mode == replaceModePath {
		entry = fmt.Sprintf("{ path = %s, editable = true }", quoteTOMLString(opts.Path))
	}
	return replaceTOMLEntry(cmd, path, "[tool.uv.sources]", opts.Package, entry, opts)
}

func replaceInGemfile(cmd *cobra.Command, path string, opts replaceOptions) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading Gemfile: %w", err)
	}

	lines := strings.SplitAfter(string(content), "\n")
	lineRe := regexp.MustCompile(`^(\s*)gem\s+["']` + regexp.QuoteMeta(opts.Package) + `["']`)
	found := false
	for i, line := range lines {
		if !lineRe.MatchString(strings.TrimRight(line, "\n")) {
			continue
		}
		found = true
		updated, err := updateGemfileLine(line, opts)
		if err != nil {
			return err
		}
		lines[i] = updated
		break
	}
	if !found && opts.Mode != replaceModeDrop {
		lines = append(lines, fmt.Sprintf("gem %q%s", opts.Package, gemReplacementSuffix(opts)))
		found = true
	}
	if !found {
		return fmt.Errorf("gem %q not found in Gemfile", opts.Package)
	}

	newContent := strings.Join(lines, "")
	if opts.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would edit: %s\n", path)
		return nil
	}
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing Gemfile: %w", err)
	}
	return nil
}

func replaceTOMLEntry(cmd *cobra.Command, path, section, key, entry string, opts replaceOptions) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	newContent, err := updateTOMLSectionEntry(string(content), section, key, entry, opts.Mode == replaceModeDrop)
	if err != nil {
		return err
	}
	if opts.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would edit: %s\n", path)
		return nil
	}
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	return nil
}

func updateTOMLSectionEntry(content, section, key, entry string, drop bool) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	targetSection := tomlSectionName(section)
	start := -1
	end := len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if tomlSectionName(trimmed) == targetSection {
			start = i
			continue
		}
		if start >= 0 && i > start && tomlSectionName(trimmed) != "" {
			end = i
			break
		}
	}

	keyName := tomlKey(key)
	filterEntry := func(sectionLines []string) []string {
		filtered := sectionLines[:0]
		for _, line := range sectionLines {
			if tomlLineHasKey(line, keyName) {
				continue
			}
			filtered = append(filtered, line)
		}
		return filtered
	}

	if start == -1 {
		if drop {
			return content, nil
		}
		prefix := content
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		if prefix != "" && !strings.HasSuffix(prefix, "\n\n") {
			prefix += "\n"
		}
		return prefix + section + "\n" + keyName + " = " + entry + "\n", nil
	}

	before := append([]string{}, lines[:start]...)
	header := lines[start : start+1]
	middle := append([]string{}, lines[start+1:end]...)
	after := append([]string{}, lines[end:]...)
	middle = filterEntry(middle)
	if !drop {
		middle = append(middle, keyName+" = "+entry+"\n")
	}
	if drop && !tomlSectionHasEntries(middle) {
		return strings.Join(append(before, after...), ""), nil
	}
	return strings.Join(append(append(append(before, header...), middle...), after...), ""), nil
}

func tomlSectionName(line string) string {
	line = strings.TrimSpace(stripTOMLComment(line))
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	end := strings.Index(line, "]")
	if end == -1 {
		return ""
	}
	return line[:end+1]
}

func stripTOMLComment(line string) string {
	if idx := strings.Index(line, "#"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func tomlSectionHasEntries(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripTOMLComment(line))
		if trimmed != "" {
			return true
		}
	}
	return false
}

func tomlLineHasKey(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=")
}

func replacementEntry(opts replaceOptions, pathKey, gitKey string) (string, error) {
	switch opts.Mode {
	case replaceModePath:
		return fmt.Sprintf("{ %s = %s }", pathKey, quoteTOMLString(opts.Path)), nil
	case replaceModeGit:
		parts := []string{fmt.Sprintf("%s = %s", gitKey, quoteTOMLString(opts.Git))}
		if opts.Ref != "" {
			parts = append(parts, fmt.Sprintf("%s = %s", tomlGitRefKey(opts.Ref), quoteTOMLString(opts.Ref)))
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	case replaceModeDrop:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported replacement mode %q", opts.Mode)
	}
}

func tomlGitRefKey(ref string) string {
	if regexpHexSHA.MatchString(ref) {
		return "rev"
	}
	return "branch"
}

func gemReplacementSuffix(opts replaceOptions) string {
	switch opts.Mode {
	case replaceModePath:
		return fmt.Sprintf(", path: %q\n", opts.Path)
	case replaceModeGit:
		ref := ""
		if opts.Ref != "" {
			ref = fmt.Sprintf(", branch: %q", opts.Ref)
		}
		return fmt.Sprintf(", git: %q%s\n", opts.Git, ref)
	default:
		return "\n"
	}
}

func updateGemfileLine(line string, opts replaceOptions) (string, error) {
	newline := ""
	if strings.HasSuffix(line, "\n") {
		newline = "\n"
		line = strings.TrimSuffix(line, "\n")
	}

	lineRe := regexp.MustCompile(`^(\s*)gem\s+["']` + regexp.QuoteMeta(opts.Package) + `["'](.*)$`)
	matches := lineRe.FindStringSubmatch(line)
	if len(matches) != 3 {
		return "", fmt.Errorf("gem %q not found in Gemfile line", opts.Package)
	}

	args, comment := parseGemfileArgs(matches[2])
	args = filterGemReplacementArgs(args)

	switch opts.Mode {
	case replaceModePath:
		args = append(args, fmt.Sprintf("path: %q", opts.Path))
	case replaceModeGit:
		args = append(args, fmt.Sprintf("git: %q", opts.Git))
		if opts.Ref != "" {
			args = append(args, fmt.Sprintf("branch: %q", opts.Ref))
		}
	case replaceModeDrop:
	default:
		return "", fmt.Errorf("unsupported replacement mode %q", opts.Mode)
	}

	return fmt.Sprintf("%sgem %q%s%s", matches[1], opts.Package, formatGemfileArgs(args, comment), newline), nil
}

func parseGemfileArgs(tail string) ([]string, string) {
	argsText, comment := splitRubyLineComment(tail)
	parts := splitRubyArgs(argsText)
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		args = append(args, part)
	}
	return args, comment
}

func splitRubyLineComment(s string) (string, string) {
	var quote rune
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == '#' {
			return strings.TrimRight(s[:i], " \t"), s[i:]
		}
	}
	return s, ""
}

func splitRubyArgs(s string) []string {
	var parts []string
	var quote rune
	escaped := false
	start := 0
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func filterGemReplacementArgs(args []string) []string {
	filtered := args[:0]
	for _, arg := range args {
		if isGemReplacementArg(arg) {
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func isGemReplacementArg(arg string) bool {
	key := gemArgKey(arg)
	switch key {
	case "path", "git", "github", "gist", "bitbucket", "branch", "tag", "ref":
		return true
	default:
		return false
	}
}

func gemArgKey(arg string) string {
	key := strings.TrimSpace(arg)
	key = strings.TrimPrefix(key, ":")
	if before, _, ok := strings.Cut(key, "=>"); ok {
		return strings.TrimSpace(before)
	}
	if before, _, ok := strings.Cut(key, ":"); ok {
		return strings.TrimSpace(before)
	}
	return key
}

func formatGemfileArgs(args []string, comment string) string {
	var tail string
	if len(args) > 0 {
		tail = ", " + strings.Join(args, ", ")
	}
	if comment != "" {
		if tail != "" {
			tail += " "
		}
		tail += comment
	}
	return tail
}

func editsCargoManifest(managerName string) bool {
	return managerName == "cargo"
}

func editsUVManifest(managerName string) bool {
	return managerName == "uv"
}

func tomlKey(key string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(key) {
		return key
	}
	return quoteTOMLString(key)
}

func quoteTOMLString(s string) string {
	return quoteJSONString(s)
}

func quoteJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
