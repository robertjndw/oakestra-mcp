// Package explain answers "where would this land and why" by running the
// placement port in internal/placement against live resource abstractor
// data, the same two stages Oakestra runs: the root scheduler picks a
// cluster from aggregated cluster documents, then that cluster's scheduler
// picks a worker. Nothing here deploys or reserves anything.
package explain

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// CandidateSource reads scheduling candidates. *candidates.Client
// implements it.
type CandidateSource interface {
	Root(ctx context.Context) ([]candidates.Candidate, error)
	Workers(ctx context.Context, cluster candidates.Candidate) ([]candidates.Candidate, error)
}

// Options tune a dry run.
type Options struct {
	// AllClusters also runs the node stage on every cluster that passes the
	// cluster-level checks, not only on the one the root would choose. It
	// answers "where else could this go" without changing the outcome,
	// which always follows the real scheduler.
	AllClusters bool
}

// Ref names a cluster or worker.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// NodeResult is the node stage for one cluster.
type NodeResult struct {
	Cluster    Ref                    `json:"cluster"`
	Nodes      []placement.Evaluation `json:"nodes"`
	ChosenNode *Ref                   `json:"chosen_node" jsonschema:"worker the cluster scheduler would pick, null if none fits"`
	Status     string                 `json:"status,omitempty" jsonschema:"scheduler wire status when no worker fits, e.g. NoActiveClusterWithCapacity, NoGpuCapacity, TargetClusterNotActive"`
	Error      string                 `json:"error,omitempty" jsonschema:"set when the cluster's resource abstractor could not be read; nodes is empty then"`

	// workers keeps the raw candidates so instances can be matched by IP.
	workers []candidates.Candidate
}

// Outcome is what a real deploy would do right now.
type Outcome struct {
	WouldDeploy bool `json:"would_deploy" jsonschema:"true only when both a cluster and a node are found; false when it would fail or the node level is unknown"`
	Cluster     *Ref `json:"cluster" jsonschema:"cluster the root scheduler would pick, null if none qualifies"`
	Node        *Ref `json:"node" jsonschema:"worker the cluster scheduler would pick, null if none fits or unknown"`
	// NodeLevelUnavailable means the chosen cluster's workers could not be
	// read, so only the cluster choice is known.
	NodeLevelUnavailable bool   `json:"node_level_unavailable,omitempty"`
	Status               string `json:"status,omitempty" jsonschema:"scheduler wire status the deploy would fail with"`
	Summary              string `json:"summary"`
}

// Result is a full dry run for one job.
type Result struct {
	Clusters      []placement.Evaluation `json:"clusters" jsonschema:"cluster-level evaluation of every cluster, in resource abstractor order"`
	ChosenCluster *Ref                   `json:"chosen_cluster" jsonschema:"cluster the root scheduler would pick, null if none qualifies"`
	ClusterStatus string                 `json:"cluster_status,omitempty" jsonschema:"scheduler wire status when no cluster qualifies"`
	NodeResults   []NodeResult           `json:"node_results" jsonschema:"node stage for the chosen cluster, plus every passing cluster with all_clusters"`
	Outcome       Outcome                `json:"outcome"`
	Caveats       []string               `json:"caveats"`
}

// NodeResultFor returns the node stage of a cluster, if it was run.
func (r Result) NodeResultFor(clusterID string) (NodeResult, bool) {
	for _, n := range r.NodeResults {
		if n.Cluster.ID == clusterID {
			return n, true
		}
	}
	return NodeResult{}, false
}

// maxConcurrentClusters bounds worker fetches with AllClusters. Each one
// can take the full client timeout when a cluster is behind NAT, so going
// fully serial would add those up, while unbounded fan-out would hammer a
// root with many clusters.
const maxConcurrentClusters = 4

// DryRun predicts where job would be placed if it were deployed now.
// Only a failure to read the root resource abstractor is an error; an
// unreachable cluster degrades that cluster's node stage instead.
func DryRun(ctx context.Context, src CandidateSource, job placement.Resources, opts Options) (Result, error) {
	return dryRun(ctx, src, job, opts, nil)
}

