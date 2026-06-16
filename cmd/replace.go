package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/git-pkgs/managers"
	"github.com/spf13/cobra"
)

const defaultReplaceTimeout = 5 * time.Minute

var regexpGoModuleVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

type replaceMode string

const (
	replaceModeVersion replaceMode = "version"
	replaceModePath    replaceMode = "path"
	replaceModeGit     replaceMode = "git"
	replaceModeDrop    replaceMode = "drop"
)

type replaceOptions struct {
	Package   string
	Version   string
	Path      string
	Git       string
	Ref       string
	Mode      replaceMode
	DryRun    bool
	Quiet     bool
	Extra     []string
	Timeout   time.Duration
	Manager   string
	Ecosystem string
}

type replaceManagerOperation struct {
	Operation string
	Input     managers.CommandInput
}

func addReplaceCmd(parent *cobra.Command) {
	replaceCmd := &cobra.Command{
		Use:   "replace <package> [version]",
		Short: "Redirect a dependency to a local path, git ref, or version",
		Long: `Redirect a dependency to an alternative source for downstream testing.
Use --path for a local checkout, --git with an optional --ref for a git source,
or pass a version to fall through to the package manager's add operation.

Examples:
  git-pkgs replace github.com/acme/lib --path ../lib
  git-pkgs replace lodash --git https://github.com/fork/lodash --ref fix-branch
  git-pkgs replace lodash 4.17.21
  git-pkgs replace github.com/acme/lib --drop`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runReplace,
	}

	replaceCmd.Flags().String("path", "", "Redirect dependency to a local path")
	replaceCmd.Flags().String("git", "", "Redirect dependency to a git repository")
	replaceCmd.Flags().String("ref", "", "Git branch, tag, or revision for --git")
	replaceCmd.Flags().Bool("drop", false, "Remove an existing replacement")
	replaceCmd.Flags().StringP("manager", "m", "", "Override detected package manager (takes precedence over -e)")
	replaceCmd.Flags().StringP("ecosystem", "e", "", "Filter to specific ecosystem")
	replaceCmd.Flags().Bool("dry-run", false, "Show what would be run or edited without executing")
	replaceCmd.Flags().StringArrayP("extra", "x", nil, "Extra arguments to pass to package manager")
	replaceCmd.Flags().DurationP("timeout", "t", defaultReplaceTimeout, "Timeout for replace operation")
	parent.AddCommand(replaceCmd)
}

func runReplace(cmd *cobra.Command, args []string) error {
	managerOverride, _ := cmd.Flags().GetString("manager")
	ecosystemFlag, _ := cmd.Flags().GetString("ecosystem")
	ecosystem, pkg, purlVersion, err := ParsePackageArg(args[0], ecosystemFlag)
	if err != nil {
		return err
	}

	opts := replaceOptions{
		Package:   pkg,
		Path:      flagString(cmd, "path"),
		Git:       flagString(cmd, "git"),
		Ref:       flagString(cmd, "ref"),
		DryRun:    flagBool(cmd, "dry-run"),
		Quiet:     flagBool(cmd, "quiet"),
		Extra:     flagStringArray(cmd, "extra"),
		Timeout:   flagDuration(cmd, "timeout"),
		Manager:   managerOverride,
		Ecosystem: ecosystem,
	}
	if len(args) > 1 {
		opts.Version = args[1]
	} else {
		opts.Version = purlVersion
	}
	if flagBool(cmd, "drop") {
		opts.Mode = replaceModeDrop
	}

	if err := validateReplaceOptions(&opts); err != nil {
		return err
	}

	dir, err := getWorkingDir()
	if err != nil {
		return err
	}

	mgr, err := selectManagerForReplace(dir, opts.Manager, opts.Ecosystem, cmd)
	if err != nil {
		return err
	}
	opts.Manager = mgr.Name
	if err := validateReplaceManagerOptions(opts); err != nil {
		return err
	}

	if !opts.Quiet {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Detected: %s", mgr.Name)
		if mgr.Lockfile != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " (%s)", mgr.Lockfile)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}

	return executeReplace(cmd, dir, opts)
}

func validateReplaceOptions(opts *replaceOptions) error {
	modes := 0
	if opts.Path != "" {
		opts.Mode = replaceModePath
		modes++
	}
	if opts.Git != "" {
		opts.Mode = replaceModeGit
		modes++
	}
	if opts.Mode == replaceModeDrop {
		modes++
	}
	if opts.Version != "" {
		if modes > 0 {
			return errors.New("specify only one replacement mode: [version], --path, --git, or --drop")
		}
		opts.Mode = replaceModeVersion
		modes++
	}
	if modes == 0 {
		return errors.New("specify one of [version], --path, --git, or --drop")
	}
	if modes > 1 {
		return errors.New("specify only one replacement mode: [version], --path, --git, or --drop")
	}
	if opts.Ref != "" && opts.Mode != replaceModeGit {
		return errors.New("--ref can only be used with --git")
	}
	return nil
}

