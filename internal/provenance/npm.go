package provenance

import (
	"context"
	"net/url"
)

func (c *Client) lookupNPM(ctx context.Context, dep Dependency) Result {
	endpoint := "https://registry.npmjs.org/" + url.PathEscape(dep.Name) + "/" + url.PathEscape(dep.Version)
	var body struct {
		Dist struct {
			Attestations any   `json:"attestations"`
			Provenance   any   `json:"provenance"`
			Signatures   []any `json:"signatures"`
		} `json:"dist"`
	}
	if err := c.fetchJSON(ctx, endpoint, "application/json", &body); err != nil {
		return Result{Status: StatusError, Error: err.Error()}
	}

	if hasValue(body.Dist.Attestations) || hasValue(body.Dist.Provenance) {
		return Result{
			Status:             StatusTrustedPublishing,
			TrustedPublishing:  true,
			RegistrySignatures: len(body.Dist.Signatures),
			Evidence:           []string{"npm registry attestation"},
		}
	}
	if len(body.Dist.Signatures) > 0 {
		return Result{
			Status:             StatusSigned,
			RegistrySignatures: len(body.Dist.Signatures),
			Evidence:           []string{"npm registry signature"},
		}
	}
	return Result{Status: StatusMissing}
}

func hasValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}
