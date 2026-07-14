package provenance

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakeHTTPClient struct {
	responses map[string]fakeResponse
	requests  []*http.Request
}

type fakeResponse struct {
	status int
	body   string
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req)
	response, ok := f.responses[req.URL.String()]
	if !ok {
		response = fakeResponse{status: http.StatusNotFound}
	}
	return &http.Response{
		StatusCode: response.status,
		Body:       io.NopCloser(strings.NewReader(response.body)),
	}, nil
}

func newFakeClient(responses map[string]fakeResponse) (*Client, *fakeHTTPClient) {
	fake := &fakeHTTPClient{responses: responses}
	return NewClientWithHTTPClient("git-pkgs/test", fake), fake
}

func TestLookupNPMTrustedPublishing(t *testing.T) {
	client, _ := newFakeClient(map[string]fakeResponse{
		"https://registry.npmjs.org/@scope%2Fpkg/1.2.3": {
			status: http.StatusOK,
			body:   `{"dist":{"attestations":{"provenance":{}},"signatures":[{}]}}`,
		},
	})

	got := client.Lookup(context.Background(), Dependency{Ecosystem: "npm", Name: "@scope/pkg", Version: "1.2.3"})
	if got.Status != StatusTrustedPublishing || !got.TrustedPublishing {
		t.Fatalf("result = %#v, want trusted publishing", got)
	}
	if got.RegistrySignatures != 1 {
		t.Fatalf("registry signatures = %d, want 1", got.RegistrySignatures)
	}
}

func TestLookupNPMSignedOnly(t *testing.T) {
	client, _ := newFakeClient(map[string]fakeResponse{
		"https://registry.npmjs.org/lodash/4.17.21": {
			status: http.StatusOK,
			body:   `{"dist":{"signatures":[{}]}}`,
		},
	})

	got := client.Lookup(context.Background(), Dependency{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"})
	if got.Status != StatusSigned || got.TrustedPublishing {
		t.Fatalf("result = %#v, want signed only", got)
	}
}