func validateReplaceManagerOptions(opts replaceOptions) error {
	if opts.Manager == "gomod" && opts.Mode == replaceModeGit && opts.Ref != "" && !isGoModuleVersion(opts.Ref) {
		return fmt.Errorf("gomod --ref must be a Go module version or pseudo-version, got %q", opts.Ref)
	}
	if editsCargoManifest(opts.Manager) || editsUVManifest(opts.Manager) || opts.Manager == "bundler" || opts.Mode == replaceModeVersion {
		return nil
	}

	capability := replaceCapabilityForMode(opts.Mode)
	if capability == "" {
		return nil
	}
	supported, err := managerSupportsCapability(opts.Manager, capability)
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("%s does not support %s replacements", opts.Manager, opts.Mode)
	}
	return nil
}

func replaceCapabilityForMode(mode replaceMode) string {
	switch mode {
	case replaceModePath:
		return "replace_path"
	case replaceModeGit:
		return "replace_git"
	case replaceModeDrop:
		return "replace_drop"
	default:
		return ""
	}
}

func selectManagerForReplace(dir, managerOverride, ecosystem string, cmd *cobra.Command) (*DetectedManager, error) {
	if managerOverride != "" {
		return &DetectedManager{Name: managerOverride}, nil
	}
	detected, err := DetectManagers(dir)
	if err != nil {
		return nil, fmt.Errorf("detecting package managers: %w", err)
	}
	if ecosystem != "" {
		detected = FilterByEcosystem(detected, ecosystem)
		if len(detected) == 0 {
			return nil, fmt.Errorf("no %s package manager detected", ecosystem)
		}
	}
	mgr, err := PromptForManager(detected, cmd.OutOrStdout(), os.Stdin)
	if err != nil {
		return nil, err
	}
	return mgr, nil
}

func executeReplace(cmd *cobra.Command, dir string, opts replaceOptions) error {
	switch opts.Mode {
	case replaceModeVersion:
		return executeReplaceVersion(cmd, dir, opts)
	case replaceModePath, replaceModeGit, replaceModeDrop:
		return executeReplaceSource(cmd, dir, opts)
	default:
		return fmt.Errorf("unsupported replacement mode %q", opts.Mode)
	}
}

func executeReplaceVersion(cmd *cobra.Command, dir string, opts replaceOptions) error {
	op := replaceManagerOperation{
		Operation: "add",
		Input: managers.CommandInput{
			Args: map[string]string{
				"package": opts.Package,
				"version": opts.Version,
			},
			Flags: map[string]any{},
			Extra: opts.Extra,
		},
	}

	return executeReplaceManagerOperations(cmd, dir, opts, []replaceManagerOperation{op})
}

func executeReplaceSource(cmd *cobra.Command, dir string, opts replaceOptions) error {
	if editsCargoManifest(opts.Manager) {
		return replaceInCargoManifest(cmd, filepath.Join(dir, "Cargo.toml"), opts)
	}
	if editsUVManifest(opts.Manager) {
		return replaceInUVManifest(cmd, filepath.Join(dir, "pyproject.toml"), opts)
	}
	if opts.Manager == "bundler" {
		return replaceInGemfile(cmd, filepath.Join(dir, "Gemfile"), opts)
	}

	ops, err := buildReplaceManagerOperations(opts)
	if err != nil {
		return err
	}
	return executeReplaceManagerOperations(cmd, dir, opts, ops)
}

