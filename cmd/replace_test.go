package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceDryRun(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		args    []string
		wantOut string
	}{
		{
			name: "npm path replacement",
			files: map[string]string{
				"package.json":      `{"dependencies":{"lodash":"^4.17.0"}}`,
				"package-lock.json": `{}`,
			},
			args:    []string{"replace", "lodash", "--path", "../lodash", "--dry-run"},
			wantOut: "Would run: [npm install lodash@file:../lodash]",
		},
		{
			name: "pnpm git replacement with ref",
			files: map[string]string{
				"package.json":   `{"dependencies":{"lodash":"^4.17.0"}}`,
				"pnpm-lock.yaml": "",
			},
			args:    []string{"replace", "lodash", "--git", "https://github.com/fork/lodash", "--ref", "fix-branch", "--dry-run"},
			wantOut: "Would run: [pnpm add lodash@git+https://github.com/fork/lodash#fix-branch]",
		},
		{
			name: "version falls through to add",
			files: map[string]string{
				"package.json":      `{"dependencies":{"lodash":"^4.17.0"}}`,
				"package-lock.json": `{}`,
			},
			args:    []string{"replace", "lodash", "4.17.21", "--dry-run"},
			wantOut: "Would run: [npm install lodash@4.17.21]",
		},
		{
			name: "go drop replacement",
			files: map[string]string{
				"go.mod": "module example.test/app\n\ngo 1.22\n",
			},
			args:    []string{"replace", "example.test/lib", "--drop", "--dry-run"},
			wantOut: "Would run: [go mod edit -dropreplace example.test/lib]",
		},
		{
			name: "composer path replacement",
			files: map[string]string{
				"composer.json": `{"require":{"vendor/pkg":"^1.0"}}`,
			},
			args:    []string{"replace", "vendor/pkg", "--path", "../pkg", "--dry-run"},
			wantOut: `Would run: [composer config repositories.git-pkgs-vendor-pkg {"type":"path","url":"../pkg"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			cleanup := chdirReplaceTest(t, tmpDir)
			defer cleanup()

			var stdout, stderr bytes.Buffer
			root := NewRootCmd()
			root.SetArgs(tt.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			if err := root.Execute(); err != nil {
				t.Fatalf("replace failed: %v\nstderr: %s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Fatalf("output = %q, want to contain %q", stdout.String(), tt.wantOut)
			}
		})
	}
}

func TestReplaceValidation(t *testing.T) {
	tests := []struct {
		name    string
		opts    replaceOptions
		wantErr string
	}{
		{
			name:    "requires mode",
			opts:    replaceOptions{},
			wantErr: "specify one of",
		},
		{
			name: "rejects multiple modes",
			opts: replaceOptions{
				Version: "1.0.0",
				Path:    "../pkg",
			},
			wantErr: "specify only one",
		},
		{
			name: "ref requires git",
			opts: replaceOptions{
				Path: "../pkg",
				Ref:  "main",
			},
			wantErr: "--ref can only be used with --git",
		},
		{
			name: "rejects drop with version",
			opts: replaceOptions{
				Version: "1.0.0",
				Mode:    replaceModeDrop,
			},
			wantErr: "specify only one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReplaceOptions(&tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestGoReplaceGitTargetNormalizesURLs(t *testing.T) {
	ops, err := buildReplaceManagerOperations(replaceOptions{
		Package: "example.test/lib",
		Git:     "https://github.com/fork/lib.git",
		Ref:     "v1.2.3",
		Mode:    replaceModeGit,
		Manager: "gomod",
	})
	if err != nil {
		t.Fatalf("build go replace operations: %v", err)
	}
	want := "example.test/lib=github.com/fork/lib@v1.2.3"
	if len(ops) != 1 || ops[0].Input.Args["replacement"] != want {
		t.Fatalf("operations = %#v, want replacement %q", ops, want)
	}
}

func TestGoReplaceGitRefRequiresVersion(t *testing.T) {
	err := validateReplaceManagerOptions(replaceOptions{
		Package: "example.test/lib",
		Git:     "https://github.com/fork/lib.git",
		Ref:     "feature-branch",
		Mode:    replaceModeGit,
		Manager: "gomod",
	})
	if err == nil || !strings.Contains(err.Error(), "must be a Go module version") {
		t.Fatalf("error = %v, want Go module version error", err)
	}
}

func TestReplaceCargoManifest(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := filepath.Join(tmpDir, "Cargo.toml")
	if err := os.WriteFile(manifest, []byte("[package]\nname = \"app\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup := chdirReplaceTest(t, tmpDir)
	defer cleanup()

	root := NewRootCmd()
	root.SetArgs([]string{"replace", "serde", "--path", "../serde", "-m", "cargo"})
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	if err := root.Execute(); err != nil {
		t.Fatalf("replace cargo path: %v", err)
	}

	content := readReplaceTestFile(t, manifest)
	if !strings.Contains(content, "[patch.crates-io]\nserde = { path = \"../serde\" }\n") {
		t.Fatalf("Cargo.toml missing patch entry:\n%s", content)
	}

	root = NewRootCmd()
	root.SetArgs([]string{"replace", "serde", "--drop", "-m", "cargo"})
	root.SetOut(&stdout)
	if err := root.Execute(); err != nil {
		t.Fatalf("replace cargo drop: %v", err)
	}
	content = readReplaceTestFile(t, manifest)
	if strings.Contains(content, "serde =") {
		t.Fatalf("Cargo.toml still contains serde patch:\n%s", content)
	}
}

func TestReplaceCargoManifestUsesBranchForNamedRef(t *testing.T) {
	content, err := updateTOMLSectionEntry("[patch.crates-io] # local overrides\n", "[patch.crates-io]", "serde", `{ git = "https://github.com/fork/serde", branch = "main" }`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `[patch.crates-io] # local overrides`) {
		t.Fatalf("Cargo.toml did not preserve commented header:\n%s", content)
	}
	if !strings.Contains(content, `serde = { git = "https://github.com/fork/serde", branch = "main" }`) {
		t.Fatalf("Cargo.toml missing branch replacement:\n%s", content)
	}
}

