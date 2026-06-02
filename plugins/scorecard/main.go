package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/enrichment/scorecard"
	"github.com/git-pkgs/purl"
)

const formatJSON = "json"
const versionUnknown = "unknown"

type dependency struct {
	Name         string `json:"name"`
	Ecosystem    string `json:"ecosystem"`
	PURL         string `json:"purl"`
	Requirement  string `json:"requirement"`
	ManifestPath string `json:"manifest_path"`
}

type scorecardSummary struct {
	TotalDependencies      int `json:"total_dependencies"`
	PackagesWithRepos      int `json:"packages_with_repos"`
	CheckedRepositories    int `json:"checked_repositories"`
	DisplayedDependencies  int `json:"displayed_dependencies"`
	PolicyFailures         int `json:"policy_failures"`
	SkippedNoRepository    int `json:"skipped_no_repository"`
	SkippedUnsupportedRepo int `json:"skipped_unsupported_repository"`
	LookupErrors           int `json:"lookup_errors"`
}

type scorecardEntry struct {
	Name         string           `json:"name"`
	Ecosystem    string           `json:"ecosystem"`
	Version      string           `json:"version,omitempty"`
	ManifestPath string           `json:"manifest_path,omitempty"`
	PURL         string           `json:"purl,omitempty"`
	Repository   string           `json:"repository"`
	Score        float64          `json:"score"`
	Date         string           `json:"date,omitempty"`
	Checks       []scorecardCheck `json:"checks,omitempty"`
	LookupError  string           `json:"lookup_error,omitempty"`
}

type scorecardCheck struct {
	Name   string `json:"name"`
	Score  int    `json:"score"`
	Reason string `json:"reason,omitempty"`
}

type scorecardResult struct {
	Summary      scorecardSummary `json:"summary"`
	Dependencies []scorecardEntry `json:"dependencies"`
}

type options struct {
	inputPath string
	format    string
	below     float64
	checks    map[string]bool
}

type packageLookup interface {
	BulkLookup(ctx context.Context, purls []string) (map[string]*enrichment.PackageInfo, error)
}

type scoreLookup interface {
	GetScore(ctx context.Context, repoURL string) (*scorecard.Result, error)
}

var newPackageLookup = func() (packageLookup, error) {
	return enrichment.NewClient(enrichment.WithUserAgent(scorecardUserAgent()))
}

var newScoreLookup = func() scoreLookup {
	return scorecard.New(scorecardUserAgent())
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	deps, err := loadDependencies(opts.inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading dependencies: %v\n", err)
		os.Exit(1)
	}

	result, err := runScorecard(deps, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scorecard: %v\n", err)
		os.Exit(1)
	}

	switch opts.format {
	case formatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "writing json: %v\n", err)
			os.Exit(1)
		}
	default:
		outputText(os.Stdout, result, opts)
	}

	if result.Summary.PolicyFailures > 0 {
		os.Exit(1)
	}
}

func scorecardUserAgent() string {
	pluginVersion := versionUnknown
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		pluginVersion = info.Main.Version
	}
	return "git-pkgs/" + pluginVersion
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("git-pkgs-scorecard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "", "Read dependency JSON from file instead of running git pkgs list --format json; use - for stdin")
	format := fs.String("format", "text", "Output format: text, json")
	below := fs.String("below", "", "Only show packages with an overall score or selected check score at or below N")
	checks := fs.String("checks", "", "Comma-separated scorecard checks to include, such as Maintained,Dangerous-Workflow")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}

	threshold := -1.0
	if *below != "" {
		parsed, err := strconv.ParseFloat(*below, 64)
		if err != nil {
			return options{}, fmt.Errorf("--below must be a number")
		}
		if parsed < 0 || parsed > 10 {
			return options{}, fmt.Errorf("--below must be between 0 and 10")
		}
		threshold = parsed
	}

	checkSet := make(map[string]bool)
	for _, check := range strings.Split(*checks, ",") {
		check = strings.TrimSpace(check)
		if check == "" {
			continue
		}
		checkSet[strings.ToLower(check)] = true
	}

	if *format != "text" && *format != formatJSON {
		return options{}, fmt.Errorf("unknown format %q", *format)
	}

	return options{
		inputPath: *input,
		format:    *format,
		below:     threshold,
		checks:    checkSet,
	}, nil
}

