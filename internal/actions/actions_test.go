package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra/oakestratest"
)

const (
	rootRA   = `[{"_id":"c1","candidate_name":"cluster-a","ip":"127.0.0.1","active":true,"active_nodes":1,"vcpus":8,"memory":16000,"virtualization":["docker"]}]`
	workerRA = `[{"_id":"w1","candidate_name":"worker-1","architecture":"amd64","vcpus":8,"memory":16000,"virtualization":["docker"],"active":true}]`
)

var alice = auth.Principal{User: "alice", Roles: []string{auth.RoleApplicationProvider}, AccessToken: "tok"}

type harness struct {
	t     *testing.T
	fake  *oakestratest.Fake
	eng   *Engine
	store *MemoryStore
	clock time.Time
	c     *oakapi.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(workerRA)) }))
	t.Cleanup(worker.Close)
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(rootRA)) }))
	t.Cleanup(root.Close)
	_, port, _ := net.SplitHostPort(worker.Listener.Addr().String())
	p, _ := strconv.Atoi(port)

	h := &harness{t: t, fake: oakestratest.New(t, "tok"), store: NewMemoryStore(), clock: time.Unix(1_800_000_000, 0)}
	h.eng = NewEngine(h.store, []byte(strings.Repeat("k", 32)), 5*time.Minute, candidates.New(root.URL, p, nil))
	h.eng.now = func() time.Time { return h.clock }
	h.c = h.client("tok")
	return h
}

func (h *harness) client(token string) *oakapi.Client {
	c, err := oakapi.NewClient(oakapi.WithBaseURL(h.fake.URL), oakapi.WithToken(token))
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) confirm(p auth.Principal, token string) (Result, error) {
	return h.eng.Confirm(context.Background(), p, h.c, token)
}

func ops(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Op+" "+s.Target)
	}
	return out
}

func statuses(r Result) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Status)
	}
	return out
}

