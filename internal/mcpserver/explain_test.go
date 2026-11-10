package mcpserver

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
)

func TestPlacementToolsGating(t *testing.T) {
	cands := candidates.New("http://x", 1, nil)
	names := func(d Deps) map[string]bool {
		m := map[string]bool{}
		for _, i := range Catalog(d) {
			m[i.Name] = true
		}
		return m
	}
	if got := names(Deps{Placement: cands}); !got["dry_run_placement"] || got["explain_placement"] {
		t.Fatalf("placement only should offer dry_run_placement alone: %v", got)
	}
	if got := names(Deps{Placement: cands, Oakestra: &oakestra.Factory{}}); !got["explain_placement"] {
		t.Fatalf("placement and oakestra should offer explain_placement: %v", got)
	}
	if got := names(Deps{Candidates: cands, Oakestra: &oakestra.Factory{}}); got["dry_run_placement"] || got["explain_placement"] {
		t.Fatalf("without Placement no placement tools: %v", got)
	}
}

func TestExplainPlacementTool(t *testing.T) {
	cs, rec := setup(t)
	for _, ref := range []string{"web", "svc1"} {
		res, out := call(t, cs, "explain_placement", map[string]any{"service": ref})
		if res.IsError {
			t.Fatal(text(res))
		}
		svc := out["service"].(map[string]any)
		if svc["id"] != "svc1" || svc["status"] != "RUNNING" {
			t.Fatalf("service %v", svc)
		}
		inst := out["instances"].([]any)[0].(map[string]any)
		if inst["worker"].(map[string]any)["name"] != "worker-1" || inst["node_still_chosen"] != true {
			t.Fatalf("instance %v", inst)
		}
		o := out["dry_run"].(map[string]any)["outcome"].(map[string]any)
		if o["would_deploy"] != true {
			t.Fatalf("outcome %v", o)
		}
		txt := text(res)
		for _, want := range []string{
			"Instance 0 RUNNING on cluster cluster-a, node worker-1; the scheduler would still pick exactly this spot.",
			"Would land on cluster cluster-a, node worker-1.",
			"1 of 2 clusters fail: cluster-b (active",
			"Caveats: Based on a snapshot",
		} {
			if !strings.Contains(txt, want) {
				t.Errorf("text lacks %q:\n%s", want, txt)
			}
		}
	}

	res, _ := call(t, cs, "explain_placement", map[string]any{"service": "ghost"})
	if !res.IsError || !strings.Contains(text(res), "list_services") {
		t.Fatalf("unknown service: %s", text(res))
	}
	res, _ = call(t, cs, "explain_placement", map[string]any{"service": " "})
	if !res.IsError {
		t.Fatal("empty service should fail")
	}
	if last := rec.calls[len(rec.calls)-1]; last.Tool != "explain_placement" || last.Kind != KindRead {
		t.Fatalf("not recorded: %+v", last)
	}
}

func TestDryRunPlacementTool(t *testing.T) {
	cs, _ := setup(t)
	sla := map[string]any{
		"sla_version": "v2.0", "customerID": "Admin",
		"applications": []any{map[string]any{
			"application_name": "shop",
			"microservices": []any{
				map[string]any{"microservice_name": "web", "virtualization": "container", "memory": 256, "vcpus": 1},
				map[string]any{"microservice_name": "ml", "virtualization": "container", "memory": 256, "vcpus": 1, "vram": 8000},
			},
		}},
	}
	res, out := call(t, cs, "dry_run_placement", map[string]any{"sla": sla, "all_clusters": true})
	if res.IsError {
		t.Fatal(text(res))
	}
	results := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results %v", out)
	}
	web := results[0].(map[string]any)
	if web["microservice"] != "web" || web["outcome"].(map[string]any)["would_deploy"] != true {
		t.Fatalf("web %v", web)
	}
	ml := results[1].(map[string]any)
	if ml["cluster_status"] != "NoGpuCapacity" || ml["chosen_cluster"] != nil {
		t.Fatalf("ml %v", ml)
	}
	txt := text(res)
	if !strings.Contains(txt, "shop/web: Would land on cluster cluster-a, node worker-1.") ||
		!strings.Contains(txt, "shop/ml: Would not deploy: no cluster passes the cluster-level checks (NoGpuCapacity).") {
		t.Fatalf("text:\n%s", txt)
	}
	// The second content block is the JSON copy, so only look at the summary.
	summary := res.Content[0].(*sdk.TextContent).Text
	if strings.Count(summary, "Based on a snapshot") != 1 {
		t.Errorf("caveats should be deduplicated:\n%s", txt)
	}

	// Agents often send the SLA as a string.
	res, out = call(t, cs, "dry_run_placement", map[string]any{
		"sla": `{"microservice_name":"solo","virtualization":"container","memory":10,"vcpus":1}`,
	})
	if res.IsError || len(out["results"].([]any)) != 1 {
		t.Fatalf("string SLA: %s", text(res))
	}

	res, _ = call(t, cs, "dry_run_placement", map[string]any{"sla": map[string]any{"virtualization": "container", "vcpus": 1}})
	if !res.IsError || !strings.Contains(text(res), "memory is missing") {
		t.Fatalf("invalid SLA: %s", text(res))
	}
}

func TestDryRunPlacementRootUnavailable(t *testing.T) {
	cands := candidates.New("http://127.0.0.1:1", 1, nil)
	st, ct := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := NewServer(Deps{Placement: cands}, auth.Principal{User: "bob"}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	res, _ := call(t, cs, "dry_run_placement", map[string]any{
		"sla": map[string]any{"virtualization": "container", "memory": 1, "vcpus": 1},
	})
	if !res.IsError || !strings.Contains(text(res), "resource data is unavailable") {
		t.Fatalf("root unavailable: %s", text(res))
	}
}
