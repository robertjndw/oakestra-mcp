package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/mcpserver"
)

// fakeAuth accepts "Bearer good" only, standing in for the real
// authenticator, which has its own tests.
func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			WriteError(w, http.StatusUnauthorized, "nope")
			return
		}
		p := auth.Principal{User: "alice", Roles: []string{"Admin"}, Organization: "org1", AccessToken: "x"}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func newHandler(activity http.Handler) http.Handler {
	d := mcpserver.Deps{Version: "test"}
	return New(Options{
		Auth:            fakeAuth,
		MCP:             mcpserver.Handler(d),
		Tools:           []mcpserver.ToolInfo{{Name: "list_clusters", Title: "List clusters", Description: "d", Kind: mcpserver.KindRead}},
		Version:         "0.1.0",
		PublicURL:       "http://root.example:8090/mcp",
		ActivityHandler: activity,
	})
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthzNeedsNoAuth(t *testing.T) {
	rec := do(newHandler(nil), http.MethodGet, "/healthz", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body)
	}
}

func TestInfo(t *testing.T) {
	h := newHandler(nil)
	if rec := do(h, http.MethodGet, "/info", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("info without token: %d", rec.Code)
	}
	rec := do(h, http.MethodGet, "/info", "good", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("info: %d %s", rec.Code, rec.Body)
	}
	var got infoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "oakestra-mcp" || got.MCPPath != "/mcp" || got.User.Username != "alice" ||
		got.PublicURL == "" || len(got.Tools) != 1 || got.Tools[0].Kind != "read" {
		t.Fatalf("unexpected info %+v", got)
	}
}

func TestMCPEndpoint(t *testing.T) {
	h := newHandler(nil)
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}`
	if rec := do(h, http.MethodPost, "/mcp", "", initReq); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mcp without token: %d", rec.Code)
	}
	rec := do(h, http.MethodPost, "/mcp", "good", initReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "oakestra-mcp") {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected a JSON response, got %q", ct)
	}

	rec = do(h, http.MethodGet, "/mcp", "good", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: %d", rec.Code)
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
		t.Fatalf("SDK error not rewritten to JSON: %s", rec.Body)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	rec := do(newHandler(nil), http.MethodGet, "/nope", "", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := do(newHandler(nil), http.MethodGet, "/activity", "good", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("/activity without handler: %d", rec.Code)
	}
}

func TestActivitySeam(t *testing.T) {
	activity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		WriteJSON(w, http.StatusOK, map[string]string{"user": p.User})
	})
	h := newHandler(activity)
	if rec := do(h, http.MethodGet, "/activity", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("activity must be behind auth, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/activity?limit=5", "good", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("activity: %d %s", rec.Code, rec.Body)
	}
}
