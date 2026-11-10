package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra/oakestratest"
)

var writeTools = []string{"prepare_deploy_application", "prepare_scale_service", "prepare_undeploy_service",
	"prepare_delete_application", "confirm_action"}

func writeDeps(t *testing.T, fakeURL string, enable bool) (Deps, *memRecorder) {
	t.Helper()
	f, err := oakestra.NewFactory(fakeURL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := &memRecorder{}
	eng := actions.NewEngine(actions.NewMemoryStore(), []byte(strings.Repeat("k", 32)), 5*time.Minute, nil)
	return Deps{Oakestra: f, Actions: eng, Recorder: rec, EnableWrites: enable}, rec
}

func connect(t *testing.T, d Deps, p auth.Principal) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := NewServer(d, p).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listTools(t *testing.T, cs *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		m[tool.Name] = tool
	}
	return m
}

func TestWriteToolsHiddenWhenDisabledButInCatalog(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	d, _ := writeDeps(t, fake.URL, false)

	cat := map[string]ToolInfo{}
	for _, i := range Catalog(d) {
		cat[i.Name] = i
	}
	for _, name := range writeTools {
		if info, ok := cat[name]; !ok || info.Kind != KindWrite || info.Description == "" {
			t.Errorf("catalog entry for %s: %+v", name, info)
		}
	}
	if !strings.Contains(cat["prepare_scale_service"].Description, "5 minutes") {
		t.Errorf("description should name the TTL: %s", cat["prepare_scale_service"].Description)
	}

	tools := listTools(t, connect(t, d, alice()))
	for _, name := range writeTools {
		if tools[name] != nil {
			t.Errorf("%s served with writes disabled", name)
		}
	}
	if tools["list_applications"] == nil {
		t.Error("read tools should still be served")
	}

	d.EnableWrites = true
	tools = listTools(t, connect(t, d, alice()))
	for _, name := range writeTools {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("%s not served with writes enabled", name)
		}
		a := tool.Annotations
		if name == "confirm_action" {
			if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.IdempotentHint {
				t.Errorf("confirm_action annotations %+v", a)
			}
		} else if !a.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only: it only plans", name)
		}
	}
}

func alice() auth.Principal {
	return auth.Principal{User: "alice", Roles: []string{auth.RoleApplicationProvider}, AccessToken: "tok"}
}

func TestWriteToolsNeedRole(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	d, rec := writeDeps(t, fake.URL, true)
	infra := auth.Principal{User: "ivan", Roles: []string{auth.RoleInfrastructureProvider}, AccessToken: "tok"}
	cs := connect(t, d, infra)

	res, _ := call(t, cs, "prepare_scale_service", map[string]any{"service": "web", "replicas": 2})
	if !res.IsError || !strings.Contains(text(res), "Application_Provider") {
		t.Fatalf("infra provider allowed: %s", text(res))
	}
	res, _ = call(t, cs, "confirm_action", map[string]any{"confirmation_token": "x"})
	if !res.IsError {
		t.Fatal("confirm allowed without role")
	}
	for _, c := range rec.calls {
		if c.Outcome != OutcomeDenied || c.Kind != KindWrite {
			t.Errorf("recorded as %+v, want denied write", c)
		}
	}
	if len(rec.calls) != 2 || fake.Count("GET /api/service/svc1") != 0 {
		t.Fatal("a denied call must not reach Oakestra")
	}
}

