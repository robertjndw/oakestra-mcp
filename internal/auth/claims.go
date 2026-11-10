package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Token types as set by Flask-JWT-Extended in the "type" claim.
const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

// Claims are the parts of an Oakestra JWT this server cares about.
type Claims struct {
	Type         string
	Subject      string
	User         string
	Roles        []string
	Organization string
	ExpiresAt    time.Time
}

// Identity is the username Oakestra knows the caller by. The "user" claim
// is what the system manager writes explicitly, "sub" is the
// Flask-JWT-Extended identity; they are equal for tokens from /api/auth/login
// but either may be missing in hand-made tokens.
func (c Claims) Identity() string {
	if c.User != "" {
		return c.User
	}
	return c.Subject
}

// IsRefresh reports whether the token is a refresh token.
func (c Claims) IsRefresh() bool { return c.Type == TokenTypeRefresh }

// ParseClaims decodes the JWT payload without checking the signature. That
// is deliberate: the signing key lives in Oakestra's jwt-generator, so
// validity is established by asking the system manager (see Authenticator).
// Never trust these claims before that check has passed.
func ParseClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return Claims{}, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var raw struct {
		Type         string          `json:"type"`
		Sub          json.RawMessage `json:"sub"`
		User         string          `json:"user"`
		Roles        json.RawMessage `json:"roles"`
		Organization string          `json:"organization"`
		Exp          *float64        `json:"exp"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("decoding JWT claims: %w", err)
	}
	c := Claims{
		Type:         raw.Type,
		User:         raw.User,
		Organization: raw.Organization,
		Roles:        parseRoles(raw.Roles),
	}
	if c.Type == "" {
		c.Type = TokenTypeAccess
	}
	// Flask-JWT-Extended allows non-string identities; Oakestra uses the
	// username, but don't fail on anything else.
	var sub string
	if json.Unmarshal(raw.Sub, &sub) == nil {
		c.Subject = sub
	}
	if raw.Exp != nil {
		sec := int64(*raw.Exp)
		c.ExpiresAt = time.Unix(sec, 0)
	}
	if c.Identity() == "" {
		return Claims{}, errors.New("JWT has neither a user nor a sub claim")
	}
	return c, nil
}

// parseRoles accepts both a list of role names (what Oakestra issues) and
// a list of {"name": ...} objects, so a schema change upstream degrades to
// "no roles" instead of rejecting every token.
func parseRoles(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	if json.Unmarshal(raw, &names) == nil {
		return names
	}
	var objs []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &objs) == nil {
		for _, o := range objs {
			if o.Name != "" {
				names = append(names, o.Name)
			} else if o.Role != "" {
				names = append(names, o.Role)
			}
		}
	}
	return names
}

// BearerToken extracts the token from an "Authorization: Bearer ..." header.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
