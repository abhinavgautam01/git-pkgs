package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/git-pkgs/managers"
	"github.com/spf13/cobra"
)

const defaultReplaceTimeout = 5 * time.Minute

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

	manager, err := createManager(dir, opts.Manager)
	if err != nil {
		return err
	}

	if !opts.Quiet {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Detected: %s", mgr.Name)
		if mgr.Lockfile != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " (%s)", mgr.Lockfile)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}

	return executeReplace(cmd, dir, manager, opts)
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

func executeReplace(cmd *cobra.Command, dir string, mgr managers.Manager, opts replaceOptions) error {
	switch opts.Mode {
	case replaceModeVersion:
		return executeReplaceVersion(cmd, dir, opts)
	case replaceModePath, replaceModeGit, replaceModeDrop:
		return executeReplaceSource(cmd, dir, mgr, opts)
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

func executeReplaceSource(cmd *cobra.Command, dir string, mgr managers.Manager, opts replaceOptions) error {
	if opts.DryRun {
		return dryRunReplaceSource(cmd, dir, opts)
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	result, err := mgr.Replace(ctx, opts.Package, managerReplaceOptions(opts))
	if err != nil {
		return fmt.Errorf("replace failed: %w", err)
	}
	return outputReplaceResult(cmd, result)
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

func dryRunReplaceSource(cmd *cobra.Command, dir string, opts replaceOptions) error {
	if manifestPath := replaceManifestPath(dir, opts.Manager); manifestPath != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would edit: %s\n", manifestPath)
		return nil
	}

	mockRunner := managers.NewMockRunner()
	mgr, err := createManagerWithRunner(dir, opts.Manager, mockRunner)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	if _, err := mgr.Replace(ctx, opts.Package, managerReplaceOptions(opts)); err != nil {
		return fmt.Errorf("building replace command: %w", err)
	}
	for _, c := range mockRunner.Captured {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would run: %v\n", c)
	}
	return nil
}

func outputReplaceResult(cmd *cobra.Command, result *managers.Result) error {
	if result == nil {
		return nil
	}
	if result.Stdout != "" {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		}
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("replace failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func managerReplaceOptions(opts replaceOptions) managers.ReplaceOptions {
	return managers.ReplaceOptions{
		Path:  opts.Path,
		Git:   opts.Git,
		Ref:   opts.Ref,
		Drop:  opts.Mode == replaceModeDrop,
		Extra: opts.Extra,
	}
}

func replaceManifestPath(dir, managerName string) string {
	switch managerName {
	case "cargo":
		return filepath.Join(dir, "Cargo.toml")
	case "uv":
		return filepath.Join(dir, "pyproject.toml")
	case "bundler":
		return filepath.Join(dir, "Gemfile")
	default:
		return ""
	}
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