func TestPrepareConfirmFlow(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	d, rec := writeDeps(t, fake.URL, true)
	cs := connect(t, d, alice())

	res, out := call(t, cs, "prepare_scale_service", map[string]any{"service": "web", "replicas": 2})
	if res.IsError {
		t.Fatal(text(res))
	}
	token, _ := out["confirmation_token"].(string)
	actionID, _ := out["action_id"].(string)
	if token == "" || actionID == "" || out["kind"] != "scale_service" || len(out["steps"].([]any)) != 1 {
		t.Fatalf("prepare output %v", out)
	}
	summary := res.Content[0].(*sdk.TextContent).Text
	for _, want := range []string{"from 1 to 2", "1. deploy_instance svc1", "Nothing has changed yet", "explicit approval"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if fake.Count("POST /api/service/svc1/instance") != 0 {
		t.Fatal("prepare deployed")
	}

	res, out = call(t, cs, "confirm_action", map[string]any{"confirmation_token": token})
	if res.IsError {
		t.Fatal(text(res))
	}
	if out["status"] != "done" || out["action_id"] != actionID {
		t.Fatalf("confirm output %v", out)
	}
	if got := fake.InstanceNumbers("svc1"); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("instances %v", got)
	}

	res, _ = call(t, cs, "confirm_action", map[string]any{"confirmation_token": token})
	if !res.IsError || !strings.Contains(text(res), "already confirmed") {
		t.Fatalf("replay: %s", text(res))
	}

	// prepare and confirm rows share the action ID in the activity log.
	if len(rec.calls) != 3 {
		t.Fatalf("%d calls recorded", len(rec.calls))
	}
	for i, c := range rec.calls {
		if c.ActionID != actionID || c.Kind != KindWrite {
			t.Errorf("call %d (%s): action %q kind %s", i, c.Tool, c.ActionID, c.Kind)
		}
	}
	if rec.calls[0].Outcome != OutcomeOK || rec.calls[1].Outcome != OutcomeOK || rec.calls[2].Outcome != OutcomeError {
		t.Errorf("outcomes %s %s %s", rec.calls[0].Outcome, rec.calls[1].Outcome, rec.calls[2].Outcome)
	}

	// Another user's confirm is denied and still linked to the action.
	res, out = call(t, cs, "prepare_undeploy_service", map[string]any{"service": "web", "instance_number": 1})
	if res.IsError {
		t.Fatal(text(res))
	}
	bob := auth.Principal{User: "bob", Roles: []string{auth.RoleAdmin}, AccessToken: "tok"}
	res, _ = call(t, connect(t, d, bob), "confirm_action", map[string]any{"confirmation_token": out["confirmation_token"]})
	last := rec.calls[len(rec.calls)-1]
	if !res.IsError || last.Outcome != OutcomeDenied || last.ActionID != out["action_id"] {
		t.Fatalf("other user: %s, recorded %+v", text(res), last)
	}
}

func TestConfirmRefusesChangedPlan(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	d, _ := writeDeps(t, fake.URL, true)
	cs := connect(t, d, alice())

	_, out := call(t, cs, "prepare_delete_application", map[string]any{"application": "shop"})
	fake.AddInstance("svc1", "RUNNING")
	res, _ := call(t, cs, "confirm_action", map[string]any{"confirmation_token": out["confirmation_token"]})
	if !res.IsError || !strings.Contains(text(res), "Prepare the action again") {
		t.Fatalf("drift: %s", text(res))
	}
	if fake.Count("DELETE /api/application/app1") != 0 {
		t.Fatal("changed plan executed")
	}
}

func TestConfirmReportsPartialProgress(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	fake.FailDeployAt = 2
	d, rec := writeDeps(t, fake.URL, true)
	cs := connect(t, d, alice())

	_, out := call(t, cs, "prepare_scale_service", map[string]any{"service": "svc1", "replicas": 4})
	res, _ := call(t, cs, "confirm_action", map[string]any{"confirmation_token": out["confirmation_token"]})
	txt := text(res)
	for _, want := range []string{"stopped at step 2 of 3", "[done] deploy_instance", "[failed] deploy_instance", "[skipped] deploy_instance"} {
		if !res.IsError || !strings.Contains(txt, want) {
			t.Errorf("lacks %q:\n%s", want, txt)
		}
	}
	if last := rec.calls[len(rec.calls)-1]; last.Outcome != OutcomeError || last.ActionID != out["action_id"] {
		t.Fatalf("recorded %+v", last)
	}
}