func TestTokenFormat(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	id := strings.Repeat("ab", 16)
	tok := makeToken(secret, id, "alice", "hash", 1234)
	pt, err := parseToken(tok)
	if err != nil || pt.actionID != id || pt.expires != 1234 {
		t.Fatalf("parse %q: %+v %v", tok, pt, err)
	}
	if !validMAC(secret, pt, "alice", "hash") {
		t.Fatal("own token rejected")
	}
	for name, ok := range map[string]bool{
		"other user": validMAC(secret, pt, "bob", "hash"),
		"other plan": validMAC(secret, pt, "alice", "hash2"),
		"other key":  validMAC([]byte(strings.Repeat("x", 32)), pt, "alice", "hash"),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
	// The length prefix keeps "a|b" + "c" from colliding with "a" + "b|c".
	if sign(secret, id, "a|b", "c", 1) == sign(secret, id, "a", "b|c", 1) {
		t.Error("field boundaries are ambiguous")
	}
	for _, bad := range []string{"", "a.b", id + ".x.y", "zz.1.y", id + ".1.y.z"} {
		if _, err := parseToken(bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSecretIsCreatedOnceAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	a, err := LoadOrCreateSecret(path)
	if err != nil || len(a) != secretLen {
		t.Fatalf("%d bytes, %v", len(a), err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	b, err := LoadOrCreateSecret(path)
	if err != nil || string(a) != string(b) {
		t.Fatal("secret not reused")
	}
	short := filepath.Join(t.TempDir(), "short")
	_ = os.WriteFile(short, []byte("x"), 0o600)
	if _, err := LoadOrCreateSecret(short); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestScaleUpThenDown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	prep, err := h.eng.PrepareScale(ctx, alice, h.c, "web", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(prep.Steps); !slices.Equal(got, []string{"deploy_instance svc1", "deploy_instance svc1"}) {
		t.Fatalf("steps %v", got)
	}
	if len(prep.DryRun) != 1 || !prep.DryRun[0].Outcome.WouldDeploy {
		t.Fatalf("scale up should carry a passing dry run: %+v", prep.DryRun)
	}
	if !strings.Contains(prep.Summary, "from 1 to 3") {
		t.Fatalf("summary %q", prep.Summary)
	}
	if h.fake.Count("POST /api/service/svc1/instance") != 0 {
		t.Fatal("prepare must not deploy")
	}
	res, err := h.confirm(alice, prep.ConfirmationToken)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StepDone || !slices.Equal(statuses(res), []string{"done", "done"}) {
		t.Fatalf("result %+v", res)
	}
	if got := h.fake.InstanceNumbers("svc1"); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("instances %v", got)
	}

	prep, err = h.eng.PrepareScale(ctx, alice, h.c, "svc1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(prep.Steps); !slices.Equal(got, []string{"undeploy_instance svc1#2", "undeploy_instance svc1#1"}) {
		t.Fatalf("scale down should remove the highest numbers first: %v", got)
	}
	if len(prep.DryRun) != 0 {
		t.Fatal("no dry run when scaling down")
	}
	if _, err := h.confirm(alice, prep.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.InstanceNumbers("svc1"); !slices.Equal(got, []int{0}) {
		t.Fatalf("instances %v", got)
	}

	if _, err := h.eng.PrepareScale(ctx, alice, h.c, "web", 1); err == nil || !strings.Contains(err.Error(), "nothing to do") {
		t.Fatalf("same count: %v", err)
	}
	if _, err := h.eng.PrepareScale(ctx, alice, h.c, "web", MaxInstances+1); err == nil {
		t.Fatal("replicas above the cap accepted")
	}
}

func TestConfirmRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prep := func() Prepared {
		t.Helper()
		p, err := h.eng.PrepareScale(ctx, alice, h.c, "web", 2)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := prep()
	tok := p.ConfirmationToken
	bob := auth.Principal{User: "bob", Roles: alice.Roles, AccessToken: "tok"}
	if res, err := h.confirm(bob, tok); !errors.Is(err, ErrWrongUser) || res.ActionID != p.ActionID {
		t.Fatalf("other user: %v %+v", err, res)
	}
	parts := strings.Split(tok, ".")
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	for name, bad := range map[string]string{
		"mac":     parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])),
		"expires": parts[0] + "." + strconv.FormatInt(exp+3600, 10) + "." + parts[2],
	} {
		if _, err := h.confirm(alice, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("tampered %s: %v", name, err)
		}
	}
	unknown := strings.Repeat("0", 32) + "." + parts[1] + "." + parts[2]
	if _, err := h.confirm(alice, unknown); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
	if h.fake.Count("POST /api/service/svc1/instance") != 0 {
		t.Fatal("refused confirms must not execute")
	}

	if _, err := h.confirm(alice, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := h.confirm(alice, tok); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if n := h.fake.Count("POST /api/service/svc1/instance"); n != 1 {
		t.Fatalf("replay executed again: %d deploys", n)
	}

	p, err := h.eng.PrepareScale(ctx, alice, h.c, "web", 3)
	if err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(5 * time.Minute)
	if _, err := h.confirm(alice, p.ConfirmationToken); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestConcurrentConfirmExecutesOnce(t *testing.T) {
	h := newHarness(t)
	p, err := h.eng.PrepareScale(context.Background(), alice, h.c, "web", 2)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, consumed int
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.confirm(alice, p.ConfirmationToken)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrConsumed):
				consumed++
			default:
				// A loser that re-plans after the winner deployed sees the
				// new instance, which is a changed plan.
				if !errors.Is(err, ErrPlanChanged) {
					t.Errorf("unexpected: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d confirms succeeded, want 1", ok)
	}
	if n := h.fake.Count("POST /api/service/svc1/instance"); n != 1 {
		t.Fatalf("%d deploys, want 1", n)
	}
}

func TestPlanDriftIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.eng.PrepareScale(ctx, alice, h.c, "web", 2)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else adds an instance in between: scaling to 2 now means no
	// deploy at all, so running the old plan would overshoot.
	h.fake.AddInstance("svc1", "RUNNING")
	if _, err := h.confirm(alice, p.ConfirmationToken); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("drift: %v", err)
	}
	if n := h.fake.Count("POST /api/service/svc1/instance"); n != 0 {
		t.Fatalf("drifted plan executed %d deploys", n)
	}

	// A status change alone is not drift.
	p, err = h.eng.PrepareUndeploy(ctx, alice, h.c, "web", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.fake.Mu.Lock()
	h.fake.Services["svc1"].Instances[1].Status = "FAILED"
	h.fake.Mu.Unlock()
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatalf("status change refused: %v", err)
	}
}

func TestPendingRequestWarns(t *testing.T) {
	h := newHarness(t)
	h.fake.Services["svc1"].Status = "REQUESTED"
	p, err := h.eng.PrepareScale(context.Background(), alice, h.c, "web", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "waiting for the scheduler") {
		t.Fatalf("warnings %v", p.Warnings)
	}
}

func shopSLA(appName string) json.RawMessage {
	return json.RawMessage(`{"application_name":"` + appName + `","application_namespace":"test","microservices":[
	 {"microservice_name":"api","microservice_namespace":"test","virtualization":"container","code":"nginx","memory":128,"vcpus":1},
	 {"microservice_name":"ml","microservice_namespace":"test","virtualization":"container","code":"torch","memory":128,"vcpus":1,"vram":8000}]}`)
}

func TestDeployApplication(t *testing.T) {
	h := newHarness(t)
	two := 2
	p, err := h.eng.PrepareDeploy(context.Background(), alice, h.c, shopSLA("store"), &two)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create_application test/store", "deploy_instance store/api", "deploy_instance store/api",
		"deploy_instance store/ml", "deploy_instance store/ml"}
	if got := ops(p.Steps); !slices.Equal(got, want) {
		t.Fatalf("steps %v", got)
	}
	if len(p.DryRun) != 2 || !p.DryRun[0].Outcome.WouldDeploy || p.DryRun[1].Outcome.WouldDeploy {
		t.Fatalf("dry run %+v", p.DryRun)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "store/ml would NOT be placed right now") ||
		!strings.Contains(p.Warnings[0], "Confirming still runs every step") {
		t.Fatalf("warnings %v", p.Warnings)
	}

	res, err := h.confirm(alice, p.ConfirmationToken)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(statuses(res), []string{"done", "done", "done", "done", "done"}) {
		t.Fatalf("result %+v", res)
	}
	var api, ml string
	for _, s := range h.fake.Services {
		switch s.Name {
		case "api":
			api = s.ID
		case "ml":
			ml = s.ID
		}
	}
	if len(h.fake.InstanceNumbers(api)) != 2 || len(h.fake.InstanceNumbers(ml)) != 2 {
		t.Fatalf("instances api %v ml %v", h.fake.InstanceNumbers(api), h.fake.InstanceNumbers(ml))
	}
	if !strings.Contains(res.Steps[0].Message, "api="+api) {
		t.Fatalf("create message %q", res.Steps[0].Message)
	}

	// The same SLA again clashes with the application just created.
	if _, err := h.eng.PrepareDeploy(context.Background(), alice, h.c, shopSLA("store"), nil); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("clash: %v", err)
	}
}

func TestDeployValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		sla  string
		want string
	}{
		"microservice only": {`{"microservice_name":"a","virtualization":"container","memory":1,"vcpus":1}`, "single microservice"},
		"bad name":          {`{"application_name":"my-app","application_namespace":"x","microservices":[{"microservice_name":"a","microservice_namespace":"x","code":"c","virtualization":"container","memory":1,"vcpus":1}]}`, "letters or digits"},
		"no code":           {`{"application_name":"a","application_namespace":"x","microservices":[{"microservice_name":"a","microservice_namespace":"x","virtualization":"container","memory":1,"vcpus":1}]}`, "code"},
		"no memory":         {`{"application_name":"a","application_namespace":"x","microservices":[{"microservice_name":"a","microservice_namespace":"x","code":"c","virtualization":"container","vcpus":1}]}`, "memory"},
		"version":           {`{"sla_version":"v9","applications":[]}`, "v9"},
	} {
		_, err := h.eng.PrepareDeploy(ctx, alice, h.c, json.RawMessage(tc.sla), nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := h.fake.Count("POST /api/application"); n != 0 {
		t.Fatalf("validation posted %d times", n)
	}
}

func TestDeployPartialFailure(t *testing.T) {
	h := newHarness(t)
	h.fake.FailDeployAt = 2
	one := 1
	p, err := h.eng.PrepareDeploy(context.Background(), alice, h.c, shopSLA("half"), &one)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.confirm(alice, p.ConfirmationToken)
	var ee *ExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("want ExecutionError, got %v", err)
	}
	if got := statuses(res); !slices.Equal(got, []string{"done", "done", "failed"}) {
		t.Fatalf("statuses %v", got)
	}
	if !strings.Contains(res.Steps[2].Message, "HTTP 500") || strings.Contains(res.Steps[2].Message, "http://") {
		t.Fatalf("step message %q", res.Steps[2].Message)
	}
	msg := err.Error()
	for _, want := range []string{"stopped at step 3 of 3", "nothing is rolled back", "[failed] deploy_instance half/ml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}

	// With more steps after the failure they are reported as skipped.
	h.fake.FailDeployAt = 3 // the next deploy request after the two above
	p, err = h.eng.PrepareScale(context.Background(), alice, h.c, "web", 4)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = h.confirm(alice, p.ConfirmationToken)
	if got := statuses(res); !slices.Equal(got, []string{"failed", "skipped", "skipped"}) {
		t.Fatalf("statuses %v", got)
	}
}

func TestUndeploy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.AddInstance("svc1", "RUNNING")
	h.fake.AddInstance("svc1", "RUNNING")

	one := 1
	p, err := h.eng.PrepareUndeploy(ctx, alice, h.c, "web", &one)
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(p.Steps); !slices.Equal(got, []string{"undeploy_instance svc1#1"}) {
		t.Fatalf("steps %v", got)
	}
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.InstanceNumbers("svc1"); !slices.Equal(got, []int{0, 2}) {
		t.Fatalf("instances %v", got)
	}

	if _, err := h.eng.PrepareUndeploy(ctx, alice, h.c, "web", &one); err == nil || !strings.Contains(err.Error(), "#0, #2") {
		t.Fatalf("gone instance: %v", err)
	}

	p, err = h.eng.PrepareUndeploy(ctx, alice, h.c, "svc1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(p.Steps); !slices.Equal(got, []string{"undeploy_instance svc1#2", "undeploy_instance svc1#0"}) {
		t.Fatalf("steps %v", got)
	}
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.InstanceNumbers("svc1"); len(got) != 0 {
		t.Fatalf("instances %v", got)
	}
	if _, err := h.eng.PrepareUndeploy(ctx, alice, h.c, "svc1", nil); err == nil || !strings.Contains(err.Error(), "no instances") {
		t.Fatalf("nothing to undeploy: %v", err)
	}
}

