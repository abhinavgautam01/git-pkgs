package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestSourceTrackerStatuses(t *testing.T) {
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	tracker := newSourceTracker()
	tracker.consider("swift", "", false)
	tracker.consider("npm", "registry.npmjs.org", true)
	tracker.markOK("npm", "registry.npmjs.org", now.Add(-90*time.Second))
	tracker.markError("npm", "registry.npmjs.org", errors.New("one package timed out"))
	tracker.consider("cargo", "crates.io", true)
	tracker.markError("cargo", "crates.io", errors.New("registry unavailable"))

	sources, warnings := tracker.statuses(now)
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3", len(sources))
	}
	if sources[0].Ecosystem != "cargo" || sources[0].Status != sourceStatusError {
		t.Fatalf("cargo source = %+v, want error", sources[0])
	}
	if sources[0].Error != "registry unavailable" {
		t.Fatalf("cargo error = %q", sources[0].Error)
	}
	if sources[1].Ecosystem != "npm" || sources[1].Status != sourceStatusOK {
		t.Fatalf("npm source = %+v, want ok", sources[1])
	}
	if sources[1].CacheAgeSeconds != 90 {
		t.Fatalf("npm cache age = %d, want 90", sources[1].CacheAgeSeconds)
	}
	if sources[2].Ecosystem != "swift" || sources[2].Status != sourceStatusUnsupported {
		t.Fatalf("swift source = %+v, want unsupported", sources[2])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "one package timed out") {
		t.Fatalf("warnings = %q, want partial npm error", warnings)
	}
	if tracker.allUnavailable() {
		t.Fatal("tracker with an ok source reported all unavailable")
	}
}

func TestSourceTrackerAllUnavailable(t *testing.T) {
	tracker := newSourceTracker()
	tracker.consider("cargo", "crates.io", true)
	tracker.markError("cargo", "crates.io", errors.New("registry unavailable"))
	tracker.consider("swift", "", false)

	err := tracker.unavailableError()
	if err == nil {
		t.Fatal("unavailableError = nil, want error")
	}
	if !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("unavailableError = %q", err)
	}
}

func TestMetadataJSONOutputsUseResultEnvelope(t *testing.T) {
	tracker := newSourceTracker()
	tracker.consider("npm", "registry.npmjs.org", true)
	tracker.markOK("npm", "registry.npmjs.org", time.Now().UTC())

	tests := []struct {
		name   string
		output func(*cobra.Command) error
	}{
		{
			name: "outdated",
			output: func(cmd *cobra.Command) error {
				return outputOutdatedJSON(cmd, []OutdatedPackage{{Name: "example"}}, tracker)
			},
		},
		{
			name: "deprecated",
			output: func(cmd *cobra.Command) error {
				return outputDeprecatedJSON(cmd, []DeprecatedPackage{{Name: "example"}}, tracker)
			},
		},
		{
			name: "licenses",
			output: func(cmd *cobra.Command) error {
				return outputLicensesJSON(cmd, []LicenseInfo{{Name: "example"}}, tracker)
			},
		},
		{
			name: "vulns",
			output: func(cmd *cobra.Command) error {
				return outputVulnsJSON(cmd, []VulnResult{{Package: "example"}}, tracker)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			if err := tt.output(cmd); err != nil {
				t.Fatalf("output: %v", err)
			}

			var envelope struct {
				Results []json.RawMessage `json:"results"`
				Sources []SourceStatus    `json:"sources"`
			}
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v\n%s", err, output.String())
			}
			if len(envelope.Results) != 1 || len(envelope.Sources) != 1 {
				t.Fatalf("envelope = %+v, want one result and source", envelope)
			}
		})
	}
}
