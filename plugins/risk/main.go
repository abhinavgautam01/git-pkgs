package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ecosystems "github.com/ecosyste-ms/ecosystems-go/packages"
	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all"
)

const (
	checkTyposquat      = "typosquat"
	checkConfusion      = "confusion"
	checkInstallScripts = "install-scripts"
	formatJSON          = "json"
	popularPackageLimit = 250
	riskLookupTimeout   = 5 * time.Minute
	popularCacheTTL     = 24 * time.Hour
	npmRegistryBaseURL  = "https://registry.npmjs.org"
)

var errPackageNotFound = errors.New("package not found")

type dependency struct {
	Name           string `json:"name"`
	Ecosystem      string `json:"ecosystem"`
	PURL           string `json:"purl"`
	Requirement    string `json:"requirement"`
	DependencyType string `json:"dependency_type"`
	ManifestPath   string `json:"manifest_path"`
	ManifestKind   string `json:"manifest_kind"`
}

type riskSummary struct {
	TotalDependencies int            `json:"total_dependencies"`
	Issues            int            `json:"issues"`
	ByCheck           map[string]int `json:"by_check"`
}

type riskIssue struct {
	Check        string `json:"check"`
	Severity     string `json:"severity"`
	Name         string `json:"name"`
	Ecosystem    string `json:"ecosystem"`
	Version      string `json:"version,omitempty"`
	ManifestPath string `json:"manifest_path,omitempty"`
	Reason       string `json:"reason"`
	Evidence     string `json:"evidence,omitempty"`
	SimilarTo    string `json:"similar_to,omitempty"`
}

type riskResult struct {
	Summary riskSummary `json:"summary"`
	Issues  []riskIssue `json:"issues"`
}

type options struct {
	inputPath    string
	format       string
	checks       map[string]bool
	popularLimit int
	cacheDir     string
}

type riskServices struct {
	popularPackages popularPackageSource
	publicRegistry  publicRegistryLookup
	npmManifests    npmManifestFetcher
}

type popularPackageSource interface {
	PopularPackages(ctx context.Context, ecosystem string, limit int) ([]string, error)
}

type publicRegistryLookup interface {
	PublicPackageExists(ctx context.Context, dep dependency) (bool, string, error)
}

type npmManifestFetcher interface {
	NPMScripts(ctx context.Context, name, version string) (map[string]string, error)
}

type ecosystemsRegistryClient interface {
	GetRegistryPackagesWithResponse(ctx context.Context, registryName string, params *ecosystems.GetRegistryPackagesParams, reqEditors ...ecosystems.RequestEditorFn) (*ecosystems.GetRegistryPackagesResponse, error)
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

	ctx, cancel := context.WithTimeout(context.Background(), riskLookupTimeout)
	defer cancel()

	services, err := newRiskServices(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initializing risk checks: %v\n", err)
		os.Exit(1)
	}

	result, err := runRiskChecks(ctx, deps, opts, services)
	if err != nil {
		fmt.Fprintf(os.Stderr, "checking risk signals: %v\n", err)
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
		outputText(os.Stdout, result)
	}

	if len(result.Issues) > 0 {
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("git-pkgs-risk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "", "Read dependency JSON from file instead of running git pkgs list --format json; use - for stdin")
	format := fs.String("format", "text", "Output format: text, json")
	checks := fs.String("check", "typosquat,confusion,install-scripts", "Comma-separated checks to run")
	popularLimit := fs.Int("popular-limit", popularPackageLimit, "Number of popular packages per ecosystem to compare for typosquat checks")
	cacheDir := fs.String("cache-dir", "", "Directory for risk-check metadata cache")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}

	checkSet := make(map[string]bool)
	for _, check := range strings.Split(*checks, ",") {
		check = strings.TrimSpace(check)
		if check == "" {
			continue
		}
		switch check {
		case checkTyposquat, checkConfusion, checkInstallScripts:
			checkSet[check] = true
		default:
			return options{}, fmt.Errorf("unknown check %q", check)
		}
	}
	if len(checkSet) == 0 {
		return options{}, fmt.Errorf("at least one check is required")
	}
	if *format != "text" && *format != formatJSON {
		return options{}, fmt.Errorf("unknown format %q", *format)
	}
	if *popularLimit <= 0 {
		return options{}, fmt.Errorf("popular-limit must be greater than zero")
	}
	if *cacheDir == "" {
		*cacheDir = defaultCacheDir()
	}

	return options{
		inputPath:    *input,
		format:       *format,
		checks:       checkSet,
		popularLimit: *popularLimit,
		cacheDir:     *cacheDir,
	}, nil
}

func defaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "git-pkgs", "risk")
}

