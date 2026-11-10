package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// fakeSource serves candidate documents from JSON, like the resource
// abstractors would. A cluster without an entry in workers is unreachable.
type fakeSource struct {
	root    string
	rootErr error
	workers map[string]string

	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    atomic.Int32
}

func (f *fakeSource) Root(context.Context) ([]candidates.Candidate, error) {
	if f.rootErr != nil {
		return nil, f.rootErr
	}
	return parse(f.root)
}

func (f *fakeSource) Workers(_ context.Context, c candidates.Candidate) ([]candidates.Candidate, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.inFlight++
	f.maxSeen = max(f.maxSeen, f.inFlight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	body, ok := f.workers[c.ID]
	if !ok {
		return nil, fmt.Errorf("%w: GET http://%s:10100/api/v1/resources/ timed out", candidates.ErrUnavailable, c.IP)
	}
	return parse(body)
}

func parse(s string) ([]candidates.Candidate, error) {
	var out []candidates.Candidate
	err := json.Unmarshal([]byte(s), &out)
	return out, err
}

func cluster(id, name string, active bool, memory int, extra string) string {
	return fmt.Sprintf(`{"_id":%q,"candidate_name":%q,"ip":"10.0.0.%d","active":%t,"vcpus":16,"memory":%d,"cpu_percent":10,"virtualization":["docker"]%s}`,
		id, name, len(id), active, memory, extra)
}

func worker(id, name string, memory int, extra string) string {
	return fmt.Sprintf(`{"_id":%q,"candidate_name":%q,"ip":"192.168.1.%d","active":true,"vcpus":4,"memory":%d,"cpu_percent":20,"virtualization":["docker"]%s}`,
		id, name, len(id), memory, extra)
}

func list(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func job(t *testing.T, ms string) placement.Resources {
	t.Helper()
	j, err := placement.JobFromMicroservice(json.RawMessage(ms))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

const smallJob = `{"microservice_name":"web","virtualization":"container","memory":1000,"vcpus":1}`

func threeClusters() *fakeSource {
	return &fakeSource{
		root: list(
			cluster("c1", "edge-1", true, 8000, ""),
			cluster("c2", "edge-2", true, 500, ""),
			cluster("c3", "edge-3", false, 99999, ""),
		),
		workers: map[string]string{
			"c1": list(worker("w1", "w1", 500, ""), worker("w3", "w3", 4000, ""), worker("w2", "w2", 3000, "")),
		},
	}
}

func TestDryRunWouldDeploy(t *testing.T) {
	src := threeClusters()
	res, err := DryRun(context.Background(), src, job(t, smallJob), Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcome
	if !o.WouldDeploy || o.Cluster.Name != "edge-1" || o.Node.Name != "w3" || o.Status != "" {
		t.Fatalf("outcome %+v", o)
	}
	if res.ChosenCluster.ID != "c1" || res.ClusterStatus != "" || len(res.Clusters) != 3 {
		t.Fatalf("cluster stage %+v", res)
	}
	if len(res.NodeResults) != 1 || len(res.NodeResults[0].Nodes) != 3 || res.NodeResults[0].ChosenNode.ID != "w3" {
		t.Fatalf("node stage %+v", res.NodeResults)
	}
	for _, want := range []string{
		"Would land on cluster edge-1, node w3.",
		"2 of 3 clusters fail: edge-2 (memory), edge-3 (active).",
		"1 of 3 nodes in edge-1 fail: w1 (memory).",
	} {
		if !strings.Contains(o.Summary, want) {
			t.Errorf("summary %q lacks %q", o.Summary, want)
		}
	}
	if len(res.Caveats) != 3 {
		t.Errorf("want only the 3 standard caveats, got %v", res.Caveats)
	}
	assertNoDashes(t, res)
}

func TestDryRunRootUnreachable(t *testing.T) {
	src := &fakeSource{rootErr: fmt.Errorf("%w: connection refused", candidates.ErrUnavailable)}
	_, err := DryRun(context.Background(), src, job(t, smallJob), Options{})
	if err == nil || !strings.Contains(err.Error(), "root resource abstractor") {
		t.Fatalf("want root error, got %v", err)
	}
}

func TestDryRunUnreachableCluster(t *testing.T) {
	src := threeClusters()
	delete(src.workers, "c1")
	res, err := DryRun(context.Background(), src, job(t, smallJob), Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcome
	if o.WouldDeploy || !o.NodeLevelUnavailable || o.Cluster.ID != "c1" || o.Node != nil {
		t.Fatalf("outcome %+v", o)
	}
	if nr := res.NodeResults[0]; nr.Error == "" || len(nr.Nodes) != 0 || nr.ChosenNode != nil {
		t.Fatalf("node result %+v", nr)
	}
	if !strings.Contains(o.Summary, "whether a node fits is unknown") {
		t.Errorf("summary %q", o.Summary)
	}
	if !containsAny(res.Caveats, "cluster-level only") {
		t.Errorf("caveats %v", res.Caveats)
	}
}

func TestDryRunPinnedNodeMissing(t *testing.T) {
	src := threeClusters()
	j := job(t, `{"virtualization":"container","memory":100,"vcpus":1,"constraints":[{"type":"direct","node":"ghost"}]}`)
	res, err := DryRun(context.Background(), src, j, Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcome
	if o.WouldDeploy || o.Cluster.Name != "edge-1" || o.Status != placement.StatusTargetClusterNotActive {
		t.Fatalf("outcome %+v", o)
	}
	if !strings.Contains(o.Summary, `node "ghost", pinned by a direct constraint, is not an active worker there`) {
		t.Errorf("summary %q", o.Summary)
	}
}

func TestDryRunGPUOnlyFailure(t *testing.T) {
	gpuJob := `{"virtualization":"container","memory":1000,"vcpus":1,"vram":8000}`

	t.Run("node level", func(t *testing.T) {
		src := &fakeSource{
			root: list(cluster("c1", "edge-1", true, 8000, `,"gpu_vram_free":[16000]`)),
			workers: map[string]string{
				"c1": list(worker("w1", "w1", 4000, ""), worker("w2", "w2", 4000, `,"gpu_vram_free":[4000]`)),
			},
		}
		res, err := DryRun(context.Background(), src, job(t, gpuJob), Options{})
		if err != nil {
			t.Fatal(err)
		}
		o := res.Outcome
		if o.WouldDeploy || o.Status != placement.StatusNoGpuCapacity || res.ChosenCluster.Name != "edge-1" {
			t.Fatalf("outcome %+v", o)
		}
		if !strings.Contains(o.Summary, "w1 (gpu_count)") || !strings.Contains(o.Summary, "w2 (vram)") {
			t.Errorf("summary %q", o.Summary)
		}
	})

	t.Run("cluster level", func(t *testing.T) {
		src := &fakeSource{root: list(cluster("c1", "edge-1", true, 8000, ""))}
		res, err := DryRun(context.Background(), src, job(t, gpuJob), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ChosenCluster != nil || res.ClusterStatus != placement.StatusNoGpuCapacity || len(res.NodeResults) != 0 {
			t.Fatalf("result %+v", res)
		}
		if !strings.Contains(res.Outcome.Summary, "no cluster passes the cluster-level checks (NoGpuCapacity)") {
			t.Errorf("summary %q", res.Outcome.Summary)
		}
	})
}

func TestDryRunInactiveCluster(t *testing.T) {
	src := &fakeSource{root: list(cluster("c3", "edge-3", false, 99999, ""))}
	res, err := DryRun(context.Background(), src, job(t, smallJob), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChosenCluster != nil || res.Outcome.Status != placement.StatusTargetClusterNotActive {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if !strings.Contains(res.Outcome.Summary, "no cluster is active") || !strings.Contains(res.Outcome.Summary, "edge-3 (active)") {
		t.Errorf("summary %q", res.Outcome.Summary)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"chosen_cluster":null`) {
		t.Errorf("chosen_cluster should be null: %s", b)
	}
}

func TestDryRunNoRetryCaveat(t *testing.T) {
	// edge-1 wins on aggregate memory, but its memory is spread over small
	// workers; edge-2 has a single big enough worker the root never tries.
	src := &fakeSource{
		root: list(cluster("c1", "edge-1", true, 9000, ""), cluster("c2", "edge-2", true, 5000, "")),
		workers: map[string]string{
			"c1": list(worker("w1", "w1", 3000, ""), worker("w2", "w2", 3000, ""), worker("w3", "w3", 3000, "")),
			"c2": list(worker("big", "big", 5000, "")),
		},
	}
	j := job(t, `{"virtualization":"container","memory":4000,"vcpus":1}`)

	res, err := DryRun(context.Background(), src, j, Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcome
	if o.WouldDeploy || o.Cluster.Name != "edge-1" || o.Status != placement.StatusNoActiveClusterWithCapacity {
		t.Fatalf("outcome %+v", o)
	}
	if !strings.Contains(o.Summary, "Oakestra does not retry another cluster, even though edge-2 also passes") {
		t.Errorf("summary %q", o.Summary)
	}
	if !containsAny(res.Caveats, "does not retry another cluster: the deploy fails on edge-1 instead of moving to edge-2") ||
		!containsAny(res.Caveats, "Run again with all_clusters") {
		t.Errorf("caveats %v", res.Caveats)
	}

	res, err = DryRun(context.Background(), src, j, Options{AllClusters: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome.WouldDeploy || res.Outcome.Cluster.Name != "edge-1" {
		t.Fatalf("all_clusters must not change the outcome: %+v", res.Outcome)
	}
	if !containsAny(res.Caveats, "edge-2 (which has a fitting node, big)") || containsAny(res.Caveats, "Run again") {
		t.Errorf("caveats %v", res.Caveats)
	}
	if !strings.Contains(res.Outcome.Summary, "Cluster edge-2 would pick node big if it were chosen.") {
		t.Errorf("summary %q", res.Outcome.Summary)
	}
}

func TestDryRunAllClustersBounded(t *testing.T) {
	var clusters []string
	src := &fakeSource{workers: map[string]string{}}
	for i := range 10 {
		id := fmt.Sprintf("c%02d", i)
		clusters = append(clusters, cluster(id, "edge-"+id, true, 8000+i, ""))
		if i != 3 {
			src.workers[id] = list(worker("w"+id, "w-"+id, 4000, ""))
		}
	}
	src.root = list(clusters...)

	res, err := DryRun(context.Background(), src, job(t, smallJob), Options{AllClusters: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NodeResults) != 10 || src.calls.Load() != 10 {
		t.Fatalf("want 10 node results and fetches, got %d/%d", len(res.NodeResults), src.calls.Load())
	}
	if src.maxSeen > maxConcurrentClusters {
		t.Fatalf("%d concurrent fetches, bound is %d", src.maxSeen, maxConcurrentClusters)
	}
	for i, nr := range res.NodeResults {
		if nr.Cluster.ID != fmt.Sprintf("c%02d", i) {
			t.Fatalf("node results not in resource abstractor order: %v", nr.Cluster)
		}
	}
	if nr, _ := res.NodeResultFor("c03"); nr.Error == "" {
		t.Fatal("unreachable cluster should carry an error")
	}
	if !res.Outcome.WouldDeploy || res.Outcome.Cluster.ID != "c09" {
		t.Fatalf("outcome %+v", res.Outcome)
	}
}

func TestNotEnforcedCaveats(t *testing.T) {
	src := threeClusters()
	j := job(t, `{"virtualization":"container","memory":100,"vcpus":1,"arch":["arm64"],
		"constraints":[{"type":"geo","location":"48.1,11.5","threshold":10},{"type":"latency","area":"x","threshold":5}]}`)
	res, err := DryRun(context.Background(), src, j, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"architecture arm64", "geo constraint", "latency constraint"} {
		if !containsAny(res.Caveats, want) {
			t.Errorf("caveats %v lack %q", res.Caveats, want)
		}
	}
}

func TestMemoize(t *testing.T) {
	src := threeClusters()
	m := Memoize(src)
	for range 3 {
		if _, err := DryRun(context.Background(), m, job(t, smallJob), Options{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("workers fetched %d times, want 1", n)
	}
}

func TestExplainAnnotatesInstances(t *testing.T) {
	src := threeClusters()
	src.workers["c2"] = list(worker("x1", "x1", 500, ""))
	svc := `{"_id":{"$oid":"svc1"},"microserviceID":"svc1","microservice_name":"web","app_name":"shop",
		"status":"RUNNING","status_detail":"","virtualization":"docker","memory":1000,"vcpus":1,
		"instance_list":[
		 {"instance_number":0,"status":"RUNNING","cluster_id":"c1","worker_id":"w3","host_ip":"192.168.1.2"},
		 {"instance_number":"1","status":"RUNNING","cluster_id":"c1","host_ip":"192.168.1.2"},
		 {"instance_number":2,"status":"FAILED","status_detail":"NoActiveClusterWithCapacity","cluster_id":"c2","worker_id":"x1"},
		 {"instance_number":3,"status":"RUNNING","cluster_id":"gone","worker_id":"w9"},
		 {"instance_number":4,"status":"REQUESTED"}]}`

	ex, err := Explain(context.Background(), src, json.RawMessage(svc), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Service.ID != "svc1" || ex.Service.Name != "web" || ex.Service.Application != "shop" || ex.Service.Status != "RUNNING" {
		t.Fatalf("service %+v", ex.Service)
	}
	if len(ex.Instances) != 5 {
		t.Fatalf("instances %+v", ex.Instances)
	}
	// c2 is not chosen but hosts an instance, so its node stage ran too.
	if _, ok := ex.DryRun.NodeResultFor("c2"); !ok {
		t.Fatal("node stage missing for the cluster of an instance")
	}

	i0 := ex.Instances[0]
	if i0.Cluster.Name != "edge-1" || i0.Worker.Name != "w3" || !*i0.ClusterPasses || !i0.ClusterStillChosen ||
		!*i0.NodePasses || !*i0.NodeStillChosen {
		t.Errorf("instance 0 %+v", i0)
	}
	// All workers of edge-1 share 192.168.1.2 in this fixture, as behind a NAT.
	i1 := ex.Instances[1]
	if i1.Number != 1 || i1.Worker != nil || i1.Note != "the cluster no longer reports this worker" {
		t.Errorf("instance 1 with an ambiguous host_ip should not be resolved: %+v", i1)
	}
	i2 := ex.Instances[2]
	if i2.Cluster.Name != "edge-2" || *i2.ClusterPasses || i2.ClusterStillChosen || *i2.NodePasses || *i2.NodeStillChosen {
		t.Errorf("instance 2 %+v", i2)
	}
	if i3 := ex.Instances[3]; i3.ClusterPasses != nil || !strings.Contains(i3.Note, "no longer reports this cluster") {
		t.Errorf("instance 3 %+v", i3)
	}
	if i4 := ex.Instances[4]; i4.Cluster != nil || i4.Note != "not assigned to a cluster yet" {
		t.Errorf("instance 4 %+v", i4)
	}
	for _, want := range []string{
		"Service web (svc1) is RUNNING.",
		"Instance 0 RUNNING on cluster edge-1, node w3; the scheduler would still pick exactly this spot.",
		"Instance 2 FAILED (NoActiveClusterWithCapacity) on cluster edge-2, node x1; that cluster fails the cluster-level checks now.",
		"Dry run now: Would land on cluster edge-1, node w3.",
	} {
		if !strings.Contains(ex.Summary, want) {
			t.Errorf("summary %q lacks %q", ex.Summary, want)
		}
	}
	if !containsAny(ex.DryRun.Caveats, "Instances already running") {
		t.Errorf("caveats %v", ex.DryRun.Caveats)
	}
	assertNoDashes(t, ex)
}

func TestExplainResolvesWorkerByUniqueHostIP(t *testing.T) {
	src := &fakeSource{
		root: list(cluster("c1", "edge-1", true, 8000, "")),
		workers: map[string]string{
			"c1": list(`{"_id":"w1","candidate_name":"alpha","ip":"10.1.1.1","active":true,"vcpus":4,"memory":4000,"virtualization":["docker"]}`),
		},
	}
	svc := `{"microserviceID":"s","virtualization":"docker","memory":10,"vcpus":1,
		"instance_list":[{"instance_number":0,"status":"RUNNING","cluster_id":"c1","host_ip":"10.1.1.1"}]}`
	ex, err := Explain(context.Background(), src, json.RawMessage(svc), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if w := ex.Instances[0].Worker; w == nil || w.ID != "w1" || w.Name != "alpha" {
		t.Fatalf("worker %+v", w)
	}
}

func TestExplainUnreachableInstanceCluster(t *testing.T) {
	src := threeClusters()
	svc := `{"microserviceID":"s","virtualization":"docker","memory":10,"vcpus":1,
		"instance_list":[{"instance_number":0,"status":"RUNNING","cluster_id":"c2","worker_id":"x"}]}`
	ex, err := Explain(context.Background(), src, json.RawMessage(svc), Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := ex.Instances[0]
	if in.NodePasses != nil || !strings.Contains(in.Note, "could not be read") || in.Worker.ID != "x" {
		t.Fatalf("instance %+v", in)
	}
}

func TestExplainRejectsUnusableDocument(t *testing.T) {
	_, err := Explain(context.Background(), threeClusters(), json.RawMessage(`{"microserviceID":"s"}`), Options{})
	if err == nil || !strings.Contains(err.Error(), "virtualization") {
		t.Fatalf("got %v", err)
	}
}

func TestJobsFromSLA(t *testing.T) {
	full := `{"sla_version":"v2.0","customerID":"Admin","applications":[
	 {"application_name":"shop","microservices":[
	  {"microservice_name":"web","virtualization":"container","memory":100,"vcpus":1},
	  {"microservice_name":"gpu","virtualization":"container","memory":100,"vcpus":1,"vram":4000}]},
	 {"application_name":"blog","microservices":[
	  {"microservice_name":"db","virtualization":"container","memory":200,"vcpus":2}]}]}`
	jobs, err := JobsFromSLA(json.RawMessage(full))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || jobs[0].Application != "shop" || jobs[1].Microservice != "gpu" || jobs[2].Application != "blog" {
		t.Fatalf("jobs %+v", jobs)
	}
	if jobs[0].Job.Virtualization[0] != "docker" || jobs[1].Job.Vgpus != 1 {
		t.Fatalf("SLA rewrites not applied: %+v", jobs[:2])
	}

	jobs, err = JobsFromSLA(json.RawMessage(`{"microservice_name":"solo","virtualization":"unikernel","memory":64,"vcpus":1,"arch":"amd64"}`))
	if err != nil || len(jobs) != 1 || jobs[0].Microservice != "solo" || jobs[0].Application != "" {
		t.Fatalf("single microservice: %+v %v", jobs, err)
	}

	jobs, err = JobsFromSLA(json.RawMessage(`{"application_name":"a","microservices":[{"virtualization":"container","memory":1,"vcpus":1}]}`))
	if err != nil || len(jobs) != 1 || jobs[0].Application != "a" {
		t.Fatalf("single application: %+v %v", jobs, err)
	}

	bad := `{"applications":[{"application_name":"shop","microservices":[
	  {"microservice_name":"web","virtualization":"container","vcpus":1},
	  {"microservice_name":"ok","virtualization":"container","memory":1,"vcpus":1},
	  {"microservice_name":"uk","virtualization":"unikernel","memory":1,"vcpus":1}]}]}`
	_, err = JobsFromSLA(json.RawMessage(bad))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{`applications[0] ("shop") microservices[0] "web"`, "memory is missing", `microservices[2] "uk"`, "arch is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	for _, in := range []string{`[]`, `"x"`, `null`, `{"applications":[]}`, `{"applications":{}}`, `{"applications":[{"microservices":[]}]}`} {
		if _, err := JobsFromSLA(json.RawMessage(in)); err == nil {
			t.Errorf("%s should be rejected", in)
		}
	}
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func assertNoDashes(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if strings.ContainsAny(string(b), "\u2013\u2014") {
		t.Errorf("output contains an en or em dash: %s", b)
	}
}