func loadDependencies(inputPath string) ([]dependency, error) {
	var raw []byte
	var err error

	switch inputPath {
	case "":
		raw, err = exec.Command("git", "pkgs", "list", "--format", "json").CombinedOutput()
	case "-":
		raw, err = io.ReadAll(os.Stdin)
	default:
		raw, err = os.ReadFile(inputPath)
	}
	if err != nil {
		output := strings.TrimSpace(string(raw))
		if output == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, output)
	}

	var deps []dependency
	if err := json.Unmarshal(raw, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

func runScorecard(deps []dependency, opts options) (*scorecardResult, error) {
	deps = uniqueDependencies(deps)
	result := &scorecardResult{
		Summary: scorecardSummary{
			TotalDependencies: len(deps),
		},
		Dependencies: []scorecardEntry{},
	}
	if len(deps) == 0 {
		return result, nil
	}

	purls, depByPURL := packagePURLs(deps)
	if len(purls) == 0 {
		result.Summary.SkippedNoRepository = len(deps)
		return result, nil
	}

	lookup, err := newPackageLookup()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	packages, err := lookup.BulkLookup(ctx, purls)
	if err != nil {
		return nil, err
	}

	scoreLookup := newScoreLookup()
	reposSeen := make(map[string]bool)
	repos := make([]string, 0, len(purls))
	repoByPURL := make(map[string]string, len(purls))
	for _, purlStr := range purls {
		pkg := packages[purlStr]
		if pkg == nil || pkg.Repository == "" {
			result.Summary.SkippedNoRepository++
			continue
		}

		repo := normalizeRepository(pkg.Repository)
		if repo == "" {
			result.Summary.SkippedUnsupportedRepo++
			continue
		}
		result.Summary.PackagesWithRepos++
		repoByPURL[purlStr] = repo

		if !reposSeen[repo] {
			reposSeen[repo] = true
			repos = append(repos, repo)
		}
	}

	scores, errorsByRepo := fetchScorecardResults(ctx, scoreLookup, repos)
	result.Summary.LookupErrors = len(errorsByRepo)
	result.Summary.CheckedRepositories = len(scores)

	for _, purlStr := range purls {
		dep := depByPURL[purlStr]
		repo := repoByPURL[purlStr]
		if repo == "" {
			continue
		}

		entry := scorecardEntry{
			Name:         dep.Name,
			Ecosystem:    dep.Ecosystem,
			Version:      dep.Requirement,
			ManifestPath: dep.ManifestPath,
			PURL:         purlStr,
			Repository:   repo,
		}
		if errMsg := errorsByRepo[repo]; errMsg != "" {
			entry.LookupError = errMsg
		} else if score := scores[repo]; score != nil {
			entry.Score = score.Score
			entry.Date = score.Date
			entry.Checks = filterChecks(score.Checks, opts.checks)
		}

		if includeEntry(entry, opts) {
			if isPolicyFailure(entry, opts) {
				result.Summary.PolicyFailures++
			}
			result.Dependencies = append(result.Dependencies, entry)
		}
	}

	sort.Slice(result.Dependencies, func(i, j int) bool {
		if result.Dependencies[i].Score != result.Dependencies[j].Score {
			return result.Dependencies[i].Score < result.Dependencies[j].Score
		}
		if result.Dependencies[i].Repository != result.Dependencies[j].Repository {
			return result.Dependencies[i].Repository < result.Dependencies[j].Repository
		}
		return result.Dependencies[i].Name < result.Dependencies[j].Name
	})
	result.Summary.DisplayedDependencies = len(result.Dependencies)

	return result, nil
}

type scorecardFetchResult struct {
	repo  string
	score *scorecard.Result
	err   error
}

func fetchScorecardResults(ctx context.Context, lookup scoreLookup, repos []string) (map[string]*scorecard.Result, map[string]string) {
	const scorecardLookupConcurrency = 8

	scores := make(map[string]*scorecard.Result, len(repos))
	errorsByRepo := make(map[string]string)
	if len(repos) == 0 {
		return scores, errorsByRepo
	}

	workers := scorecardLookupConcurrency
	if len(repos) < workers {
		workers = len(repos)
	}
	jobs := make(chan string)
	results := make(chan scorecardFetchResult, len(repos))

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for repo := range jobs {
				score, err := lookup.GetScore(ctx, repo)
				results <- scorecardFetchResult{repo: repo, score: score, err: err}
			}
		}()
	}

	for _, repo := range repos {
		jobs <- repo
	}
	close(jobs)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			errorsByRepo[result.repo] = result.err.Error()
			continue
		}
		if result.score != nil {
			scores[result.repo] = result.score
		}
	}

	return scores, errorsByRepo
}

