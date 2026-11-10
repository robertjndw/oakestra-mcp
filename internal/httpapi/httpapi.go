// Package httpapi assembles the HTTP routes. The contract is documented in
// docs/api.md; the dashboard reaches the same routes under
// /addon-assets/oakestra-mcp/api/ because its nginx strips that prefix.
package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/mcpserver"
)

// MCPPath is where the MCP endpoint is mounted.
const MCPPath = "/mcp"

// maxMCPBody bounds request bodies; SLAs are the largest legitimate input
// and are nowhere near this.
const maxMCPBody = 8 << 20

// Options wires the routes. Auth and MCP are required; the rest is
// optional so later phases can fill it in.
type Options struct {
	// Auth wraps every route except /healthz.
	Auth func(http.Handler) http.Handler
	MCP  http.Handler
	// Tools is the catalog reported by /info.
	Tools         []mcpserver.ToolInfo
	Version       string
	PublicURL     string
	WritesEnabled bool
	// ActivityHandler is mounted at /activity when set. The principal is on
	// the request context (auth.FromContext).
	ActivityHandler http.Handler
	Logger          *slog.Logger
}

// New returns the root handler.
func New(o Options) http.Handler {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mcp := jsonErrors(http.MaxBytesHandler(o.MCP, maxMCPBody))
	mux.Handle(MCPPath, o.Auth(mcp))
	mux.Handle("GET /info", o.Auth(http.HandlerFunc(o.info)))
	if o.ActivityHandler != nil {
		mux.Handle("/activity", o.Auth(o.ActivityHandler))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotFound, "no route for "+r.Method+" "+r.URL.Path)
	})
	return logRequests(log, mux)
}

type infoUser struct {
	Username     string   `json:"username"`
	Roles        []string `json:"roles"`
	Organization string   `json:"organization"`
}

type infoResponse struct {
	Name          string               `json:"name"`
	Version       string               `json:"version"`
	MCPPath       string               `json:"mcp_path"`
	PublicURL     string               `json:"public_url"`
	WritesEnabled bool                 `json:"writes_enabled"`
	User          infoUser             `json:"user"`
	Tools         []mcpserver.ToolInfo `json:"tools"`
}

func (o Options) info(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	roles := p.Roles
	if roles == nil {
		roles = []string{}
	}
	tools := o.Tools
	if tools == nil {
		tools = []mcpserver.ToolInfo{}
	}
	WriteJSON(w, http.StatusOK, infoResponse{
		Name:          "oakestra-mcp",
		Version:       o.Version,
		MCPPath:       MCPPath,
		PublicURL:     o.PublicURL,
		WritesEnabled: o.WritesEnabled,
		User:          infoUser{Username: p.User, Roles: roles, Organization: p.Organization},
		Tools:         tools,
	})
}

// WriteJSON writes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteError writes the {"error": msg} body every route uses for failures.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests logs one line per request. Headers are deliberately not
// logged: they carry the caller's Oakestra token.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" && sw.status == http.StatusOK {
			return
		}
		log.Info("http request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// jsonErrors rewrites the SDK's plain-text error responses (405, 415, 400)
// into the {"error": ...} shape the rest of the API uses. Successful
// responses pass through untouched.
func jsonErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ew := &errorWriter{ResponseWriter: w}
		next.ServeHTTP(ew, r)
		if ew.capturing {
			WriteError(w, ew.status, strings.TrimSpace(ew.buf.String()))
		}
	})
}

type errorWriter struct {
	http.ResponseWriter
	status    int
	capturing bool
	buf       bytes.Buffer
}

func (e *errorWriter) WriteHeader(code int) {
	if code >= 400 && strings.HasPrefix(e.Header().Get("Content-Type"), "text/plain") {
		e.status, e.capturing = code, true
		return
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *errorWriter) Write(b []byte) (int, error) {
	if e.capturing {
		return e.buf.Write(b)
	}
	return e.ResponseWriter.Write(b)
}

func (e *errorWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }
