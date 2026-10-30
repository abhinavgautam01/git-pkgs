package cmd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
)

const swiftRegistryPackageResolved = `{
  "pins": [
    {
	  "identity": "apple.swift-argument-parser",
	  "kind": "registry",
	  "location": "apple.swift-argument-parser",
      "state": {
        "revision": "19b3c3ceed117c5cc883517c4e658548315ba70b",
        "version": "1.6.0"
      }
    }
  ],
  "version": 2
}`

const swiftSourcePackageResolved = `{
  "pins": [
    {
      "identity": "cryptoswift",
      "kind": "remoteSourceControl",
      "location": "https://github.com/krzyzanowskim/CryptoSwift.git",
      "state": {
        "revision": "19b3c3ceed117c5cc883517c4e658548315ba70b",
        "version": "1.6.0"
      }
    }
  ],
  "version": 2
}`

func TestVulnsSwiftMixedIdentities(t *testing.T) {
	const lockfile = `{"version": 3, "pins": [
		{"identity":"cryptoswift", "kind":"remoteSourceControl",
		 "location":"https://github.com/krzyzanowskim/CryptoSwift.git",
		 "state":{"revision":"19b3c3ceed117c5cc883517c4e658548315ba70b", "version":"1.6.0"}},
		{"identity":"apple.swift-argument-parser", "kind":"registry", "location":"",
		 "state":{"version":"1.6.0"}}
	]}`
	for _, args := range [][]string{{"scan", "--live"}, {"scan"}, {"sync"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := createTestRepo(t)
			addFileAndCommit(t, dir, "Package.resolved", lockfile, "Add Swift dependencies")
			t.Cleanup(chdir(t, dir))
			if _, _, err := runCmd(t, "init", "--no-hooks"); err != nil {
				t.Fatal(err)
			}
			transport := &swiftOSVTransport{}
			original := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = original })
			stdout, stderr, err := runCmd(t, append([]string{"vulns"}, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			want := `Skipping 1 dependency with a package identity that cannot be queried in OSV: swift package "apple.swift-argument-parser" (Package.resolved).`
			if !strings.Contains(stdout+stderr, want) {
				t.Fatalf("missing skip notice: stdout=%q stderr=%q", stdout, stderr)
			}
			if transport.queries != 1 {
				t.Fatalf("queried %d packages, want one source dependency", transport.queries)
			}
		})
	}
}

type swiftOSVTransport struct{ queries int }

func (s *swiftOSVTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.osv.dev" || req.URL.Path != "/v1/querybatch" {
		return nil, fmt.Errorf("unexpected request: %s", req.URL)
	}
	var body struct {
		Queries []struct {
			Package struct{ Ecosystem, Name string }
		}
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Queries) != 1 || body.Queries[0].Package.Ecosystem != "SwiftURL" ||
		body.Queries[0].Package.Name != "github.com/krzyzanowskim/CryptoSwift" {
		return nil, fmt.Errorf("unexpected OSV queries: %+v", body.Queries)
	}
	s.queries += len(body.Queries)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"results":[{}]}`)),
		Request:    req,
	}, nil
}

func TestVulnsHistoryRejectsUnsupportedPackageIdentity(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "Package.resolved", swiftRegistryPackageResolved, "Add Swift lockfile")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	if _, _, err := runCmd(t, "init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	_, _, err := runCmd(t, "vulns", "history", "apple.swift-argument-parser", "--format", "json")
	if err == nil {
		t.Fatal("vulns history error = nil, want unsupported package identity error")
	}

	want := `querying OSV for swift package "apple.swift-argument-parser": package identity cannot be queried in OSV`
	if err.Error() != want {
		t.Fatalf("vulns history error = %q, want %q", err, want)
	}
}

func TestVulnsScanReportsUnsupportedPackageIdentity(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "Package.resolved", swiftRegistryPackageResolved, "Add Swift lockfile")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	if _, _, err := runCmd(t, "init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	_, stderr, err := runCmd(t, "vulns", "scan", "--live")
	if err != nil {
		t.Fatalf("vulns scan failed: %v", err)
	}
	want := "Skipping 1 dependency with a package identity that cannot be queried in OSV: " +
		"swift package \"apple.swift-argument-parser\" (Package.resolved).\n"
	if stderr != want {
		t.Fatalf("vulns scan stderr = %q, want %q", stderr, want)
	}
}

func TestInitStoresSwiftSourceCoordinate(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "Package.resolved", swiftSourcePackageResolved, "Add Swift lockfile")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	if _, _, err := runCmd(t, "init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	db, err := database.Open(filepath.Join(repoDir, ".git", "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	branch, err := db.GetDefaultBranch()
	if err != nil {
		t.Fatalf("get default branch: %v", err)
	}
	dependencies, err := db.GetLatestDependencies(branch.ID)
	if err != nil {
		t.Fatalf("get latest dependencies: %v", err)
	}
	if len(dependencies) != 1 {
		t.Fatalf("dependencies = %+v, want one Swift dependency", dependencies)
	}
	if dependencies[0].Name != "github.com/krzyzanowskim/CryptoSwift" {
		t.Errorf("dependency name = %q, want source coordinate", dependencies[0].Name)
	}
	if dependencies[0].PURL != "pkg:swift/github.com/krzyzanowskim/CryptoSwift@1.6.0" {
		t.Errorf("dependency PURL = %q, want source PURL", dependencies[0].PURL)
	}
}
