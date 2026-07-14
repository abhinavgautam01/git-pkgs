package provenance

import (
	"context"
	"testing"
)

func TestLookupRubyGemsIsUnsupportedWithoutNetworkRequest(t *testing.T) {
	client, fake := newFakeClient(nil)
	got := client.Lookup(context.Background(), Dependency{Ecosystem: "rubygems", Name: "rails", Version: "8.0.0"})

	if got.Status != StatusUnsupported {
		t.Fatalf("status = %q, want %q", got.Status, StatusUnsupported)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(fake.requests))
	}
}
