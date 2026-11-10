// Package auth turns the Oakestra JWT an agent presents into a verified
// Principal. There is no identity system of our own: access tokens are
// checked against the system manager, and refresh tokens are exchanged for
// access tokens there, because Oakestra access tokens only live 10 minutes
// and agents are configured once with a static header.
package auth

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnauthorized marks token problems the caller can fix (expired,
// malformed, rejected by Oakestra). Everything else returned by
// Authenticate is an infrastructure failure.
var ErrUnauthorized = errors.New("unauthorized")

const (
	// verifyTTL bounds how long a revoked or deleted user keeps working
	// after Oakestra stops accepting their token.
	verifyTTL = 30 * time.Second
	// refreshMargin keeps an exchanged access token from being handed out
	// right before it expires mid-request.
	refreshMargin = 30 * time.Second
	cacheSize     = 4096
)

// Authenticator verifies tokens against the Oakestra system manager.
type Authenticator struct {
	baseURL string
	hc      *http.Client
	now     func() time.Time

	verified  *ttlCache[Principal]
	exchanged *ttlCache[string]
}

// Option configures an Authenticator.
type Option func(*Authenticator)

// WithClock overrides the time source; tests use it to expire cache entries.
func WithClock(now func() time.Time) Option {
	return func(a *Authenticator) { a.now = now }
}

// NewAuthenticator creates an Authenticator for the system manager at
// baseURL (e.g. http://system_manager:10000). hc may be nil.
func NewAuthenticator(baseURL string, hc *http.Client, opts ...Option) *Authenticator {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	a := &Authenticator{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      hc,
		now:     time.Now,
	}
	for _, o := range opts {
		o(a)
	}
	a.verified = newTTLCache[Principal](cacheSize, a.now)
	a.exchanged = newTTLCache[string](cacheSize, a.now)
	return a
}

// Authenticate resolves token (an access or refresh token) to a Principal.
// Errors wrapping ErrUnauthorized are the caller's fault; any other error
// means the system manager could not be asked.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	claims, err := ParseClaims(token)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if a.expired(claims) {
		return Principal{}, fmt.Errorf("%w: token expired", ErrUnauthorized)
	}

	access := token
	if claims.IsRefresh() {
		access, claims, err = a.exchange(ctx, token, claims)
		if err != nil {
			return Principal{}, err
		}
	}
	return a.verify(ctx, access, claims)
}

func (a *Authenticator) expired(c Claims) bool {
	return !c.ExpiresAt.IsZero() && !a.now().Before(c.ExpiresAt)
}

// exchange trades a refresh token for an access token at
// POST /api/auth/refresh. The system manager guards that route with
// Flask-JWT-Extended's jwt_required(refresh=True), so the refresh token goes
// in the Authorization header and the answer is {"token": "<access>"}.
func (a *Authenticator) exchange(ctx context.Context, refresh string, rc Claims) (string, Claims, error) {
	key := tokenKey(refresh)
	if access, ok := a.exchanged.get(key); ok {
		if c, err := ParseClaims(access); err == nil {
			return access, c, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/auth/refresh", nil)
	if err != nil {
		return "", Claims{}, err
	}
	req.Header.Set("Authorization", "Bearer "+refresh)
	req.Header.Set("Accept", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return "", Claims{}, fmt.Errorf("refreshing token at system manager: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if rejected(resp.StatusCode) || resp.StatusCode == http.StatusNotFound {
		return "", Claims{}, fmt.Errorf("%w: refresh token rejected by Oakestra (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", Claims{}, fmt.Errorf("refreshing token: system manager returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", Claims{}, errors.New("refreshing token: system manager response has no token")
	}
	ac, err := ParseClaims(out.Token)
	if err != nil {
		return "", Claims{}, fmt.Errorf("refreshing token: system manager returned an unreadable token: %w", err)
	}

	exp := ac.ExpiresAt.Add(-refreshMargin)
	if ac.ExpiresAt.IsZero() {
		exp = a.now().Add(time.Minute)
	}
	// Never outlive the refresh token itself, or a revoked-by-expiry
	// refresh token would keep working through the cache.
	if !rc.ExpiresAt.IsZero() && rc.ExpiresAt.Before(exp) {
		exp = rc.ExpiresAt
	}
	a.exchanged.put(key, out.Token, exp)
	return out.Token, ac, nil
}

// verify asks the system manager whether access is valid by reading the
// caller's own user record, which only succeeds with a valid, unexpired
// token whose identity matches the username.
func (a *Authenticator) verify(ctx context.Context, access string, c Claims) (Principal, error) {
	key := tokenKey(access)
	if p, ok := a.verified.get(key); ok {
		return p, nil
	}

	// The route compares the URL username with the JWT identity ("sub").
	name := cmp.Or(c.Subject, c.User)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/user/"+url.PathEscape(name), nil)
	if err != nil {
		return Principal{}, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return Principal{}, fmt.Errorf("verifying token at system manager: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()

	if rejected(resp.StatusCode) {
		return Principal{}, fmt.Errorf("%w: token rejected by Oakestra (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Principal{}, fmt.Errorf("verifying token: system manager returned HTTP %d", resp.StatusCode)
	}

	p := Principal{User: c.Identity(), Roles: c.Roles, Organization: c.Organization, AccessToken: access}
	exp := a.now().Add(verifyTTL)
	if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(exp) {
		exp = c.ExpiresAt
	}
	a.verified.put(key, p, exp)
	return p, nil
}

// rejected covers the statuses Flask-JWT-Extended and the system manager use
// for bad tokens: 401 (missing/expired), 403 (identity mismatch) and 422
// (malformed, bad signature, wrong token type).
func rejected(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusUnprocessableEntity
}