func TestReplaceUVManifest(t *testing.T) {
	content, err := updateTOMLSectionEntry("[project]\nname = \"app\"\n", "[tool.uv.sources]", "demo-pkg", `{ path = "../demo", editable = true }`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "[tool.uv.sources]\ndemo-pkg = { path = \"../demo\", editable = true }\n") {
		t.Fatalf("pyproject content missing uv source:\n%s", content)
	}

	content, err = updateTOMLSectionEntry(content, "[tool.uv.sources]", "demo-pkg", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "demo-pkg =") {
		t.Fatalf("pyproject content still contains uv source:\n%s", content)
	}
	if strings.Contains(content, "[tool.uv.sources]") {
		t.Fatalf("pyproject content still contains empty uv section:\n%s", content)
	}
}

func TestReplaceGemfile(t *testing.T) {
	tmpDir := t.TempDir()
	gemfile := filepath.Join(tmpDir, "Gemfile")
	if err := os.WriteFile(gemfile, []byte("source \"https://rubygems.org\"\ngem \"rails\", \"~> 7.0\", require: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup := chdirReplaceTest(t, tmpDir)
	defer cleanup()

	root := NewRootCmd()
	root.SetArgs([]string{"replace", "rails", "--git", "https://github.com/fork/rails", "--ref", "main", "-m", "bundler"})
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	if err := root.Execute(); err != nil {
		t.Fatalf("replace gem: %v", err)
	}

	content := readReplaceTestFile(t, gemfile)
	if !strings.Contains(content, `gem "rails", "~> 7.0", require: false, git: "https://github.com/fork/rails", branch: "main"`) {
		t.Fatalf("Gemfile missing git replacement:\n%s", content)
	}

	root = NewRootCmd()
	root.SetArgs([]string{"replace", "rails", "--drop", "-m", "bundler"})
	root.SetOut(&stdout)
	if err := root.Execute(); err != nil {
		t.Fatalf("drop gem replacement: %v", err)
	}

	content = readReplaceTestFile(t, gemfile)
	if !strings.Contains(content, `gem "rails", "~> 7.0", require: false`) {
		t.Fatalf("Gemfile did not preserve original args after drop:\n%s", content)
	}
	if strings.Contains(content, `git:`) || strings.Contains(content, `branch:`) {
		t.Fatalf("Gemfile still contains replacement args after drop:\n%s", content)
	}
}

func TestReplaceGemfileFiltersShorthandAndHashRocketSources(t *testing.T) {
	line := `gem "rails", "~> 7.0", github: "rails/rails", :branch => "main", require: false` + "\n"
	got, err := updateGemfileLine(line, replaceOptions{
		Package: "rails",
		Git:     "https://github.com/fork/rails",
		Ref:     "feature",
		Mode:    replaceModeGit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "github:") || strings.Contains(got, ":branch =>") {
		t.Fatalf("Gemfile replacement retained old source args: %s", got)
	}
	want := `gem "rails", "~> 7.0", require: false, git: "https://github.com/fork/rails", branch: "feature"`
	if !strings.Contains(got, want) {
		t.Fatalf("Gemfile line = %q, want containing %q", got, want)
	}
}

func TestNPMDropReplacementRequiresVersion(t *testing.T) {
	_, err := buildReplaceManagerOperations(replaceOptions{
		Package: "lodash",
		Mode:    replaceModeDrop,
		Manager: "npm",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot safely drop") {
		t.Fatalf("error = %v, want cannot safely drop", err)
	}
}

func TestComposerGitRefExtrasOnlyApplyToRequire(t *testing.T) {
	ops, err := buildReplaceManagerOperations(replaceOptions{
		Package: "vendor/pkg",
		Git:     "https://github.com/fork/pkg",
		Ref:     "feature",
		Mode:    replaceModeGit,
		Manager: "composer",
		Extra:   []string{"--no-update"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("operations length = %d, want 2", len(ops))
	}
	if len(ops[0].Input.Extra) != 0 {
		t.Fatalf("config operation extra = %#v, want none", ops[0].Input.Extra)
	}
	if got := ops[1].Input.Extra; len(got) != 1 || got[0] != "--no-update" {
		t.Fatalf("require operation extra = %#v, want --no-update", got)
	}
}

func chdirReplaceTest(t *testing.T, dir string) func() {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}
}

func readReplaceTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
