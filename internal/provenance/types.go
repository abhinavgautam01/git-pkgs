// Package provenance checks registry-provided provenance signals for exact
// package versions without depending on CLI or database concerns.
package provenance

import "context"

// Status describes the provenance signal available for a dependency release.
type Status string

const (
	StatusTrustedPublishing Status = "trusted_publishing"
	StatusSigned            Status = "signed"
	StatusMissing           Status = "missing"
	StatusUnsupported       Status = "unsupported"
	StatusError             Status = "error"
)

// Dependency identifies one exact package version to inspect.
type Dependency struct {
	Ecosystem string
	Name      string
	Version   string
}

// Result contains the registry signal and any supporting evidence.
type Result struct {
	Status             Status
	TrustedPublishing  bool
	RegistrySignatures int
	Evidence           []string
	Error              string
}

// Checker checks provenance for exact dependency versions.
type Checker interface {
	Lookup(context.Context, Dependency) Result
}
