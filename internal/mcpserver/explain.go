package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/robertjndw/oakestra-mcp/internal/explain"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
)

type explainPlacementIn struct {
	Service     string `json:"service" jsonschema:"service ID or service (microservice) name"`
	AllClusters bool   `json:"all_clusters,omitempty" jsonschema:"also evaluate the workers of every cluster that passes the cluster-level checks, not only the one the root would pick; slower, shows alternatives"`
}

type dryRunPlacementIn struct {
	SLA         any  `json:"sla" jsonschema:"a full Oakestra application SLA object (sla_version, customerID, applications[].microservices[]) or a single microservice object with at least virtualization, memory and vcpus"`
	AllClusters bool `json:"all_clusters,omitempty" jsonschema:"also evaluate the workers of every cluster that passes the cluster-level checks, not only the one the root would pick; slower, shows alternatives"`
}

type dryRunItem struct {
	Application  string `json:"application,omitempty"`
	Microservice string `json:"microservice,omitempty"`
	explain.Result
}

type dryRunPlacementOut struct {
	Results []dryRunItem `json:"results" jsonschema:"one dry run per microservice, in SLA order"`
}

const checksDoc = "Checks, in the order Oakestra's scheduler applies them: active (reported within 30 s), pinned_target " +
	"(a \"direct\" constraint naming a cluster or node), virtualization, cpu (vcpus against installed cores, not free " +
	"capacity), memory, gpu_count and vram (docker jobs only; free VRAM per GPU), csi_driver (volumes). Each check is " +
	"pass, fail, unknown (data missing, never blocks) or not_enforced. Architecture (arch), geo, latency, addons and " +
	"allowed-clusters constraints are reported as not_enforced because Oakestra ignores them: a service can land on " +
	"any architecture or location. The root picks a cluster from aggregates over its workers, then that cluster picks " +
	"a worker; among passing candidates the highest (100 - cpu_percent) + memory wins. Oakestra never retries another " +
	"cluster: if the chosen cluster has no fitting worker the deploy fails. Results are a snapshot, not a reservation. " +
	"If a cluster's resource abstractor is unreachable, its node stage carries an error and the outcome is cluster-level only."

func registerPlacementTools(r *registry) {
	if r.d.Oakestra != nil {
		addTool(r, ToolInfo{
			Name:  "explain_placement",
			Title: "Explain service placement",
			Description: "Explain why a deployed service runs where it runs, or why it is not running. Loads the service " +
				"(by ID or name, with the caller's permissions) and re-runs Oakestra's placement logic on live resource " +
				"data: every cluster and worker with each check's verdict and numbers, the cluster and worker the " +
				"scheduler would pick now, and for each instance its status, status detail, current cluster and worker, " +
				"and whether that spot still qualifies. Read-only, deploys nothing. Start with outcome.summary and " +
				"instances, then drill into failing checks. " + checksDoc,
			Kind: KindRead,
		}, r.explainPlacement)
	}

	addTool(r, ToolInfo{
		Name:  "dry_run_placement",
		Title: "Dry-run placement",
		Description: "Predict where an SLA would be placed if it were deployed now, without deploying anything. " +
			"Accepts a full application SLA or a single microservice object and returns one result per microservice: " +
			"every cluster and worker with each check's verdict and numbers, the cluster and worker the scheduler would " +
			"pick, the status a failing deploy would report, and caveats. Use it to check an SLA before deploying or to " +
			"find out which requirement blocks placement (e.g. lower vram or memory, change virtualization). " + checksDoc,
		Kind: KindRead,
	}, r.dryRunPlacement)
}

func (r *registry) explainPlacement(ctx context.Context, in explainPlacementIn) (explain.Explanation, string, error) {
	ref := strings.TrimSpace(in.Service)
	if ref == "" {
		return explain.Explanation{}, "", errors.New("service is required: pass a service ID or name (see list_services)")
	}
	c, err := r.client(ctx)
	if err != nil {
		return explain.Explanation{}, "", err
	}
	// Resolving with the caller's client is what enforces ownership: a
	// non-admin only finds their own services.
	s, _, err := c.Services.ResolveByNameOrID(ctx, ref)
	if err != nil {
		return explain.Explanation{}, "", err
	}
	raw, err := oakestra.GetServiceRaw(ctx, c, s.MicroserviceID)
	if err != nil {
		return explain.Explanation{}, "", err
	}
	ex, err := explain.Explain(ctx, explain.Memoize(r.d.Placement), raw, explain.Options{AllClusters: in.AllClusters})
	if err != nil {
		return explain.Explanation{}, "", err
	}
	return ex, ex.Summary + caveatText(ex.DryRun.Caveats), nil
}

func (r *registry) dryRunPlacement(ctx context.Context, in dryRunPlacementIn) (dryRunPlacementOut, string, error) {
	raw, err := slaJSON(in.SLA)
	if err != nil {
		return dryRunPlacementOut{}, "", err
	}
	jobs, err := explain.JobsFromSLA(raw)
	if err != nil {
		return dryRunPlacementOut{}, "", fmt.Errorf("the SLA is not usable for a dry run: %w", err)
	}

	src := explain.Memoize(r.d.Placement)
	out := dryRunPlacementOut{Results: make([]dryRunItem, 0, len(jobs))}
	var lines, caveats []string
	for _, j := range jobs {
		res, err := explain.DryRun(ctx, src, j.Job, explain.Options{AllClusters: in.AllClusters})
		if err != nil {
			return dryRunPlacementOut{}, "", err
		}
		out.Results = append(out.Results, dryRunItem{Application: j.Application, Microservice: j.Microservice, Result: res})
		lines = append(lines, jobLabel(j)+": "+res.Outcome.Summary)
		for _, c := range res.Caveats {
			if !slices.Contains(caveats, c) {
				caveats = append(caveats, c)
			}
		}
	}
	return out, strings.Join(lines, "\n") + caveatText(caveats), nil
}

// slaJSON accepts the SLA as an object and, because agents regularly do
// this, as a string containing the JSON.
func slaJSON(v any) (json.RawMessage, error) {
	if s, ok := v.(string); ok {
		if !json.Valid([]byte(s)) {
			return nil, errors.New("sla is a string that is not valid JSON; pass the SLA as a JSON object")
		}
		return json.RawMessage(s), nil
	}
	if v == nil {
		return nil, errors.New("sla is required: pass a full application SLA or a single microservice object")
	}
	return json.Marshal(v)
}

func jobLabel(j explain.NamedJob) string {
	switch {
	case j.Application != "" && j.Microservice != "":
		return j.Application + "/" + j.Microservice
	case j.Microservice != "":
		return j.Microservice
	case j.Application != "":
		return j.Application + "/(unnamed microservice)"
	}
	return "microservice"
}

func caveatText(cs []string) string {
	if len(cs) == 0 {
		return ""
	}
	return "\nCaveats: " + strings.Join(cs, " ")
}
