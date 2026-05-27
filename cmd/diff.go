package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/git-pkgs/git-pkgs/internal/analyzer"
	"github.com/git-pkgs/git-pkgs/internal/database"
	"github.com/git-pkgs/git-pkgs/internal/git"
	"github.com/spf13/cobra"
)

func addDiffCmd(parent *cobra.Command) {
	diffCmd := &cobra.Command{
		Use:   "diff [from..to]",
		Short: "Compare dependencies between commits or working tree",
		Long: `Compare dependencies between two commits, refs, or the working tree.
With no arguments, compares HEAD against the working tree (like git diff).
Supports range syntax (main..feature) or explicit --from/--to flags.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runDiff,
	}

	diffCmd.Flags().String("from", "", "Starting commit (default: HEAD)")
	diffCmd.Flags().String("to", "", "Ending commit (default: working tree)")
	diffCmd.Flags().StringP("ecosystem", "e", "", "Filter by ecosystem")
	diffCmd.Flags().StringP("type", "t", "", "Filter by dependency type (runtime, development, etc.)")
	diffCmd.Flags().StringP("format", "f", "text", "Output format: text, json")
	diffCmd.Flags().Bool("exclude-bots", false, "Exclude changes by bot authors")
	parent.AddCommand(diffCmd)
}

type DiffResult struct {
	Added    []DiffEntry `json:"added,omitempty"`
	Modified []DiffEntry `json:"modified,omitempty"`
	Removed  []DiffEntry `json:"removed,omitempty"`
}

type DiffEntry struct {
	Name            string `json:"name"`
	Ecosystem       string `json:"ecosystem,omitempty"`
	ManifestPath    string `json:"manifest_path"`
	DependencyType  string `json:"dependency_type,omitempty"`
	FromRequirement string `json:"from_requirement,omitempty"`
	ToRequirement   string `json:"to_requirement,omitempty"`
}

func runDiff(cmd *cobra.Command, args []string) error {
	fromRef, _ := cmd.Flags().GetString("from")
	toRef, _ := cmd.Flags().GetString("to")
	ecosystem, _ := cmd.Flags().GetString("ecosystem")
	depType, _ := cmd.Flags().GetString("type")
	format, _ := cmd.Flags().GetString("format")
	includeSubmodules, _ := cmd.Flags().GetBool("include-submodules")
	excludeBots, _ := cmd.Flags().GetBool("exclude-bots")

	// Parse range syntax if provided
	if len(args) > 0 {
		parts := strings.Split(args[0], "..")
		if len(parts) == 2 {
			fromRef = parts[0]
			toRef = parts[1]
		} else {
			fromRef = args[0]
		}
	}

	// Set defaults
	if fromRef == "" {
		fromRef = refHEAD
	}
	// toRef "" means working tree

	repo, err := git.OpenRepository(".")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	var result *DiffResult

	// When comparing to working tree, use direct parsing since there's
	// no database state for uncommitted changes
	if toRef == "" {
		if excludeBots {
			return fmt.Errorf("--exclude-bots requires a commit-to-commit diff")
		}
		result, err = diffWithWorkingTree(repo, fromRef, includeSubmodules)
	} else {
		result, err = diffBetweenCommits(repo, fromRef, toRef, excludeBots)
	}
	if err != nil {
		return err
	}

	// Apply filters
	if ecosystem != "" || depType != "" {
		result = filterDiffResult(result, ecosystem, depType)
	}

	if len(result.Added) == 0 && len(result.Modified) == 0 && len(result.Removed) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No dependency changes.")
		return nil
	}

	// Output
	switch format {
	case formatJSON:
		return outputDiffJSON(cmd, result)
	default:
		return outputDiffText(cmd, result)
	}
}

// diffBetweenCommits compares dependencies between two commits using on-demand indexing.
func diffBetweenCommits(repo *git.Repository, fromRef, toRef string, excludeBots bool) (*DiffResult, error) {
	fromDeps, err := repo.GetDependencies(fromRef, "")
	if err != nil {
		return nil, fmt.Errorf("getting deps at %s: %w", fromRef, err)
	}

	if excludeBots {
		toDeps, err := depsAfterNonBotCommits(repo, fromRef, toRef, fromDeps)
		if err != nil {
			return nil, err
		}
		return computeDiff(fromDeps, toDeps), nil
	}

	toDeps, err := repo.GetDependencies(toRef, "")
	if err != nil {
		return nil, fmt.Errorf("getting deps at %s: %w", toRef, err)
	}

	return computeDiff(fromDeps, toDeps), nil
}

type diffCommit struct {
	SHA       string
	Author    string
	Email     string
	ParentSHA string
}

func depsAfterNonBotCommits(repo *git.Repository, fromRef, toRef string, fromDeps []database.Dependency) ([]database.Dependency, error) {
	commits, err := commitsInRange(repo, fromRef, toRef)
	if err != nil {
		return nil, err
	}

	deps := append([]database.Dependency(nil), fromDeps...)
	for _, c := range commits {
		if database.IsBotAuthor(c.Author, c.Email) {
			continue
		}

		var parentDeps []database.Dependency
		if c.ParentSHA != "" {
			parentDeps, err = repo.GetDependencies(c.ParentSHA, "")
			if err != nil {
				return nil, fmt.Errorf("getting deps at %s: %w", c.ParentSHA, err)
			}
		}

		commitDeps, err := repo.GetDependencies(c.SHA, "")
		if err != nil {
			return nil, fmt.Errorf("getting deps at %s: %w", c.SHA, err)
		}

		deps = applyDiffToDeps(deps, computeDiff(parentDeps, commitDeps))
	}

	return deps, nil
}

func commitsInRange(repo *git.Repository, fromRef, toRef string) ([]diffCommit, error) {
	gitCmd := exec.Command("git", "log", "--reverse", "--format=%H%x00%an%x00%ae%x00%P%x1e", fromRef+".."+toRef)
	gitCmd.Dir = repo.WorkDir()
	out, err := gitCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing commits in %s..%s: %w", fromRef, toRef, err)
	}

	var commits []diffCommit
	for _, record := range strings.Split(strings.TrimSuffix(string(out), "\x1e\n"), "\x1e") {
		record = strings.Trim(record, "\n")
		if record == "" {
			continue
		}
		parts := strings.Split(record, "\x00")
		if len(parts) < 4 {
			continue
		}
		parentSHA := ""
		if parents := strings.Fields(parts[3]); len(parents) > 0 {
			parentSHA = parents[0]
		}
		commits = append(commits, diffCommit{
			SHA:       parts[0],
			Author:    parts[1],
			Email:     parts[2],
			ParentSHA: parentSHA,
		})
	}
	return commits, nil
}

func applyDiffToDeps(deps []database.Dependency, diff *DiffResult) []database.Dependency {
	for _, e := range diff.Removed {
		deps = removeDiffEntry(deps, e)
	}
	for _, e := range diff.Modified {
		if !modifyDiffEntry(deps, e) {
			deps = append(deps, dependencyFromDiffEntry(e))
		}
	}
	for _, e := range diff.Added {
		deps = append(deps, dependencyFromDiffEntry(e))
	}
	return deps
}

func dependencyFromDiffEntry(e DiffEntry) database.Dependency {
	return database.Dependency{
		Name:           e.Name,
		Ecosystem:      e.Ecosystem,
		Requirement:    e.ToRequirement,
		ManifestPath:   e.ManifestPath,
		DependencyType: e.DependencyType,
	}
}

func removeDiffEntry(deps []database.Dependency, e DiffEntry) []database.Dependency {
	for i, d := range deps {
		if d.Name == e.Name && d.ManifestPath == e.ManifestPath && d.Requirement == e.FromRequirement {
			return append(deps[:i], deps[i+1:]...)
		}
	}
	return deps
}

func modifyDiffEntry(deps []database.Dependency, e DiffEntry) bool {
	for i := range deps {
		d := deps[i]
		if d.Name == e.Name && d.ManifestPath == e.ManifestPath && d.Requirement == e.FromRequirement {
			deps[i].Requirement = e.ToRequirement
			deps[i].Ecosystem = e.Ecosystem
			deps[i].DependencyType = e.DependencyType
			return true
		}
	}
	return false
}

// diffWithWorkingTree compares dependencies between a commit and the working tree.
func diffWithWorkingTree(repo *git.Repository, fromRef string, includeSubmodules bool) (*DiffResult, error) {
	fromDeps, err := repo.GetDependencies(fromRef, "")
	if err != nil {
		return nil, fmt.Errorf("getting deps at %s: %w", fromRef, err)
	}

	// Parse working tree directly
	a := analyzer.New()
	toChanges, err := a.DependenciesInWorkingDir(repo.WorkDir(), includeSubmodules)
	if err != nil {
		return nil, fmt.Errorf("reading working tree: %w", err)
	}
	toDeps := changesToDeps(toChanges)

	return computeDiff(fromDeps, toDeps), nil
}

func changesToDeps(changes []analyzer.Change) []database.Dependency {
	var deps []database.Dependency
	for _, c := range changes {
		deps = append(deps, database.Dependency{
			Name:           c.Name,
			Ecosystem:      c.Ecosystem,
			Requirement:    c.Requirement,
			ManifestPath:   c.ManifestPath,
			DependencyType: c.DependencyType,
		})
	}
	return deps
}

func computeDiff(fromDeps, toDeps []database.Dependency) *DiffResult {
	result := &DiffResult{}

	// Build multi-maps keyed by manifest:name, since lockfiles can contain
	// the same package at multiple versions (e.g. npm dependency hoisting).
	type depKey struct {
		ManifestPath string
		Name         string
	}

	fromMulti := make(map[depKey][]database.Dependency)
	for _, d := range fromDeps {
		key := depKey{d.ManifestPath, d.Name}
		fromMulti[key] = append(fromMulti[key], d)
	}

	toMulti := make(map[depKey][]database.Dependency)
	for _, d := range toDeps {
		key := depKey{d.ManifestPath, d.Name}
		toMulti[key] = append(toMulti[key], d)
	}

	// Find added and modified
	for key, toList := range toMulti {
		fromList, exists := fromMulti[key]
		if !exists {
			// Entirely new package
			for _, d := range toList {
				result.Added = append(result.Added, DiffEntry{
					Name:           d.Name,
					Ecosystem:      d.Ecosystem,
					ManifestPath:   d.ManifestPath,
					DependencyType: d.DependencyType,
					ToRequirement:  d.Requirement,
				})
			}
			continue
		}

		// Single version on each side: compare directly (shows "modified")
		if len(fromList) == 1 && len(toList) == 1 {
			if fromList[0].Requirement != toList[0].Requirement {
				result.Modified = append(result.Modified, DiffEntry{
					Name:            toList[0].Name,
					Ecosystem:       toList[0].Ecosystem,
					ManifestPath:    toList[0].ManifestPath,
					DependencyType:  toList[0].DependencyType,
					FromRequirement: fromList[0].Requirement,
					ToRequirement:   toList[0].Requirement,
				})
			}
			continue
		}

		// Multiple versions on at least one side: count occurrences of each
		// version and pair net removals with net additions as Modified.
		fromCounts := make(map[string]int, len(fromList))
		for _, d := range fromList {
			fromCounts[d.Requirement]++
		}
		toCounts := make(map[string]int, len(toList))
		for _, d := range toList {
			toCounts[d.Requirement]++
		}

		// Use the first entry as a template for metadata.
		ref := toList[0]

		// Build lists of individual net removals and net additions.
		var removedVersions []string
		var addedVersions []string

		// Versions that decreased in count or disappeared entirely.
		for ver, fc := range fromCounts {
			tc := toCounts[ver]
			if delta := fc - tc; delta > 0 {
				for i := 0; i < delta; i++ {
					removedVersions = append(removedVersions, ver)
				}
			}
		}
		// Versions that increased in count or appeared for the first time.
		for ver, tc := range toCounts {
			fc := fromCounts[ver]
			if delta := tc - fc; delta > 0 {
				for i := 0; i < delta; i++ {
					addedVersions = append(addedVersions, ver)
				}
			}
		}

		// Sort for deterministic pairing.
		sort.Strings(removedVersions)
		sort.Strings(addedVersions)

		// Pair removals with additions as Modified.
		paired := len(removedVersions)
		if len(addedVersions) < paired {
			paired = len(addedVersions)
		}
		for i := 0; i < paired; i++ {
			result.Modified = append(result.Modified, DiffEntry{
				Name:            ref.Name,
				Ecosystem:       ref.Ecosystem,
				ManifestPath:    ref.ManifestPath,
				DependencyType:  ref.DependencyType,
				FromRequirement: removedVersions[i],
				ToRequirement:   addedVersions[i],
			})
		}
		// Surplus additions.
		for i := paired; i < len(addedVersions); i++ {
			result.Added = append(result.Added, DiffEntry{
				Name:           ref.Name,
				Ecosystem:      ref.Ecosystem,
				ManifestPath:   ref.ManifestPath,
				DependencyType: ref.DependencyType,
				ToRequirement:  addedVersions[i],
			})
		}
		// Surplus removals.
		for i := paired; i < len(removedVersions); i++ {
			result.Removed = append(result.Removed, DiffEntry{
				Name:            ref.Name,
				Ecosystem:       ref.Ecosystem,
				ManifestPath:    ref.ManifestPath,
				DependencyType:  ref.DependencyType,
				FromRequirement: removedVersions[i],
			})
		}
	}

	// Find removed (packages not in toMulti at all)
	for key, fromList := range fromMulti {
		if _, exists := toMulti[key]; !exists {
			for _, d := range fromList {
				result.Removed = append(result.Removed, DiffEntry{
					Name:            d.Name,
					Ecosystem:       d.Ecosystem,
					ManifestPath:    d.ManifestPath,
					DependencyType:  d.DependencyType,
					FromRequirement: d.Requirement,
				})
			}
		}
	}

	// Sort results for deterministic output
	sortDiffEntries(result.Added)
	sortDiffEntries(result.Modified)
	sortDiffEntries(result.Removed)

	return result
}

func sortDiffEntries(entries []DiffEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ManifestPath != entries[j].ManifestPath {
			return entries[i].ManifestPath < entries[j].ManifestPath
		}
		return entries[i].Name < entries[j].Name
	})
}

func filterDiffResult(result *DiffResult, ecosystem, depType string) *DiffResult {
	filtered := &DiffResult{}

	for _, e := range result.Added {
		if ecosystem != "" && !strings.EqualFold(e.Ecosystem, ecosystem) {
			continue
		}
		if depType != "" && !strings.EqualFold(e.DependencyType, depType) {
			continue
		}
		filtered.Added = append(filtered.Added, e)
	}
	for _, e := range result.Modified {
		if ecosystem != "" && !strings.EqualFold(e.Ecosystem, ecosystem) {
			continue
		}
		if depType != "" && !strings.EqualFold(e.DependencyType, depType) {
			continue
		}
		filtered.Modified = append(filtered.Modified, e)
	}
	for _, e := range result.Removed {
		if ecosystem != "" && !strings.EqualFold(e.Ecosystem, ecosystem) {
			continue
		}
		if depType != "" && !strings.EqualFold(e.DependencyType, depType) {
			continue
		}
		filtered.Removed = append(filtered.Removed, e)
	}

	return filtered
}

func outputDiffJSON(cmd *cobra.Command, result *DiffResult) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func outputDiffText(cmd *cobra.Command, result *DiffResult) error {
	if len(result.Added) > 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), Bold("Added:"))
		for _, e := range result.Added {
			line := fmt.Sprintf("  %s %s", Green("+"), Green(e.Name))
			if e.ToRequirement != "" {
				line += fmt.Sprintf(" %s", e.ToRequirement)
			}
			if e.DependencyType != "" && e.DependencyType != depTypeRuntime {
				line += fmt.Sprintf(" [%s]", e.DependencyType)
			}
			line += fmt.Sprintf(" %s", Dim("("+e.ManifestPath+")"))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}

	if len(result.Modified) > 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), Bold("Modified:"))
		for _, e := range result.Modified {
			line := fmt.Sprintf("  %s %s %s -> %s", Yellow("~"), Yellow(e.Name), Dim(e.FromRequirement), e.ToRequirement)
			if e.DependencyType != "" && e.DependencyType != depTypeRuntime {
				line += fmt.Sprintf(" [%s]", e.DependencyType)
			}
			line += fmt.Sprintf(" %s", Dim("("+e.ManifestPath+")"))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}

	if len(result.Removed) > 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), Bold("Removed:"))
		for _, e := range result.Removed {
			line := fmt.Sprintf("  %s %s", Red("-"), Red(e.Name))
			if e.FromRequirement != "" {
				line += fmt.Sprintf(" %s", e.FromRequirement)
			}
			if e.DependencyType != "" && e.DependencyType != depTypeRuntime {
				line += fmt.Sprintf(" [%s]", e.DependencyType)
			}
			line += fmt.Sprintf(" %s", Dim("("+e.ManifestPath+")"))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}

	return nil
}