// dryRun also runs the node stage on the clusters in extra, which Explain
// uses to judge the clusters current instances live on.
func dryRun(ctx context.Context, src CandidateSource, job placement.Resources, opts Options, extra []string) (Result, error) {
	roots, err := src.Root(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reading clusters from the root resource abstractor: %w", err)
	}

	res := Result{Clusters: make([]placement.Evaluation, 0, len(roots)), NodeResults: []NodeResult{}}
	for _, c := range roots {
		r, err := decode(c)
		if err != nil {
			return Result{}, fmt.Errorf("cluster %s: %w", label(c), err)
		}
		res.Clusters = append(res.Clusters, placement.Evaluate(job, r, placement.LevelCluster))
	}

	win, status := placement.Choose(job, res.Clusters)
	if win >= 0 {
		res.ChosenCluster = refOf(roots[win], res.Clusters[win])
	} else {
		res.ClusterStatus = status
	}

	targets := map[int]bool{}
	if win >= 0 {
		targets[win] = true
	}
	for i, e := range res.Clusters {
		if (opts.AllClusters && e.Passed) || slices.Contains(extra, roots[i].ID) {
			targets[i] = true
		}
	}
	order := make([]int, 0, len(targets))
	for i := range targets {
		order = append(order, i)
	}
	slices.Sort(order)

	results := make([]NodeResult, len(order))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentClusters)
	for k, i := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[k] = nodeStage(ctx, src, job, roots[i], res.Clusters[i])
		}()
	}
	wg.Wait()
	res.NodeResults = results

	res.Outcome = outcome(res, job)
	res.Caveats = caveats(res, job)
	return res, nil
}

func nodeStage(ctx context.Context, src CandidateSource, job placement.Resources, cluster candidates.Candidate, ev placement.Evaluation) NodeResult {
	nr := NodeResult{Cluster: *refOf(cluster, ev), Nodes: []placement.Evaluation{}}
	ws, err := src.Workers(ctx, cluster)
	if err != nil {
		nr.Error = err.Error()
		return nr
	}
	for _, w := range ws {
		r, err := decode(w)
		if err != nil {
			nr.Error = fmt.Sprintf("worker %s: %v", label(w), err)
			nr.Nodes = []placement.Evaluation{}
			return nr
		}
		nr.Nodes = append(nr.Nodes, placement.Evaluate(job, r, placement.LevelNode))
	}
	nr.workers = ws
	win, status := placement.Choose(job, nr.Nodes)
	if win >= 0 {
		nr.ChosenNode = refOf(ws[win], nr.Nodes[win])
	} else {
		nr.Status = status
	}
	return nr
}

// decode reads the candidate document the way the scheduler does, from the
// raw resource abstractor JSON rather than the display fields.
func decode(c candidates.Candidate) (placement.Resources, error) {
	var r placement.Resources
	if len(c.Raw) == 0 {
		return r, fmt.Errorf("empty resource document")
	}
	if err := json.Unmarshal(c.Raw, &r); err != nil {
		return r, fmt.Errorf("resource document does not decode like the scheduler expects: %w", err)
	}
	return r, nil
}

func refOf(c candidates.Candidate, ev placement.Evaluation) *Ref {
	return &Ref{ID: c.ID, Name: cmp.Or(ev.CandidateName, c.Name)}
}

func label(c candidates.Candidate) string {
	if c.Name != "" {
		return fmt.Sprintf("%q", c.Name)
	}
	return fmt.Sprintf("with ID %q", c.ID)
}

func (r Ref) String() string { return cmp.Or(r.Name, r.ID) }

// Memoize caches Root and Workers answers for the lifetime of the returned
// source. Use one per tool call so several microservices of one SLA are
// judged against the same snapshot and clusters are not asked repeatedly.
func Memoize(src CandidateSource) CandidateSource {
	return &memo{src: src, workers: map[string]*memoEntry{}}
}

type memoEntry struct {
	once sync.Once
	cs   []candidates.Candidate
	err  error
}

type memo struct {
	src     CandidateSource
	root    memoEntry
	mu      sync.Mutex
	workers map[string]*memoEntry
}

func (m *memo) Root(ctx context.Context) ([]candidates.Candidate, error) {
	m.root.once.Do(func() { m.root.cs, m.root.err = m.src.Root(ctx) })
	return m.root.cs, m.root.err
}

func (m *memo) Workers(ctx context.Context, cluster candidates.Candidate) ([]candidates.Candidate, error) {
	// The IP is part of the key because Workers dials it; clusters whose
	// documents lack an _id would otherwise share one entry.
	key := cluster.ID + "|" + cluster.IP
	m.mu.Lock()
	e, ok := m.workers[key]
	if !ok {
		e = &memoEntry{}
		m.workers[key] = e
	}
	m.mu.Unlock()
	e.once.Do(func() { e.cs, e.err = m.src.Workers(ctx, cluster) })
	return e.cs, e.err
}