func executeReplaceManagerOperations(
	cmd *cobra.Command,
	dir string,
	opts replaceOptions,
	ops []replaceManagerOperation,
) error {
	var builtCmds [][]string
	for _, op := range ops {
		cmds, err := BuildCommands(opts.Manager, op.Operation, op.Input)
		if err != nil {
			return fmt.Errorf("building command: %w", err)
		}
		builtCmds = append(builtCmds, cmds...)
	}

	if opts.DryRun {
		for _, c := range builtCmds {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would run: %v\n", c)
		}
		return nil
	}
	if !opts.Quiet {
		for _, c := range builtCmds {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Running: %v\n", c)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	for _, op := range ops {
		if err := RunManagerCommands(ctx, dir, opts.Manager, op.Operation, op.Input, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return fmt.Errorf("replace failed: %w", err)
		}
	}
	return nil
}

func buildReplaceManagerOperations(opts replaceOptions) ([]replaceManagerOperation, error) {
	input := managers.CommandInput{
		Args:  map[string]string{},
		Flags: map[string]any{},
		Extra: opts.Extra,
	}

	switch opts.Manager {
	case "gomod":
		if err := populateGoReplaceInput(opts, &input); err != nil {
			return nil, err
		}
	case ecosystemNPM, "pnpm", "yarn", "bun":
		if err := populateNPMReplaceInput(opts, &input); err != nil {
			return nil, err
		}
	case "composer":
		return buildComposerReplaceOperations(opts, input)
	default:
		return nil, fmt.Errorf("%s does not support replace for %s", opts.Manager, opts.Mode)
	}

	return []replaceManagerOperation{{Operation: "replace", Input: input}}, nil
}

func populateGoReplaceInput(opts replaceOptions, input *managers.CommandInput) error {
	switch opts.Mode {
	case replaceModePath:
		input.Args["replacement"] = opts.Package + "=" + opts.Path
	case replaceModeGit:
		target, err := normalizeGoReplaceTarget(opts.Git)
		if err != nil {
			return err
		}
		if opts.Ref != "" {
			target += "@" + opts.Ref
		}
		input.Args["replacement"] = opts.Package + "=" + target
	case replaceModeDrop:
		input.Args["package"] = opts.Package
		input.Flags["drop"] = true
	default:
		return fmt.Errorf("gomod does not support replace for %s", opts.Mode)
	}
	return nil
}

func populateNPMReplaceInput(opts replaceOptions, input *managers.CommandInput) error {
	switch opts.Mode {
	case replaceModePath:
		input.Args["package"] = opts.Package + "@file:" + opts.Path
	case replaceModeGit:
		input.Args["package"] = opts.Package + "@" + npmGitSpec(opts.Git, opts.Ref)
	case replaceModeDrop:
		return fmt.Errorf("%s cannot safely drop a file/git replacement without the original version; use 'git-pkgs replace %s <version>' to restore a registry version", opts.Manager, opts.Package)
	default:
		return fmt.Errorf("%s does not support replace for %s", opts.Manager, opts.Mode)
	}
	return nil
}

func buildComposerReplaceOperations(
	opts replaceOptions,
	input managers.CommandInput,
) ([]replaceManagerOperation, error) {
	repoName := composerRepositoryName(opts.Package)
	input.Args["repository"] = "repositories." + repoName
	switch opts.Mode {
	case replaceModePath:
		input.Args["payload"] = fmt.Sprintf(`{"type":"path","url":%s}`, quoteJSONString(opts.Path))
	case replaceModeGit:
		input.Args["payload"] = fmt.Sprintf(`{"type":"vcs","url":%s}`, quoteJSONString(opts.Git))
		ops := []replaceManagerOperation{{Operation: "replace", Input: input}}
		if opts.Ref != "" {
			requireInput := managers.CommandInput{
				Args:  map[string]string{"package": opts.Package + ":dev-" + opts.Ref},
				Flags: map[string]any{},
				Extra: opts.Extra,
			}
			ops[0].Input.Extra = nil
			ops = append(ops, replaceManagerOperation{Operation: "add", Input: requireInput})
		}
		return ops, nil
	case replaceModeDrop:
		input.Flags["drop"] = true
	default:
		return nil, fmt.Errorf("composer does not support replace for %s", opts.Mode)
	}
	return []replaceManagerOperation{{Operation: "replace", Input: input}}, nil
}

func npmGitSpec(repo, ref string) string {
	spec := repo
	if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
		spec = "git+" + spec
	}
	if ref != "" {
		spec += "#" + ref
	}
	return spec
}

func normalizeGoReplaceTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("go replacement target cannot be empty")
	}

	if strings.HasPrefix(target, "git@") {
		withoutPrefix := strings.TrimPrefix(target, "git@")
		host, path, ok := strings.Cut(withoutPrefix, ":")
		if !ok || host == "" || path == "" {
			return "", fmt.Errorf("invalid go replacement git target %q", target)
		}
		return strings.TrimSuffix(host+"/"+strings.Trim(path, "/"), ".git"), nil
	}

	if strings.Contains(target, "://") {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" || parsed.Path == "" {
			return "", fmt.Errorf("invalid go replacement git target %q", target)
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh" && parsed.Scheme != "git" {
			return "", fmt.Errorf("unsupported go replacement git URL scheme %q", parsed.Scheme)
		}
		return strings.TrimSuffix(parsed.Host+"/"+strings.Trim(parsed.Path, "/"), ".git"), nil
	}

	if strings.HasPrefix(target, "git+") {
		return "", fmt.Errorf("unsupported go replacement git target %q; use a module path or URL", target)
	}
	return strings.TrimSuffix(target, ".git"), nil
}

func isGoModuleVersion(ref string) bool {
	return regexpGoModuleVersion.MatchString(ref)
}

func composerRepositoryName(pkg string) string {
	replacer := strings.NewReplacer("/", "-", "_", "-", ".", "-")
	return "git-pkgs-" + replacer.Replace(pkg)
}

func flagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func flagStringArray(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringArray(name)
	return v
}

func flagBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func flagDuration(cmd *cobra.Command, name string) time.Duration {
	v, _ := cmd.Flags().GetDuration(name)
	return v
}