func TestDeleteApplication(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.eng.PrepareDelete(ctx, alice, h.c, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(p.Steps); !slices.Equal(got, []string{"delete_application app1"}) {
		t.Fatalf("steps %v", got)
	}
	if !strings.Contains(p.Steps[0].Detail, "web (svc1, instances #0)") || !strings.Contains(p.Summary, "cannot be undone") {
		t.Fatalf("plan should list what goes away: %+v", p.Plan)
	}

	// A new instance appearing changes what goes away.
	h.fake.AddInstance("svc1", "RUNNING")
	if _, err := h.confirm(alice, p.ConfirmationToken); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("drift: %v", err)
	}
	p, err = h.eng.PrepareDelete(ctx, alice, h.c, "app1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	if h.fake.Count("DELETE /api/application/app1") != 1 || h.fake.Apps["app1"] != nil {
		t.Fatal("application not deleted")
	}

	// A service that disappeared makes re-planning fail; nothing runs.
	if _, err := h.eng.PrepareDelete(ctx, alice, h.c, "ghost"); err == nil {
		t.Fatal("unknown application accepted")
	}
}

func TestReplanFailureKeepsToken(t *testing.T) {
	h := newHarness(t)
	p, err := h.eng.PrepareScale(context.Background(), alice, h.c, "web", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Confirm(context.Background(), alice, h.client("expired"), p.ConfirmationToken); err == nil ||
		!strings.Contains(err.Error(), "nothing was executed") {
		t.Fatalf("replan with a rejected token: %v", err)
	}
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatalf("token should survive a failed re-check: %v", err)
	}
}

func TestDeployWithoutAppListWarns(t *testing.T) {
	h := newHarness(t)
	h.fake.AppsListForbidden = true
	zero := 0
	p, err := h.eng.PrepareDeploy(context.Background(), alice, h.c, shopSLA("solo"), &zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "already taken") || len(p.DryRun) != 0 {
		t.Fatalf("warnings %v, dry run %d", p.Warnings, len(p.DryRun))
	}
	if got := ops(p.Steps); !slices.Equal(got, []string{"create_application test/solo"}) {
		t.Fatalf("steps %v", got)
	}
	if _, err := h.confirm(alice, p.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
}

func TestDeployNameClashCaughtForNonAdmin(t *testing.T) {
	h := newHarness(t)
	zero := 0
	// "shop"/"test" does not clash, "shop"/"prod" (the fake's seed app) does.
	sla := json.RawMessage(`{"sla_version":"v2.0","customerID":"x","applications":[{"applicationID":"","application_name":"shop",` +
		`"application_namespace":"prod","application_desc":"","microservices":[{"microservice_name":"w","microservice_namespace":"prod",` +
		`"virtualization":"container","memory":10,"vcpus":1,"code":"nginx","storage":0,"port":"80"}]}]}`)
	_, err := h.eng.PrepareDeploy(context.Background(), alice, h.c, sla, &zero)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("clash not detected: %v", err)
	}
}
