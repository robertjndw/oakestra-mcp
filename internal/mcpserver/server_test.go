package mcpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
)

const (
	appsJSON = `[
	 {"applicationID":"app1","application_name":"shop","application_namespace":"prod","microservices":["svc1","svc2"]},
	 {"applicationID":"app2","application_name":"dup","application_namespace":"a","microservices":[]},
	 {"applicationID":"app3","application_name":"dup","application_namespace":"b","microservices":[]}]`
	servicesJSON = `[
	 {"microserviceID":"svc1","microservice_name":"web","microservice_namespace":"prod","applicationID":"app1",
	  "application_name":"shop","status":"RUNNING","instance_list":[{"instance_number":0,"status":"RUNNING","cluster_id":"c1","worker_id":"w1"}]},
	 {"microserviceID":"svc2","microservice_name":"db","microservice_namespace":"prod","applicationID":"app1",
	  "application_name":"shop","status":"FAILED","instance_list":[{"instance_number":0,"status":"FAILED","status_detail":"NoActiveClusterWithCapacity"}]}]`
	serviceDetailJSON = `{"microserviceID":"svc1","microservice_name":"web","microservice_namespace":"prod","applicationID":"app1",
	  "app_name":"shop","app_ns":"prod","status":"RUNNING","code":"docker.io/library/nginx:latest","virtualization":"container",
	  "vcpus":1,"memory":256,"environment":["SECRET=1"],
	  "instance_list":[{"instance_number":0,"status":"RUNNING","cluster_id":"c1","worker_id":"w1","host_ip":"10.0.0.5"}]}`
	rootRA   = `[{"_id":"c1","candidate_name":"cluster-a","ip":"127.0.0.1","active":true,"active_nodes":1,"vcpus":8,"memory":16000,"virtualization":["docker"]},{"_id":"c2","candidate_name":"cluster-b","ip":"","active":false}]`
	workerRA = `[{"_id":"w1","candidate_name":"worker-1","architecture":"amd64","vcpus":8,"memory":16000,"virtualization":["docker"],"active":true}]`
)

type fakeOakestra struct {
	t     *testing.T
	token string
}

func (f *fakeOakestra) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/applications":
		// Admin-only upstream; the test principal is a plain user.
		w.WriteHeader(http.StatusForbidden)
	case "/api/user/alice":
		_, _ = w.Write([]byte(`{"_id":{"$oid":"u1"},"name":"alice"}`))
	case "/api/applications/u1":
		_, _ = w.Write([]byte(appsJSON))
	case "/api/application/app1":
		_, _ = w.Write([]byte(`{"applicationID":"app1","application_name":"shop","application_namespace":"prod","microservices":["svc1","svc2"]}`))
	case "/api/application/forbidden":
		w.WriteHeader(http.StatusForbidden)
	case "/api/services/", "/api/services/app1":
		_, _ = w.Write([]byte(servicesJSON))
	case "/api/service/svc1":
		_, _ = w.Write([]byte(serviceDetailJSON))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type memRecorder struct {
	mu    sync.Mutex
	calls []Call
	users []string
}

func (m *memRecorder) Record(ctx context.Context, c Call) {
	p, _ := auth.FromContext(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
	m.users = append(m.users, p.User)
}

func setup(t *testing.T) (*sdk.ClientSession, *memRecorder) {
	t.Helper()
	sm := httptest.NewServer(&fakeOakestra{t: t, token: "tok"})
	t.Cleanup(sm.Close)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workerRA))
	}))
	t.Cleanup(worker.Close)
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rootRA))
	}))
	t.Cleanup(root.Close)
	_, port, _ := net.SplitHostPort(worker.Listener.Addr().String())
	p, _ := strconv.Atoi(port)

	f, err := oakestra.NewFactory(sm.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := &memRecorder{}
	cands := candidates.New(root.URL, p, nil)
	d := Deps{Oakestra: f, Candidates: cands, Placement: cands, Recorder: rec, Version: "test"}
	principal := auth.Principal{User: "alice", Roles: []string{auth.RoleApplicationProvider}, AccessToken: "tok"}

	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := NewServer(d, principal).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, rec
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, map[string]any) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return res, out
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestToolListMatchesCatalog(t *testing.T) {
	cs, _ := setup(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", tool.Name)
		}
	}
	cands := candidates.New("http://x", 1, nil)
	cat := Catalog(Deps{Oakestra: &oakestra.Factory{}, Candidates: cands, Placement: cands})
	if len(cat) != len(res.Tools) {
		t.Fatalf("catalog has %d tools, server %d", len(cat), len(res.Tools))
	}
	for _, info := range cat {
		if !got[info.Name] || info.Kind != KindRead || info.Description == "" || info.Title == "" {
			t.Errorf("bad catalog entry %+v", info)
		}
	}
}

func TestDepsGateTools(t *testing.T) {
	if n := len(Catalog(Deps{})); n != 0 {
		t.Fatalf("no deps should mean no tools, got %d", n)
	}
	cat := Catalog(Deps{Candidates: candidates.New("http://x", 1, nil)})
	if len(cat) != 2 {
		t.Fatalf("candidates only should give 2 tools, got %d", len(cat))
	}
}

