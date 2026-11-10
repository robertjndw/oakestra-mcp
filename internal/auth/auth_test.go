package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeJWT builds an unsigned token; the fake system manager decides what is
// valid, just like the real one does with its signature check.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

type fakeSM struct {
	t        *testing.T
	mu       sync.Mutex
	valid    map[string]bool // access tokens the "signature check" accepts
	refresh  map[string]string
	verifies atomic.Int32
	refreshs atomic.Int32
	status   int // forced status for /api/user, 0 = normal
}

func newFakeSM(t *testing.T) (*fakeSM, *httptest.Server) {
	f := &fakeSM{t: t, valid: map[string]bool{}, refresh: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSM) serve(w http.ResponseWriter, r *http.Request) {
	tok, _ := BearerToken(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/refresh":
		f.refreshs.Add(1)
		access, ok := f.refresh[tok]
		if !ok {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": access})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/user/"):
		f.verifies.Add(1)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if !f.valid[tok] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, _ := ParseClaims(tok)
		if strings.TrimPrefix(r.URL.Path, "/api/user/") != c.Subject {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"name":"` + c.Subject + `"}`))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func accessToken(t *testing.T, user string, exp time.Time) string {
	return makeJWT(t, map[string]any{
		"type": "access", "sub": user, "user": user, "roles": []string{"Admin"},
		"organization": "org1", "exp": exp.Unix(),
	})
}

func TestAccessTokenVerifiedAndCached(t *testing.T) {
	sm, srv := newFakeSM(t)
	clk := &clock{t: time.Now()}
	tok := accessToken(t, "alice", clk.now().Add(10*time.Minute))
	sm.valid[tok] = true
	a := NewAuthenticator(srv.URL, nil, WithClock(clk.now))

	p, err := a.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.User != "alice" || p.Organization != "org1" || !p.IsAdmin() || p.AccessToken != tok {
		t.Fatalf("unexpected principal %+v", p)
	}
	if _, err := a.Authenticate(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if n := sm.verifies.Load(); n != 1 {
		t.Fatalf("verified %d times, want 1 (cached)", n)
	}
	clk.add(31 * time.Second)
	if _, err := a.Authenticate(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if n := sm.verifies.Load(); n != 2 {
		t.Fatalf("verified %d times after cache expiry, want 2", n)
	}
}

func TestRejectedTokenIsUnauthorizedAndNotCached(t *testing.T) {
	sm, srv := newFakeSM(t)
	tok := accessToken(t, "mallory", time.Now().Add(time.Minute))
	a := NewAuthenticator(srv.URL, nil)
	for range 2 {
		_, err := a.Authenticate(context.Background(), tok)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("want ErrUnauthorized, got %v", err)
		}
	}
	if n := sm.verifies.Load(); n != 2 {
		t.Fatalf("failed verifications must not be cached, got %d calls", n)
	}
}

func TestExpiredAndMalformedTokensNeverReachOakestra(t *testing.T) {
	sm, srv := newFakeSM(t)
	a := NewAuthenticator(srv.URL, nil)
	for _, tok := range []string{
		"not-a-jwt",
		accessToken(t, "bob", time.Now().Add(-time.Minute)),
		makeJWT(t, map[string]any{"type": "access"}),
	} {
		if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("want ErrUnauthorized for %q, got %v", tok, err)
		}
	}
	if sm.verifies.Load() != 0 {
		t.Fatal("system manager should not have been called")
	}
}

func TestUpstreamFailureIsNotUnauthorized(t *testing.T) {
	sm, srv := newFakeSM(t)
	sm.status = http.StatusInternalServerError
	a := NewAuthenticator(srv.URL, nil)
	_, err := a.Authenticate(context.Background(), accessToken(t, "alice", time.Now().Add(time.Minute)))
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want infrastructure error, got %v", err)
	}
}

func TestRefreshTokenExchangedAndCached(t *testing.T) {
	sm, srv := newFakeSM(t)
	clk := &clock{t: time.Now()}
	refresh := makeJWT(t, map[string]any{
		"type": "refresh", "sub": "alice", "user": "alice", "roles": []string{"Application_Provider"},
		"organization": "org1", "exp": clk.now().Add(7 * 24 * time.Hour).Unix(),
	})
	access := accessToken(t, "alice", clk.now().Add(10*time.Minute))
	sm.refresh[refresh] = access
	sm.valid[access] = true
	a := NewAuthenticator(srv.URL, nil, WithClock(clk.now))

	p, err := a.Authenticate(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessToken != access {
		t.Fatal("principal must carry the exchanged access token, not the refresh token")
	}
	if _, err := a.Authenticate(context.Background(), refresh); err != nil {
		t.Fatal(err)
	}
	if n := sm.refreshs.Load(); n != 1 {
		t.Fatalf("refreshed %d times, want 1", n)
	}

	// Inside the 30s safety margin before the access token expires, a new
	// one must be fetched.
	clk.add(10*time.Minute - 20*time.Second)
	newAccess := accessToken(t, "alice", clk.now().Add(10*time.Minute))
	sm.mu.Lock()
	sm.refresh[refresh] = newAccess
	sm.valid[newAccess] = true
	sm.mu.Unlock()
	p, err = a.Authenticate(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	if n := sm.refreshs.Load(); n != 2 || p.AccessToken != newAccess {
		t.Fatalf("expected a second exchange, got %d", n)
	}
}

func TestRejectedRefreshToken(t *testing.T) {
	_, srv := newFakeSM(t)
	refresh := makeJWT(t, map[string]any{"type": "refresh", "sub": "alice", "exp": time.Now().Add(time.Hour).Unix()})
	a := NewAuthenticator(srv.URL, nil)
	if _, err := a.Authenticate(context.Background(), refresh); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestMiddleware(t *testing.T) {
	sm, srv := newFakeSM(t)
	tok := accessToken(t, "alice", time.Now().Add(time.Minute))
	sm.valid[tok] = true
	a := NewAuthenticator(srv.URL, nil)
	h := a.Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok || p.User != "alice" {
			t.Errorf("principal missing: %+v", p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		header string
		want   int
	}{
		{"", http.StatusUnauthorized},
		{"Basic abc", http.StatusUnauthorized},
		{"Bearer garbage", http.StatusUnauthorized},
		{"Bearer " + tok, http.StatusNoContent},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/info", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%q: got %d, want %d", c.header, rec.Code, c.want)
		}
		if c.want == http.StatusUnauthorized {
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 without WWW-Authenticate")
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
				t.Fatalf("401 body is not {\"error\":...}: %s", rec.Body)
			}
		}
	}
}

func TestCacheIsBounded(t *testing.T) {
	now := time.Now()
	c := newTTLCache[int](3, func() time.Time { return now })
	for i := range 10 {
		c.put(string(rune('a'+i)), i, now.Add(time.Minute))
	}
	if n := c.len(); n > 3 {
		t.Fatalf("cache grew to %d entries", n)
	}
}
