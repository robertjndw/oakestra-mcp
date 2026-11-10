// Package oakestra hands out oakestra-go clients that act as the calling
// user, plus the few system manager calls oakestra-go does not cover.
package oakestra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
)

// Factory builds per-principal clients. Clients are cheap: they all share
// one http.Client (and with it the connection pool), only the token differs.
type Factory struct {
	baseURL   string
	hc        *http.Client
	userAgent string
}

// NewFactory validates baseURL once so ClientFor can only fail on a missing
// token. hc may be nil.
func NewFactory(baseURL string, hc *http.Client, userAgent string) (*Factory, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if userAgent == "" {
		userAgent = "oakestra-mcp"
	}
	if _, err := oakapi.NewClient(oakapi.WithBaseURL(baseURL)); err != nil {
		return nil, err
	}
	return &Factory{baseURL: baseURL, hc: hc, userAgent: userAgent}, nil
}

// ClientFor returns a client that authenticates as p. Oakestra enforces
// ownership server-side based on that token, which is what keeps
// non-admins limited to their own applications.
func (f *Factory) ClientFor(p auth.Principal) (*oakapi.Client, error) {
	if p.AccessToken == "" {
		return nil, fmt.Errorf("no access token for user %q", p.User)
	}
	return oakapi.NewClient(
		oakapi.WithBaseURL(f.baseURL),
		oakapi.WithHTTPClient(f.hc),
		oakapi.WithUserAgent(f.userAgent),
		oakapi.WithToken(p.AccessToken),
	)
}

// GetRaw fetches path from the system manager and returns the body
// untouched. It is the escape hatch for fields oakestra-go's typed structs
// drop, e.g. the full job document of a service (vram, constraints,
// volumes) that placement needs. Errors are oakestra-go's typed errors.
func GetRaw(ctx context.Context, c *oakapi.Client, path string) (json.RawMessage, error) {
	req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, req, &raw); err != nil {
		return nil, err
	}
	// Some Oakestra versions wrap the body in a JSON string; oakestra-go's
	// decoder unwraps that for typed targets but a RawMessage takes the
	// string verbatim.
	var s string
	if json.Unmarshal(raw, &s) == nil && json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	return raw, nil
}

// GetServiceRaw returns the system manager's job document for a service.
func GetServiceRaw(ctx context.Context, c *oakapi.Client, serviceID string) (json.RawMessage, error) {
	return GetRaw(ctx, c, "api/service/"+url.PathEscape(serviceID))
}

// DescribeResponse renders an error answer from the system manager for an
// agent: the status and a capped body, without the URL oakestra-go puts into
// its own message.
func DescribeResponse(e *oakapi.ErrorResponse) string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("Oakestra returned HTTP %d: %s", e.StatusCode, body)
}