func TestListClustersAndNodes(t *testing.T) {
	cs, rec := setup(t)
	res, out := call(t, cs, "list_clusters", nil)
	if res.IsError {
		t.Fatal(text(res))
	}
	clusters := out["clusters"].([]any)
	if len(clusters) != 2 || clusters[0].(map[string]any)["name"] != "cluster-a" {
		t.Fatalf("unexpected clusters %v", out)
	}
	if !strings.Contains(text(res), "2 clusters, 1 active") {
		t.Fatalf("summary: %s", text(res))
	}

	for _, ref := range []string{"cluster-a", "c1"} {
		res, out = call(t, cs, "list_nodes", map[string]any{"cluster": ref})
		if res.IsError {
			t.Fatal(text(res))
		}
		nodes := out["nodes"].([]any)
		if len(nodes) != 1 || nodes[0].(map[string]any)["architecture"] != "amd64" {
			t.Fatalf("unexpected nodes %v", out)
		}
	}

	res, _ = call(t, cs, "list_nodes", map[string]any{"cluster": "nope"})
	if !res.IsError || !strings.Contains(text(res), "cluster-a") {
		t.Fatalf("unknown cluster should list known ones: %s", text(res))
	}
	res, _ = call(t, cs, "list_nodes", map[string]any{"cluster": "cluster-b"})
	if !res.IsError || !strings.Contains(text(res), "cluster-b") {
		t.Fatalf("cluster without IP should fail clearly: %s", text(res))
	}

	if len(rec.calls) != 5 {
		t.Fatalf("recorded %d calls, want 5", len(rec.calls))
	}
	first := rec.calls[0]
	if first.Tool != "list_clusters" || first.Kind != KindRead || first.Outcome != OutcomeOK || first.ClientName != "test-agent" {
		t.Fatalf("bad record %+v", first)
	}
	if rec.users[0] != "alice" {
		t.Fatalf("recorder context has no principal")
	}
	if last := rec.calls[4]; last.Outcome != OutcomeError || last.Err == "" {
		t.Fatalf("failed call recorded as %+v", last)
	}
}

func TestApplicationTools(t *testing.T) {
	cs, _ := setup(t)

	res, out := call(t, cs, "list_applications", nil)
	if res.IsError || len(out["applications"].([]any)) != 3 {
		t.Fatalf("list_applications: %s", text(res))
	}

	res, out = call(t, cs, "get_application", map[string]any{"id_or_name": "shop"})
	if res.IsError {
		t.Fatal(text(res))
	}
	if out["id"] != "app1" || len(out["services"].([]any)) != 2 {
		t.Fatalf("get_application by name: %v", out)
	}

	res, _ = call(t, cs, "get_application", map[string]any{"id_or_name": "dup"})
	if !res.IsError || !strings.Contains(text(res), "app2") || !strings.Contains(text(res), "app3") {
		t.Fatalf("ambiguous name should list IDs: %s", text(res))
	}

	res, _ = call(t, cs, "get_application", map[string]any{"id_or_name": "ghost"})
	if !res.IsError || !strings.Contains(text(res), "list_applications") {
		t.Fatalf("not found should point to list_applications: %s", text(res))
	}
}

func TestServiceTools(t *testing.T) {
	cs, _ := setup(t)

	res, out := call(t, cs, "list_services", map[string]any{"application": "shop"})
	if res.IsError {
		t.Fatal(text(res))
	}
	svcs := out["services"].([]any)
	if len(svcs) != 2 || svcs[0].(map[string]any)["running"].(float64) != 1 {
		t.Fatalf("list_services: %v", out)
	}

	for _, ref := range []string{"svc1", "web"} {
		res, out = call(t, cs, "get_service", map[string]any{"id_or_name": ref})
		if res.IsError {
			t.Fatal(text(res))
		}
		if out["image"] != "docker.io/library/nginx:latest" || out["id"] != "svc1" {
			t.Fatalf("get_service %s: %v", ref, out)
		}
		inst := out["instance_list"].([]any)[0].(map[string]any)
		if inst["cluster_name"] != "cluster-a" || inst["worker_id"] != "w1" {
			t.Fatalf("instance not annotated: %v", inst)
		}
		if strings.Contains(text(res), "SECRET") {
			t.Fatal("environment must not be exposed")
		}
	}
}

func TestErrorsMapToToolErrors(t *testing.T) {
	sm := httptest.NewServer(&fakeOakestra{t: t, token: "other"})
	defer sm.Close()
	f, _ := oakestra.NewFactory(sm.URL, nil, "")
	rec := &memRecorder{}
	st, ct := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, _ := NewServer(Deps{Oakestra: f, Recorder: rec}, auth.Principal{User: "bob", AccessToken: "tok"}).Connect(ctx, st, nil)
	defer func() { _ = ss.Close() }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	res, _ := call(t, cs, "list_applications", nil)
	if !res.IsError || !strings.Contains(text(res), "HTTP 401") {
		t.Fatalf("401 should be a tool error: %s", text(res))
	}

	// Unreachable system manager.
	sm.Close()
	res, _ = call(t, cs, "list_services", nil)
	if !res.IsError || !strings.Contains(text(res), "not reachable") {
		t.Fatalf("connection error should be explained: %s", text(res))
	}
}

func TestForbiddenIsDenied(t *testing.T) {
	err := toolError(&oakapi.AuthError{StatusCode: http.StatusForbidden})
	if outcomeOf(err) != OutcomeDenied {
		t.Fatalf("403 should be denied, got %v", err)
	}
}
