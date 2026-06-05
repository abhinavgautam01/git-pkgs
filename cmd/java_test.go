package cmd_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
)

const mavenPomXML = `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <version>1.0.0</version>
  <dependencies>
    <dependency>
      <groupId>junit</groupId>
      <artifactId>junit</artifactId>
      <version>4.12</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
`

func TestOutdatedUsesMavenManifestDependencies(t *testing.T) {
	repoDir := createTestRepo(t)
	addFileAndCommit(t, repoDir, "pom.xml", mavenPomXML, "Add Maven pom")

	cleanup := chdir(t, repoDir)
	defer cleanup()

	_, _, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	db, err := database.Open(filepath.Join(repoDir, ".git", "pkgs.sqlite3"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	err = db.SavePackageEnrichment(
		"pkg:maven/junit/junit",
		"maven",
		"junit:junit",
		"4.13.2",
		"EPL-1.0",
		"https://repo.maven.apache.org/maven2",
		"test",
	)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close db: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("save package enrichment: %v", err)
	}

	stdout, _, err := runCmd(t, "outdated", "--ecosystem", "maven")
	if err != nil {
		t.Fatalf("outdated failed: %v", err)
	}
	if strings.Contains(stdout, "No lockfile dependencies found") {
		t.Fatalf("outdated ignored Maven manifest dependencies: %s", stdout)
	}
	if !strings.Contains(stdout, "junit:junit") {
		t.Fatalf("expected Maven dependency in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "4.12") || !strings.Contains(stdout, "4.13.2") {
		t.Fatalf("expected current and latest versions in output, got: %s", stdout)
	}
}
