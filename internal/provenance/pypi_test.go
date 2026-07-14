package provenance

import (
	"context"
	"net/http"
	"testing"
)

func TestLookupPyPIUsesIntegrityAPIForEveryReleaseFile(t *testing.T) {
	client, fake := newFakeClient(map[string]fakeResponse{
		"https://pypi.org/pypi/sampleproject/4.0.0/json": {
			status: http.StatusOK,
			body:   `{"urls":[{"filename":"sampleproject-4.0.0.tar.gz"},{"filename":"sampleproject-4.0.0-py3-none-any.whl"}]}`,
		},
		"https://pypi.org/integrity/sampleproject/4.0.0/sampleproject-4.0.0.tar.gz/provenance": {
			status: http.StatusOK,
			body:   `{"attestation_bundles":[{}]}`,
		},
		"https://pypi.org/integrity/sampleproject/4.0.0/sampleproject-4.0.0-py3-none-any.whl/provenance": {
			status: http.StatusOK,
			body:   `{"attestation_bundles":[{}]}`,
		},
	})

	got := client.Lookup(context.Background(), Dependency{Ecosystem: "pypi", Name: "sampleproject", Version: "4.0.0"})
	if got.Status != StatusTrustedPublishing || !got.TrustedPublishing {
		t.Fatalf("result = %#v, want trusted publishing", got)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("requests = %d, want release metadata plus two file provenance requests", len(fake.requests))
	}
	for _, request := range fake.requests[1:] {
		if got := request.Header.Get("Accept"); got != pypiIntegrityAccept {
			t.Fatalf("Integrity API Accept = %q, want %q", got, pypiIntegrityAccept)
		}
	}
}

func TestLookupPyPIMissingWhenAnyReleaseFileIsUnattested(t *testing.T) {
	client, _ := newFakeClient(map[string]fakeResponse{
		"https://pypi.org/pypi/sampleproject/4.0.0/json": {
			status: http.StatusOK,
			body:   `{"urls":[{"filename":"sampleproject-4.0.0.tar.gz"},{"filename":"sampleproject-4.0.0-py3-none-any.whl"}]}`,
		},
		"https://pypi.org/integrity/sampleproject/4.0.0/sampleproject-4.0.0.tar.gz/provenance": {
			status: http.StatusOK,
			body:   `{"attestation_bundles":[{}]}`,
		},
		"https://pypi.org/integrity/sampleproject/4.0.0/sampleproject-4.0.0-py3-none-any.whl/provenance": {
			status: http.StatusNotFound,
		},
	})

	got := client.Lookup(context.Background(), Dependency{Ecosystem: "pypi", Name: "sampleproject", Version: "4.0.0"})
	if got.Status != StatusMissing || got.TrustedPublishing {
		t.Fatalf("result = %#v, want missing provenance", got)
	}
}
