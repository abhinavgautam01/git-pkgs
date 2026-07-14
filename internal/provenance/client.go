package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxResponseBytes = 10 << 20

// HTTPClient is the subset of http.Client used for registry requests.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Client dispatches provenance lookups to ecosystem-specific checkers.
type Client struct {
	httpClient HTTPClient
	userAgent  string
}

// NewClient creates a provenance client with a bounded request timeout.
func NewClient(userAgent string) *Client {
	return NewClientWithHTTPClient(userAgent, &http.Client{Timeout: 30 * time.Second})
}

// NewClientWithHTTPClient creates a provenance client using the provided HTTP client.
func NewClientWithHTTPClient(userAgent string, httpClient HTTPClient) *Client {
	return &Client{httpClient: httpClient, userAgent: userAgent}
}

// Lookup retrieves the available provenance signal for a dependency release.
func (c *Client) Lookup(ctx context.Context, dep Dependency) Result {
	switch dep.Ecosystem {
	case "npm":
		return c.lookupNPM(ctx, dep)
	case "pypi":
		return c.lookupPyPI(ctx, dep)
	case "rubygems":
		return c.lookupRubyGems(ctx, dep)
	default:
		return Result{
			Status:   StatusUnsupported,
			Evidence: []string{"provenance lookup is only supported for npm and pypi"},
		}
	}
}

type notFoundError struct{}

func (notFoundError) Error() string { return "registry resource not found" }

func isNotFound(err error) bool {
	var target notFoundError
	return errors.As(err, &target)
}

func (c *Client) fetchJSON(ctx context.Context, endpoint, accept string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", accept)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return notFoundError{}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(target); err != nil {
		return fmt.Errorf("decoding registry response: %w", err)
	}
	return nil
}
