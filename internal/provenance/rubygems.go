package provenance

import "context"

func (c *Client) lookupRubyGems(_ context.Context, _ Dependency) Result {
	return Result{
		Status:   StatusUnsupported,
		Evidence: []string{"RubyGems does not expose per-release provenance through its V2 API"},
	}
}
