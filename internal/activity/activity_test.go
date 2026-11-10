package activity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/mcpserver"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insert(t *testing.T, s *Store, user, tool, outcome string, ts time.Time) int64 {
	t.Helper()
	id, err := s.Insert(context.Background(), Entry{TS: ts, User: user, Tool: tool, Kind: "read", ArgsJSON: "{}", Outcome: outcome})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRedactArgs(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig_-x"
	in := map[string]any{
		"name":     "svc",
		"Password": "p",
		"nested":   map[string]any{"Token": "t", "ok": 1, "deep": []any{map[string]any{"env": []string{"A=b"}, "x": "y"}}},
		"list":     []string{"plain", jwt},
		"note":     "eyJ not a jwt",
		"cmd":      "rm -rf /",
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(RedactArgs(in)), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "svc" || got["Password"] != "***" || got["cmd"] != "***" || got["note"] != "eyJ not a jwt" {
		t.Fatalf("top level: %v", got)
	}
	n := got["nested"].(map[string]any)
	if n["Token"] != "***" || n["ok"] != float64(1) {
		t.Fatalf("nested: %v", n)
	}
	d := n["deep"].([]any)[0].(map[string]any)
	if d["env"] != "***" || d["x"] != "y" {
		t.Fatalf("array: %v", d)
	}
	l := got["list"].([]any)
	if l[0] != "plain" || l[1] != "***" {
		t.Fatalf("jwt: %v", l)
	}
}

func TestRedactTruncatesAndHandlesBadInput(t *testing.T) {
	if got := RedactArgs(map[string]string{"blob": strings.Repeat("a", MaxArgsBytes)}); got != `{"truncated":true}` {
		t.Fatalf("got %.40s", got)
	}
	if got := RedactArgs(nil); got != "null" {
		t.Fatal(got)
	}
	if got := RedactArgs(make(chan int)); !json.Valid([]byte(got)) {
		t.Fatal(got)
	}
}

func TestPagingAndFilters(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		insert(t, s, "alice", "a_tool", "ok", now)
	}
	insert(t, s, "bob", "b_tool", "error", now)
	insert(t, s, "alice", "b_tool", "denied", now)

	rows, _ := s.List(context.Background(), Filter{Limit: 3})
	if len(rows) != 3 || rows[0].ID != 7 {
		t.Fatalf("%+v", rows)
	}
	rows, _ = s.List(context.Background(), Filter{Before: 3})
	if len(rows) != 2 || rows[0].ID != 2 {
		t.Fatalf("%+v", rows)
	}
	rows, _ = s.List(context.Background(), Filter{Tool: "b_tool"})
	if len(rows) != 2 {
		t.Fatal(len(rows))
	}
	rows, _ = s.List(context.Background(), Filter{Outcome: "denied"})
	if len(rows) != 1 || rows[0].User != "alice" {
		t.Fatalf("%+v", rows)
	}
}

