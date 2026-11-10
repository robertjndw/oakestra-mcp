package explain

import (
	"fmt"
	"strings"

	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// maxListed caps how many failing candidates a summary names, so a large
// deployment does not turn one sentence into a wall of text. The structured
// result still has all of them.
const maxListed = 8

func outcome(res Result, job placement.Resources) Outcome {
	var b strings.Builder
	o := Outcome{}

	if res.ChosenCluster == nil {
		o.Status = res.ClusterStatus
		switch {
		case len(res.Clusters) == 0:
			fmt.Fprintf(&b, "Would not deploy: the root resource abstractor reports no clusters (%s).", o.Status)
		case pinnedMissing(job, placement.LevelCluster, res.Clusters):
			name, _ := placement.PinnedName(job, placement.LevelCluster)
			fmt.Fprintf(&b, "Would not deploy: cluster %q, pinned by a direct constraint, is not an active cluster (%s).", name, o.Status)
		case o.Status == placement.StatusTargetClusterNotActive:
			fmt.Fprintf(&b, "Would not deploy: no cluster is active (%s).", o.Status)
		default:
			fmt.Fprintf(&b, "Would not deploy: no cluster passes the cluster-level checks (%s).", o.Status)
		}
		writeFailures(&b, res.Clusters, "clusters")
		o.Summary = b.String()
		return o
	}

	o.Cluster = res.ChosenCluster
	nr, _ := res.NodeResultFor(res.ChosenCluster.ID)
	switch {
	case nr.Error != "":
		o.NodeLevelUnavailable = true
		fmt.Fprintf(&b, "The root would pick cluster %s, but its workers could not be read (%s), so whether a node fits is unknown.",
			res.ChosenCluster, nr.Error)
		writeFailures(&b, res.Clusters, "clusters")
	case nr.ChosenNode == nil:
		o.Status = nr.Status
		if pinnedMissing(job, placement.LevelNode, nr.Nodes) {
			name, _ := placement.PinnedName(job, placement.LevelNode)
			fmt.Fprintf(&b, "Would fail: the root would pick cluster %s, but node %q, pinned by a direct constraint, is not an active worker there (%s).",
				res.ChosenCluster, name, o.Status)
		} else {
			fmt.Fprintf(&b, "Would fail: the root would pick cluster %s, but none of its %d nodes fit (%s).",
				res.ChosenCluster, len(nr.Nodes), o.Status)
			writeFailures(&b, nr.Nodes, "nodes in "+res.ChosenCluster.String())
		}
		if others := otherPassing(res); len(others) > 0 {
			verb := "passes"
			if len(others) > 1 {
				verb = "pass"
			}
			fmt.Fprintf(&b, " Oakestra does not retry another cluster, even though %s also %s the cluster-level checks.",
				strings.Join(others, ", "), verb)
		}
		writeFailures(&b, res.Clusters, "clusters")
	default:
		o.WouldDeploy = true
		o.Node = nr.ChosenNode
		fmt.Fprintf(&b, "Would land on cluster %s, node %s.", res.ChosenCluster, nr.ChosenNode)
		writeFailures(&b, res.Clusters, "clusters")
		writeFailures(&b, nr.Nodes, "nodes in "+res.ChosenCluster.String())
	}

	for _, other := range res.NodeResults {
		if other.Cluster.ID == res.ChosenCluster.ID {
			continue
		}
		switch {
		case other.Error != "":
			fmt.Fprintf(&b, " Cluster %s: workers unavailable.", other.Cluster)
		case other.ChosenNode != nil:
			fmt.Fprintf(&b, " Cluster %s would pick node %s if it were chosen.", other.Cluster, other.ChosenNode)
		default:
			fmt.Fprintf(&b, " Cluster %s has no fitting node (%s).", other.Cluster, other.Status)
		}
	}
	o.Summary = b.String()
	return o
}

// writeFailures appends "2 of 3 clusters fail: a (memory), b (active)."
func writeFailures(b *strings.Builder, evals []placement.Evaluation, what string) {
	var parts []string
	for _, e := range evals {
		if e.Passed {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", Ref{ID: e.CandidateID, Name: e.CandidateName}, strings.Join(failedChecks(e), ", ")))
	}
	if len(parts) == 0 {
		return
	}
	n := len(parts)
	if n > maxListed {
		parts = append(parts[:maxListed], fmt.Sprintf("and %d more", n-maxListed))
	}
	fmt.Fprintf(b, " %d of %d %s fail: %s.", n, len(evals), what, strings.Join(parts, ", "))
}

func failedChecks(e placement.Evaluation) []string {
	var names []string
	for _, c := range e.Checks {
		if c.Status == placement.StatusFail {
			names = append(names, c.Check)
		}
	}
	if len(names) == 0 {
		names = []string{"not chosen"}
	}
	return names
}

// pinnedMissing reports whether a direct constraint pins a name that no
// candidate at this level has, which is the TargetClusterNotActive case
// that is easiest to misread as a capacity problem.
func pinnedMissing(job placement.Resources, level placement.Level, evals []placement.Evaluation) bool {
	name, ok := placement.PinnedName(job, level)
	if !ok {
		return false
	}
	for _, e := range evals {
		if e.CandidateName == name {
			if c, ok := e.Check(placement.CheckActive); !ok || c.Status != placement.StatusFail {
				return false
			}
		}
	}
	return true
}

// otherPassing names the clusters that pass the cluster-level checks but
// are not the chosen one, with what the node stage found where it ran.
func otherPassing(res Result) []string {
	var out []string
	for _, e := range res.Clusters {
		if !e.Passed || e.CandidateID == res.ChosenCluster.ID {
			continue
		}
		ref := Ref{ID: e.CandidateID, Name: e.CandidateName}
		desc := ref.String()
		if nr, ok := res.NodeResultFor(e.CandidateID); ok {
			switch {
			case nr.Error != "":
				desc += " (its workers could not be read)"
			case nr.ChosenNode != nil:
				desc += fmt.Sprintf(" (which has a fitting node, %s)", nr.ChosenNode)
			default:
				desc += " (which has no fitting node either)"
			}
		}
		out = append(out, desc)
	}
	return out
}

func caveats(res Result, job placement.Resources) []string {
	cs := []string{
		"Based on a snapshot of the resource reports right now; workers and clusters report every few seconds, so numbers move.",
		"The root scheduler judges clusters on aggregates over their active workers (summed CPU and memory, best GPU per slot), " +
			"so a cluster can pass while no single worker fits. Only the node stage is conclusive.",
		"Scheduler state can change before a real deploy (other deployments, workers joining or dropping out), " +
			"so this is a prediction, not a reservation.",
	}
	cs = append(cs, notEnforcedCaveats(job)...)

	if res.ChosenCluster == nil {
		return cs
	}
	nr, _ := res.NodeResultFor(res.ChosenCluster.ID)
	switch {
	case nr.Error != "":
		cs = append(cs, fmt.Sprintf("Node-level data for cluster %s is unavailable (the root cannot reach its resource "+
			"abstractor, e.g. behind NAT), so the outcome is cluster-level only.", res.ChosenCluster))
	case nr.ChosenNode == nil:
		if others := otherPassing(res); len(others) > 0 {
			hint := ""
			if !anyNodeStageBesides(res, res.ChosenCluster.ID) {
				hint = " Run again with all_clusters to see whether their workers would fit."
			}
			cs = append(cs, fmt.Sprintf("Oakestra does not retry another cluster: the deploy fails on %s instead of moving to %s.%s",
				res.ChosenCluster, strings.Join(others, ", "), hint))
		}
	}
	return cs
}

func anyNodeStageBesides(res Result, clusterID string) bool {
	for _, n := range res.NodeResults {
		if n.Cluster.ID != clusterID {
			return true
		}
	}
	return false
}

// notEnforcedCaveats lists SLA fields an agent would reasonably expect to
// matter but that Oakestra's scheduler ignores.
func notEnforcedCaveats(job placement.Resources) []string {
	var cs []string
	if len(job.Arch) > 0 {
		cs = append(cs, fmt.Sprintf("The SLA asks for architecture %s, but Oakestra does not enforce it; the job can land on a worker "+
			"of another architecture.", strings.Join(job.Arch, ", ")))
	}
	seen := map[string]bool{}
	for _, c := range job.Constraints {
		if seen[c.Type] {
			continue
		}
		seen[c.Type] = true
		switch c.Type {
		case "direct":
		case "geo":
			cs = append(cs, "The SLA has a geo constraint; Oakestra does not enforce it, placement ignores location.")
		case "latency":
			cs = append(cs, "The SLA has a latency constraint; Oakestra does not enforce it, placement ignores latency.")
		case "addons":
			cs = append(cs, "The SLA has an addons constraint; Oakestra does not enforce it, clusters are not filtered by addons.")
		case "clusters":
			cs = append(cs, "The SLA has an allowed clusters constraint; Oakestra does not enforce it, use a direct constraint "+
				"with a cluster to pin one.")
		default:
			cs = append(cs, fmt.Sprintf("The SLA has a constraint of type %q, which Oakestra's scheduler ignores.", c.Type))
		}
	}
	return cs
}
