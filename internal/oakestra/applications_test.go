package oakestra_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra/oakestratest"
)

var (
	user  = auth.Principal{User: "alice", Roles: []string{auth.RoleApplicationProvider}, AccessToken: "tok"}
	admin = auth.Principal{User: "root", Roles: []string{auth.RoleAdmin}, AccessToken: "adm"}
)

func clientFor(t *testing.T, url string, p auth.Principal) *oakapi.Client {
	t.Helper()
	f, err := oakestra.NewFactory(url, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.ClientFor(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNonAdminListUsesPerUserEndpoint(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	c := clientFor(t, fake.URL, user)

	// The plain client call hits the admin-only route, which a non-admin gets 403 from.
	if _, _, err := c.Applications.List(context.Background()); err == nil {
		t.Fatal("admin-only listing accepted a non-admin token")
	}
	apps, err := oakestra.ListApplications(context.Background(), c, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].ApplicationName != "shop" {
		t.Fatalf("apps %+v", apps)
	}
	if fake.Count("GET /api/applications") != 1 || fake.Count("GET /api/applications/user1") != 1 {
		t.Fatalf("calls %v", fake.Calls)
	}
}

func TestAdminListUnchanged(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	fake.AdminToken = "adm"
	c := clientFor(t, fake.URL, admin)
	apps, err := oakestra.ListApplications(context.Background(), c, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("apps %+v", apps)
	}
	if fake.Count("GET /api/user/root") != 0 || fake.Count("GET /api/applications/user1") != 0 {
		t.Fatalf("admin must not touch the per-user route: %v", fake.Calls)
	}
}

func TestResolveApplication(t *testing.T) {
	fake := oakestratest.New(t, "tok")
	c := clientFor(t, fake.URL, user)
	ctx := context.Background()

	for _, ref := range []string{"shop", "app1"} {
		app, err := oakestra.ResolveApplication(ctx, c, user, ref)
		if err != nil || app.ApplicationID != "app1" {
			t.Fatalf("resolve %q: %v %+v", ref, err, app)
		}
	}

	var nf *oakapi.NotFoundError
	if _, err := oakestra.ResolveApplication(ctx, c, user, "ghost"); !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}

	fake.Mu.Lock()
	fake.Apps["app2"] = &oakestratest.App{ID: "app2", Name: "shop", Namespace: "dev"}
	fake.Mu.Unlock()
	var mm *oakapi.MultipleMatchesError
	if _, err := oakestra.ResolveApplication(ctx, c, user, "shop"); !errors.As(err, &mm) || len(mm.Matches) != 2 {
		t.Fatalf("want MultipleMatchesError with 2 matches, got %v", err)
	}
}

func TestUserIDAsPlainString(t *testing.T) {
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/alice":
			_, _ = w.Write([]byte(`{"_id":"abc123","name":"alice"}`))
		case "/api/applications/abc123":
			_, _ = w.Write([]byte(`[{"applicationID":"a","application_name":"x"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(sm.Close)
	apps, err := oakestra.ListApplications(context.Background(), clientFor(t, sm.URL, user), user)
	if err != nil || len(apps) != 1 {
		t.Fatalf("%v %+v", err, apps)
	}
}