func TestPrune(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	insert(t, s, "a", "t", "ok", now.Add(-48*time.Hour))
	insert(t, s, "a", "t", "ok", now)
	n, err := s.Prune(context.Background(), now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	rows, _ := s.List(context.Background(), Filter{})
	if len(rows) != 1 {
		t.Fatal(len(rows))
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestRecorderFlushOnClose(t *testing.T) {
	s := newStore(t)
	r := NewRecorder(s)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{User: "alice", Organization: "org"})
	for i := 0; i < 20; i++ {
		r.Record(ctx, mcpserver.Call{Tool: "t", Kind: mcpserver.KindRead, Args: map[string]any{"password": "x"},
			Outcome: mcpserver.OutcomeOK, Duration: 5 * time.Millisecond, ClientName: "cc", ActionID: "act"})
	}
	r.Close()
	r.Record(ctx, mcpserver.Call{Tool: "late"}) // must not panic after Close
	rows, _ := s.List(context.Background(), Filter{})
	if len(rows) != 20 {
		t.Fatalf("got %d rows", len(rows))
	}
	e := rows[0]
	if e.User != "alice" || e.Organization != "org" || e.DurationMS != 5 || e.ClientName != "cc" ||
		e.ActionID != "act" || e.ArgsJSON != `{"password":"***"}` {
		t.Fatalf("%+v", e)
	}
}

func TestRecorderDropsWhenFull(t *testing.T) {
	s := newStore(t)
	r := &Recorder{store: s, ch: make(chan queued, 1), done: make(chan struct{})} // no writer: queue stays full
	r.Record(context.Background(), mcpserver.Call{Tool: "a"})
	r.Record(context.Background(), mcpserver.Call{Tool: "b"})
	if n := r.dropped.Load(); n != 1 {
		t.Fatal(n)
	}
}

func get(t *testing.T, h http.Handler, p auth.Principal, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil).WithContext(auth.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) (items []map[string]any, next any) {
	t.Helper()
	var body struct {
		Items      []map[string]any `json:"items"`
		NextBefore any              `json:"next_before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return body.Items, body.NextBefore
}

func TestHandler(t *testing.T) {
	s := newStore(t)
	h := s.Handler()
	now := time.Now()
	for i := 0; i < 3; i++ {
		insert(t, s, "alice", "t1", "ok", now)
	}
	insert(t, s, "bob", "t2", "error", now)

	admin := auth.Principal{User: "root", Roles: []string{auth.RoleAdmin}}
	alice := auth.Principal{User: "alice"}

	items, _ := decode(t, get(t, h, admin, "GET", "/activity"))
	if len(items) != 4 {
		t.Fatal(len(items))
	}
	items, _ = decode(t, get(t, h, admin, "GET", "/activity?user=bob"))
	if len(items) != 1 || items[0]["user"] != "bob" {
		t.Fatal(items)
	}
	// Non-admins cannot widen their view.
	items, _ = decode(t, get(t, h, alice, "GET", "/activity?user=bob"))
	if len(items) != 3 {
		t.Fatal(len(items))
	}
	items, _ = decode(t, get(t, h, alice, "GET", "/activity?outcome=error"))
	if len(items) != 0 {
		t.Fatal(items)
	}

	// Paging.
	items, next := decode(t, get(t, h, admin, "GET", "/activity?limit=2"))
	if len(items) != 2 || next != float64(3) {
		t.Fatal(items, next)
	}
	items, next = decode(t, get(t, h, admin, "GET", "/activity?limit=2&before=3"))
	if len(items) != 2 || next != nil {
		t.Fatal(items, next)
	}

	if w := get(t, h, admin, "POST", "/activity"); w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
	if w := get(t, h, admin, "GET", "/activity?limit=x"); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
	// Empty result is [] not null.
	items, _ = decode(t, get(t, h, auth.Principal{User: "nobody"}, "GET", "/activity"))
	if items == nil {
		t.Fatal("items should be an empty array")
	}
}

func TestRedactsConfirmationToken(t *testing.T) {
	got := RedactArgs(map[string]any{"confirmation_token": "0123.1800000000.mac"})
	if got != `{"confirmation_token":"***"}` {
		t.Fatal(got)
	}
}

func TestPendingActions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	a := actions.Pending{ID: "a1", User: "alice", Organization: "org", Kind: actions.KindScaleService,
		ParamsJSON: `{"service_id":"svc1","replicas":2}`, PlanHash: "h", Created: now, Expires: now.Add(5 * time.Minute)}
	if err := s.SavePending(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPending(ctx, "a1")
	if err != nil || got.User != "alice" || got.Kind != actions.KindScaleService || !got.Expires.Equal(a.Expires) ||
		got.ParamsJSON != a.ParamsJSON || got.ConsumedAt != nil {
		t.Fatalf("load: %+v %v", got, err)
	}
	if _, err := s.LoadPending(ctx, "nope"); !errors.Is(err, actions.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	// Racing consumers: exactly one wins.
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ConsumePending(ctx, "a1", now)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d consumers won", wins.Load())
	}
	got, _ = s.LoadPending(ctx, "a1")
	if got.ConsumedAt == nil || got.ParamsJSON != "{}" {
		t.Fatalf("consumed row: %+v", got)
	}

	if n, err := s.PrunePending(ctx, now.Add(10*time.Minute)); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

func TestMigrationKeepsCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	insert(t, s, "alice", "list_clusters", "ok", time.Now())
	_ = s.Close()
	// Reopening must not re-run migrations on an up-to-date database.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version %d %v", v, err)
	}
	if es, _ := s.List(context.Background(), Filter{}); len(es) != 1 {
		t.Fatalf("calls lost: %d", len(es))
	}
}

func TestRedactStringSLAAndEmbeddedJWT(t *testing.T) {
	sla := `{"applications":[{"microservices":[{"microservice_name":"db","environment":["DB_PASSWORD=hunter2"],"cmd":["run","--pw=hunter2"]}]}]}`
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln"
	out := RedactArgs(map[string]any{
		"sla":  sla,
		"note": "Authorization: Bearer " + jwt + " please",
	})
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret from string SLA leaked: %s", out)
	}
	if !strings.Contains(out, `"microservice_name":"db"`) {
		t.Fatalf("non-secret SLA fields should survive: %s", out)
	}
	if strings.Contains(out, jwt) || !strings.Contains(out, "Bearer *** please") {
		t.Fatalf("embedded JWT not masked: %s", out)
	}
	// A string that only looks like JSON stays as it was.
	if got := RedactArgs(map[string]any{"q": "{not json"}); got != `{"q":"{not json"}` {
		t.Fatalf("got %s", got)
	}
}