func newRiskServices(opts options) (riskServices, error) {
	client, err := ecosystems.NewClientWithResponses("https://packages.ecosyste.ms")
	if err != nil {
		return riskServices{}, err
	}
	return riskServices{
		popularPackages: &ecosystemsPopularPackageSource{
			cacheDir: opts.cacheDir,
			client:   client,
		},
		publicRegistry: registryPublicLookup{},
		npmManifests: &httpNPMManifestFetcher{
			client:  &http.Client{Timeout: 30 * time.Second},
			baseURL: npmRegistryBaseURL,
		},
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

func runRiskChecks(ctx context.Context, deps []dependency, opts options, services riskServices) (riskResult, error) {
	uniqueDeps := uniqueDependencies(deps)
	result := riskResult{
		Summary: riskSummary{
			TotalDependencies: len(uniqueDeps),
			ByCheck:           map[string]int{},
		},
		Issues: []riskIssue{},
	}

	if opts.checks[checkTyposquat] {
		issues, err := detectTyposquatting(ctx, uniqueDeps, services.popularPackages, opts.popularLimit)
		if err != nil {
			return riskResult{}, err
		}
		result.Issues = append(result.Issues, issues...)
	}
	if opts.checks[checkConfusion] {
		issues, err := detectDependencyConfusion(ctx, uniqueDeps, services.publicRegistry)
		if err != nil {
			return riskResult{}, err
		}
		result.Issues = append(result.Issues, issues...)
	}
	if opts.checks[checkInstallScripts] {
		issues, err := detectInstallScripts(ctx, uniqueDeps, services.npmManifests)
		if err != nil {
			return riskResult{}, err
		}
		result.Issues = append(result.Issues, issues...)
	}

	sort.Slice(result.Issues, func(i, j int) bool {
		if result.Issues[i].Severity != result.Issues[j].Severity {
			return severityRank(result.Issues[i].Severity) > severityRank(result.Issues[j].Severity)
		}
		if result.Issues[i].Check != result.Issues[j].Check {
			return result.Issues[i].Check < result.Issues[j].Check
		}
		if result.Issues[i].Ecosystem != result.Issues[j].Ecosystem {
			return result.Issues[i].Ecosystem < result.Issues[j].Ecosystem
		}
		return result.Issues[i].Name < result.Issues[j].Name
	})

	result.Summary.Issues = len(result.Issues)
	for _, issue := range result.Issues {
		result.Summary.ByCheck[issue.Check]++
	}
	return result, nil
}

func severityRank(severity string) int {
	switch severity {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
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

type ecosystemsPopularPackageSource struct {
	cacheDir string
	client   ecosystemsRegistryClient
}

type cachedPopularPackages struct {
	FetchedAt time.Time `json:"fetched_at"`
	Packages  []string  `json:"packages"`
}

func (s *ecosystemsPopularPackageSource) PopularPackages(ctx context.Context, ecosystem string, limit int) ([]string, error) {
	registryName := registryNameForEcosystem(ecosystem)
	cachePath := filepath.Join(s.cacheDir, fmt.Sprintf("popular-%s-%d.json", safeCacheKey(registryName), limit))

	cached, err := readPopularPackageCache(cachePath)
	if err == nil && time.Since(cached.FetchedAt) < popularCacheTTL {
		return cached.Packages, nil
	}

	names, fetchErr := s.fetchPopularPackages(ctx, registryName, limit)
	if fetchErr != nil {
		if err == nil && len(cached.Packages) > 0 {
			return cached.Packages, nil
		}
		return nil, fetchErr
	}
	if err := writePopularPackageCache(cachePath, cachedPopularPackages{FetchedAt: time.Now(), Packages: names}); err != nil {
		return nil, err
	}
	return names, nil
}

func (s *ecosystemsPopularPackageSource) fetchPopularPackages(ctx context.Context, registryName string, limit int) ([]string, error) {
	const pageSize = 100
	sortField := "downloads"
	order := "desc"
	var names []string

	for page := 1; len(names) < limit; page++ {
		perPage := minInt(pageSize, limit-len(names))
		resp, err := s.client.GetRegistryPackagesWithResponse(ctx, registryName, &ecosystems.GetRegistryPackagesParams{
			Page:    &page,
			PerPage: &perPage,
			Sort:    &sortField,
			Order:   &order,
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() == http.StatusNotFound {
			return names, nil
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("ecosyste.ms package lookup for %s returned %s", registryName, resp.Status())
		}
		if len(*resp.JSON200) == 0 {
			break
		}
		for _, pkg := range *resp.JSON200 {
			if pkg.Name != "" {
				names = append(names, pkg.Name)
			}
			if len(names) == limit {
				break
			}
		}
	}
	return names, nil
}

func readPopularPackageCache(path string) (cachedPopularPackages, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return cachedPopularPackages{}, err
	}
	var cached cachedPopularPackages
	if err := json.Unmarshal(raw, &cached); err != nil {
		return cachedPopularPackages{}, err
	}
	return cached, nil
}

func writePopularPackageCache(path string, cached cachedPopularPackages) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	raw, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0644)
}

func safeCacheKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func registryNameForEcosystem(ecosystem string) string {
	switch strings.ToLower(ecosystem) {
	case "golang":
		return "go"
	default:
		return strings.ToLower(ecosystem)
	}
}

func detectTyposquatting(ctx context.Context, deps []dependency, source popularPackageSource, limit int) ([]riskIssue, error) {
	byEcosystem := make(map[string][]dependency)
	for _, dep := range deps {
		byEcosystem[strings.ToLower(dep.Ecosystem)] = append(byEcosystem[strings.ToLower(dep.Ecosystem)], dep)
	}

	var issues []riskIssue
	for ecosystem, ecosystemDeps := range byEcosystem {
		popular, err := source.PopularPackages(ctx, ecosystem, limit)
		if err != nil {
			return nil, fmt.Errorf("fetch popular %s packages: %w", ecosystem, err)
		}
		for _, dep := range ecosystemDeps {
			match, reason := suspiciousNameMatch(dep.Name, popular)
			if match == "" {
				continue
			}
			issues = append(issues, riskIssue{
				Check:        checkTyposquat,
				Severity:     "medium",
				Name:         dep.Name,
				Ecosystem:    dep.Ecosystem,
				Version:      dep.Requirement,
				ManifestPath: dep.ManifestPath,
				Reason:       reason,
				SimilarTo:    match,
			})
		}
	}
	return issues, nil
}

func suspiciousNameMatch(name string, candidates []string) (string, string) {
	normalizedName := normalizePackageName(name)
	collapsedName := collapsePackageName(name)
	for _, candidate := range candidates {
		normalizedCandidate := normalizePackageName(candidate)
		if normalizedName == normalizedCandidate {
			continue
		}

		collapsedCandidate := collapsePackageName(candidate)
		if collapsedName == collapsedCandidate {
			return candidate, "package name differs only by delimiter, scope, or punctuation"
		}

		if editDistance(normalizedName, normalizedCandidate) <= typoDistanceThreshold(normalizedCandidate) {
			return candidate, "package name is very similar to a popular package"
		}

		if hasAdjacentTransposition(normalizedName, normalizedCandidate) {
			return candidate, "package name transposes adjacent characters from a popular package"
		}

		if isComboSquat(normalizedName, normalizedCandidate) {
			return candidate, "package name combines a popular package name with extra terms"
		}
	}
	return "", ""
}

func normalizePackageName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.TrimPrefix(name, "@")
}

func collapsePackageName(name string) string {
	name = normalizePackageName(name)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func typoDistanceThreshold(candidate string) int {
	if len(candidate) >= 8 {
		return 2
	}
	return 1
}

func hasAdjacentTransposition(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var mismatches []int
	for i := range a {
		if a[i] != b[i] {
			mismatches = append(mismatches, i)
		}
	}
	return len(mismatches) == 2 &&
		mismatches[1] == mismatches[0]+1 &&
		a[mismatches[0]] == b[mismatches[1]] &&
		a[mismatches[1]] == b[mismatches[0]]
}

func isComboSquat(name, candidate string) bool {
	if len(candidate) < 5 {
		return false
	}
	prefixes := []string{
		candidate + "-",
		candidate + "_",
		candidate + ".",
	}
	suffixes := []string{
		"-" + candidate,
		"_" + candidate,
		"." + candidate,
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = minInt(
				previous[j]+1,
				current[j-1]+1,
				previous[j-1]+cost,
			)
		}
		previous, current = current, previous
	}

	return previous[len(b)]
}

func minInt(values ...int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

type registryPublicLookup struct{}

func (registryPublicLookup) PublicPackageExists(ctx context.Context, dep dependency) (bool, string, error) {
	publicPURL := publicPURLForDependency(dep)
	if publicPURL == "" {
		return false, "", nil
	}
	_, err := registries.FetchPackageFromPURL(ctx, publicPURL, nil)
	if err == nil {
		return true, publicPURL, nil
	}
	if errors.Is(err, registries.ErrNotFound) {
		return false, publicPURL, nil
	}
	return false, publicPURL, err
}

func detectDependencyConfusion(ctx context.Context, deps []dependency, lookup publicRegistryLookup) ([]riskIssue, error) {
	var issues []riskIssue
	for _, dep := range deps {
		registryURL := privateRegistryURL(dep)
		if registryURL == "" {
			continue
		}
		if strings.EqualFold(dep.Ecosystem, "npm") && strings.HasPrefix(dep.Name, "@") {
			continue
		}

		exists, publicPURL, err := lookup.PublicPackageExists(ctx, dep)
		if err != nil {
			return nil, fmt.Errorf("lookup public package %s: %w", dep.Name, err)
		}
		if !exists {
			continue
		}

		issues = append(issues, riskIssue{
			Check:        checkConfusion,
			Severity:     "high",
			Name:         dep.Name,
			Ecosystem:    dep.Ecosystem,
			Version:      dep.Requirement,
			ManifestPath: dep.ManifestPath,
			Reason:       "private-registry dependency name also exists on the public registry",
			Evidence:     fmt.Sprintf("private registry: %s; public package: %s", registryURL, publicPURL),
		})
	}
	return issues, nil
}

func publicPURLForDependency(dep dependency) string {
	if dep.Ecosystem == "" || dep.Name == "" {
		return ""
	}
	return purl.MakePURLString(dep.Ecosystem, dep.Name, "")
}

func privateRegistryURL(dep dependency) string {
	if dep.PURL == "" {
		return ""
	}
	parsed, err := purl.Parse(dep.PURL)
	if err != nil {
		return ""
	}
	registryURL := parsed.RepositoryURL()
	if registryURL == "" {
		return ""
	}
	if !parsed.IsPrivateRegistry() {
		return ""
	}
	return registryURL
}

type httpNPMManifestFetcher struct {
	client  *http.Client
	baseURL string
}

type npmRegistryManifest struct {
	Scripts map[string]string `json:"scripts"`
}

func (f *httpNPMManifestFetcher) NPMScripts(ctx context.Context, name, version string) (map[string]string, error) {
	if f.client == nil {
		f.client = http.DefaultClient
	}
	endpoint := strings.TrimRight(f.baseURL, "/") + "/" + url.PathEscape(name) + "/" + npmVersionPath(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errPackageNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("npm registry returned %s", resp.Status)
	}

	var manifest npmRegistryManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return manifest.Scripts, nil
}

func npmVersionPath(version string) string {
	if isExactVersion(version) {
		return url.PathEscape(version)
	}
	return "latest"
}

func isExactVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	return !strings.ContainsAny(version, "^~<>=*xX, []{}|")
}

func detectInstallScripts(ctx context.Context, deps []dependency, fetcher npmManifestFetcher) ([]riskIssue, error) {
	lockfiles := make(map[string]*npmLockfile)
	var issues []riskIssue
	for _, dep := range deps {
		if evidence := localInstallScriptEvidence(dep); evidence != "" {
			issues = append(issues, installScriptIssue(dep, evidence))
			continue
		}

		if !strings.EqualFold(dep.Ecosystem, "npm") {
			continue
		}

		evidence, err := npmInstallScriptEvidence(ctx, dep, fetcher, lockfiles)
		if err != nil {
			return nil, err
		}
		if evidence == "" {
			continue
		}

		issues = append(issues, installScriptIssue(dep, evidence))
	}
	return issues, nil
}

func npmInstallScriptEvidence(ctx context.Context, dep dependency, fetcher npmManifestFetcher, lockfiles map[string]*npmLockfile) (string, error) {
	if isNpmLockfile(dep.ManifestPath) {
		lockfile := lockfiles[dep.ManifestPath]
		if lockfile == nil {
			loaded, err := loadNpmLockfile(dep.ManifestPath)
			if err == nil {
				lockfiles[dep.ManifestPath] = loaded
				lockfile = loaded
			}
		}
		if evidence := lockfile.installScriptEvidence(dep.Name); evidence != "" {
			return evidence, nil
		}
	}

	scripts, err := fetcher.NPMScripts(ctx, dep.Name, dep.Requirement)
	if errors.Is(err, errPackageNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("fetch npm manifest %s: %w", dep.Name, err)
	}
	return npmLifecycleEvidence("registry manifest", scripts), nil
}

func installScriptIssue(dep dependency, evidence string) riskIssue {
	return riskIssue{
		Check:        checkInstallScripts,
		Severity:     "high",
		Name:         dep.Name,
		Ecosystem:    dep.Ecosystem,
		Version:      dep.Requirement,
		ManifestPath: dep.ManifestPath,
		Reason:       "package can run code during installation or build",
		Evidence:     evidence,
	}
}

func localInstallScriptEvidence(dep dependency) string {
	base := strings.ToLower(filepath.Base(dep.ManifestPath))
	switch strings.ToLower(dep.Ecosystem) {
	case "pypi":
		if base == "setup.py" {
			return "setup.py executes during package build/install"
		}
	case "rubygems":
		if base == "extconf.rb" {
			return "extconf.rb builds native extensions during gem install"
		}
		extconfPath := filepath.Join(filepath.Dir(dep.ManifestPath), "extconf.rb")
		if fileExists(extconfPath) {
			return "extconf.rb found next to the Ruby manifest"
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isNpmLockfile(path string) bool {
	base := filepath.Base(path)
	return base == "package-lock.json" || base == "npm-shrinkwrap.json"
}

type npmLockfile struct {
	Packages map[string]npmLockPackage `json:"packages"`
}

type npmLockPackage struct {
	HasInstallScript bool              `json:"hasInstallScript"`
	Scripts          map[string]string `json:"scripts"`
}

func loadNpmLockfile(path string) (*npmLockfile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lockfile npmLockfile
	if err := json.Unmarshal(raw, &lockfile); err != nil {
		return nil, err
	}
	return &lockfile, nil
}

func (l *npmLockfile) installScriptEvidence(name string) string {
	if l == nil {
		return ""
	}
	entry := l.Packages["node_modules/"+name]
	if !entry.HasInstallScript && len(entry.Scripts) == 0 {
		return ""
	}
	if entry.HasInstallScript {
		return "package-lock hasInstallScript=true"
	}
	return npmLifecycleEvidence("package-lock", entry.Scripts)
}

func npmLifecycleEvidence(source string, scripts map[string]string) string {
	var hooks []string
	for _, hook := range []string{"preinstall", "install", "postinstall", "prepare"} {
		if scripts[hook] != "" {
			hooks = append(hooks, hook)
		}
	}
	if len(hooks) == 0 {
		return ""
	}
	return source + " lifecycle scripts: " + strings.Join(hooks, ", ")
}

func outputText(w io.Writer, result riskResult) {
	if len(result.Issues) == 0 {
		_, _ = fmt.Fprintln(w, "No supply chain risk signals found.")
		return
	}

	_, _ = fmt.Fprintf(w, "Found %d supply chain risk signals:\n\n", len(result.Issues))
	for _, issue := range result.Issues {
		var line bytes.Buffer
		_, _ = fmt.Fprintf(&line, "%s (%s): %s [%s]", issue.Name, issue.Ecosystem, issue.Check, issue.Severity)
		if issue.SimilarTo != "" {
			_, _ = fmt.Fprintf(&line, " similar to %s", issue.SimilarTo)
		}
		_, _ = fmt.Fprintln(w, line.String())
		_, _ = fmt.Fprintf(w, "  %s\n", issue.Reason)
		if issue.Evidence != "" {
			_, _ = fmt.Fprintf(w, "  evidence: %s\n", issue.Evidence)
		}
	}
}
