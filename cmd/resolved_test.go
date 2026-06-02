package cmd

import (
	"testing"

	"github.com/git-pkgs/git-pkgs/internal/database"
)

func TestIsResolvedDependency(t *testing.T) {
	tests := []struct {
		name string
		dep  database.Dependency
		want bool
	}{
		{
			name: "lockfile dependency with version",
			dep: database.Dependency{
				Ecosystem:    "npm",
				Requirement:  "1.0.0",
				ManifestKind: manifestKindLockfile,
			},
			want: true,
		},
		{
			name: "go module manifest dependency with version",
			dep: database.Dependency{
				Ecosystem:    "golang",
				Requirement:  "1.0.0",
				ManifestKind: "manifest",
			},
			want: true,
		},
		{
			name: "maven manifest dependency with version",
			dep: database.Dependency{
				Ecosystem:    "maven",
				Requirement:  "4.13.2",
				ManifestKind: "manifest",
			},
			want: true,
		},
		{
			name: "maven manifest dependency without version",
			dep: database.Dependency{
				Ecosystem:    "maven",
				ManifestKind: "manifest",
			},
			want: false,
		},
		{
			name: "npm manifest dependency with version range",
			dep: database.Dependency{
				Ecosystem:    "npm",
				Requirement:  "^1.0.0",
				ManifestKind: "manifest",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isResolvedDependency(tt.dep)
			if got != tt.want {
				t.Fatalf("isResolvedDependency() = %v, want %v", got, tt.want)
			}
		})
	}
}