func uniqueDependencies(deps []dependency) []dependency {
	seen := make(map[string]bool)
	var result []dependency
	for _, dep := range deps {
		key := strings.ToLower(dep.Ecosystem) + "\x00" + dep.Name + "\x00" + dep.ManifestPath + "\x00" + dep.Requirement
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, dep)
	}
	return result
}

func packagePURLs(deps []dependency) ([]string, map[string]dependency) {
	var purls []string
	depByPURL := make(map[string]dependency)
	seen := make(map[string]bool)
	for _, dep := range deps {
		purlStr := packagePURLForDependency(dep)
		if purlStr == "" || seen[purlStr] {
			continue
		}
		seen[purlStr] = true
		purls = append(purls, purlStr)
		depByPURL[purlStr] = dep
	}
	return purls, depByPURL
}

func packagePURLForDependency(dep dependency) string {
	if dep.PURL != "" {
		parsed, err := purl.Parse(dep.PURL)
		if err == nil {
			parsed.Version = ""
			return parsed.String()
		}
	}
	return purl.MakePURLString(dep.Ecosystem, dep.Name, "")
}

func normalizeRepository(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	if repo == "" {
		return ""
	}

	if !strings.Contains(repo, "://") {
		repo = "https://" + repo
	}
	parsed, err := url.Parse(repo)
	if err != nil {
		return ""
	}

	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "github.com", "gitlab.com":
	default:
		return ""
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return host + "/" + parts[0] + "/" + parts[1]
}

func filterChecks(checks []scorecard.Check, selected map[string]bool) []scorecardCheck {
	var result []scorecardCheck
	for _, check := range checks {
		if len(selected) > 0 && !selected[strings.ToLower(check.Name)] {
			continue
		}
		result = append(result, scorecardCheck{
			Name:   check.Name,
			Score:  check.Score,
			Reason: check.Reason,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

func includeEntry(entry scorecardEntry, opts options) bool {
	if entry.LookupError != "" {
		return true
	}
	if opts.below < 0 {
		return true
	}
	if entry.Score <= opts.below {
		return true
	}
	for _, check := range entry.Checks {
		if float64(check.Score) <= opts.below {
			return true
		}
	}
	return false
}

func isPolicyFailure(entry scorecardEntry, opts options) bool {
	if entry.LookupError != "" || opts.below < 0 {
		return false
	}
	if entry.Score <= opts.below {
		return true
	}
	for _, check := range entry.Checks {
		if float64(check.Score) <= opts.below {
			return true
		}
	}
	return false
}

func outputText(w io.Writer, result *scorecardResult, opts options) {
	if len(result.Dependencies) == 0 {
		_, _ = fmt.Fprintln(w, "No scorecard results matched.")
		if result.Summary.SkippedNoRepository > 0 {
			_, _ = fmt.Fprintf(w, "Skipped without repository URL: %d\n", result.Summary.SkippedNoRepository)
		}
		if result.Summary.SkippedUnsupportedRepo > 0 {
			_, _ = fmt.Fprintf(w, "Skipped unsupported repository host: %d\n", result.Summary.SkippedUnsupportedRepo)
		}
		if result.Summary.LookupErrors > 0 {
			_, _ = fmt.Fprintf(w, "Scorecard lookup errors: %d\n", result.Summary.LookupErrors)
		}
		return
	}

	_, _ = fmt.Fprintf(w, "Scorecard results for %d dependencies:\n\n", len(result.Dependencies))
	for _, entry := range result.Dependencies {
		_, _ = fmt.Fprintf(w, "%s (%s): %.1f %s\n", entry.Name, entry.Ecosystem, entry.Score, entry.Repository)
		if entry.LookupError != "" {
			_, _ = fmt.Fprintf(w, "  error: %s\n", entry.LookupError)
			continue
		}
		for _, check := range entry.Checks {
			if opts.below >= 0 && float64(check.Score) > opts.below {
				continue
			}
			_, _ = fmt.Fprintf(w, "  %s: %d", check.Name, check.Score)
			if check.Reason != "" {
				_, _ = fmt.Fprintf(w, " - %s", check.Reason)
			}
			_, _ = fmt.Fprintln(w)
		}
	}
}
