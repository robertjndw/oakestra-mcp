package explain

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// ServiceInfo identifies the explained service.
type ServiceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Application  string `json:"application,omitempty"`
	Status       string `json:"status,omitempty"`
	StatusDetail string `json:"status_detail,omitempty"`
}

// InstancePlacement is where one instance is now and how the current dry
// run judges that spot.
type InstancePlacement struct {
	Number       int    `json:"instance_number"`
	Status       string `json:"status,omitempty"`
	StatusDetail string `json:"status_detail,omitempty"`
	Cluster      *Ref   `json:"cluster,omitempty"`
	Worker       *Ref   `json:"worker,omitempty" jsonschema:"worker the instance runs on; name is resolved from the cluster's resource abstractor when reachable"`
	HostIP       string `json:"host_ip,omitempty"`
	// The *bool fields are null when the data to decide is missing, e.g.
	// the cluster's resource abstractor was unreachable.
	ClusterPasses      *bool  `json:"cluster_passes" jsonschema:"whether the instance's cluster passes the cluster-level checks now"`
	ClusterStillChosen bool   `json:"cluster_still_chosen" jsonschema:"whether the root scheduler would pick this cluster again now"`
	NodePasses         *bool  `json:"node_passes" jsonschema:"whether the instance's worker passes the node-level checks now"`
	NodeStillChosen    *bool  `json:"node_still_chosen" jsonschema:"whether the scheduler of the instance's cluster would pick this worker again now"`
	Note               string `json:"note,omitempty"`
}

// Explanation is a dry run for a deployed service plus where its instances
// are now.
type Explanation struct {
	Service   ServiceInfo         `json:"service"`
	Instances []InstancePlacement `json:"instances"`
	DryRun    Result              `json:"dry_run"`
	Summary   string              `json:"summary"`
}

// storedService is the part of the system manager's job document Explain
// reads besides the scheduling fields.
type storedService struct {
	MicroserviceID   string            `json:"microserviceID"`
	MicroserviceName string            `json:"microservice_name"`
	AppName          string            `json:"app_name"`
	ApplicationName  string            `json:"application_name"`
	Status           string            `json:"status"`
	StatusDetail     string            `json:"status_detail"`
	InstanceList     []json.RawMessage `json:"instance_list"`
}

type storedInstance struct {
	Number       json.RawMessage `json:"instance_number"`
	Status       string          `json:"status"`
	StatusDetail string          `json:"status_detail"`
	ClusterID    string          `json:"cluster_id"`
	WorkerID     string          `json:"worker_id"`
	HostIP       string          `json:"host_ip"`
}

// Explain runs a dry run for a stored service document (as returned by the
// system manager's GET /api/service/<id>) and annotates every instance with
// how the scheduler would judge its current cluster and worker now.
//
// In instance_list, cluster_id is the root resource abstractor's cluster
// _id and worker_id the cluster resource abstractor's worker _id (the
// cluster manager sets it from the scheduler's answer), so both match
// candidate IDs directly. host_ip is the worker's ip and only a fallback.
func Explain(ctx context.Context, src CandidateSource, serviceRaw json.RawMessage, opts Options) (Explanation, error) {
	var svc storedService
	if err := json.Unmarshal(serviceRaw, &svc); err != nil {
		return Explanation{}, fmt.Errorf("service document does not decode: %w", err)
	}
	job, err := placement.JobFromMicroservice(serviceRaw)
	if err != nil {
		return Explanation{}, fmt.Errorf("the stored service cannot be turned into a scheduler job: %w", err)
	}

	instances := make([]storedInstance, 0, len(svc.InstanceList))
	var clusterIDs []string
	for _, raw := range svc.InstanceList {
		var in storedInstance
		if err := json.Unmarshal(raw, &in); err != nil {
			return Explanation{}, fmt.Errorf("instance_list entry does not decode: %w", err)
		}
		instances = append(instances, in)
		if in.ClusterID != "" && !slices.Contains(clusterIDs, in.ClusterID) {
			clusterIDs = append(clusterIDs, in.ClusterID)
		}
	}

	res, err := dryRun(ctx, src, job, opts, clusterIDs)
	if err != nil {
		return Explanation{}, err
	}

	ex := Explanation{
		Service: ServiceInfo{
			ID:          cmp.Or(svc.MicroserviceID, job.ID),
			Name:        svc.MicroserviceName,
			Application: cmp.Or(svc.ApplicationName, svc.AppName),
			Status:      svc.Status, StatusDetail: svc.StatusDetail,
		},
		Instances: make([]InstancePlacement, 0, len(instances)),
		DryRun:    res,
	}
	for _, in := range instances {
		ex.Instances = append(ex.Instances, annotate(in, res))
	}
	if len(ex.Instances) > 0 {
		ex.DryRun.Caveats = append(ex.DryRun.Caveats, "Instances already running consume resources on their hosts, so "+
			"those hosts can look fuller to this dry run than they did when the instances were placed.")
	}
	ex.Summary = explanationSummary(ex)
	return ex, nil
}

func annotate(in storedInstance, res Result) InstancePlacement {
	n, ok := placement.InstanceNumber(in.Number)
	if !ok {
		n = -1
	}
	ip := InstancePlacement{
		Number: n, Status: in.Status, StatusDetail: in.StatusDetail, HostIP: in.HostIP,
	}
	if in.WorkerID != "" {
		ip.Worker = &Ref{ID: in.WorkerID}
	}
	if in.ClusterID == "" {
		ip.Note = "not assigned to a cluster yet"
		return ip
	}

	ip.Cluster = &Ref{ID: in.ClusterID}
	idx := slices.IndexFunc(res.Clusters, func(e placement.Evaluation) bool { return e.CandidateID == in.ClusterID })
	if idx < 0 {
		ip.Note = "the root resource abstractor no longer reports this cluster"
		return ip
	}
	ce := res.Clusters[idx]
	ip.Cluster.Name = ce.CandidateName
	ip.ClusterPasses = new(ce.Passed)
	ip.ClusterStillChosen = res.ChosenCluster != nil && res.ChosenCluster.ID == in.ClusterID

	nr, ok := res.NodeResultFor(in.ClusterID)
	switch {
	case !ok:
		return ip
	case nr.Error != "":
		ip.Note = "the cluster's workers could not be read: " + nr.Error
		return ip
	case in.WorkerID == "" && in.HostIP == "":
		ip.Note = "not assigned to a worker yet"
		return ip
	}

	w := slices.IndexFunc(nr.workers, func(c candidates.Candidate) bool { return in.WorkerID != "" && c.ID == in.WorkerID })
	if w < 0 && in.HostIP != "" {
		// Only trust the IP when it is unambiguous; several workers behind
		// one NAT can share it.
		var hits []int
		for i, c := range nr.workers {
			if c.IP == in.HostIP {
				hits = append(hits, i)
			}
		}
		if len(hits) == 1 {
			w = hits[0]
		}
	}
	if w < 0 {
		ip.Note = "the cluster no longer reports this worker"
		return ip
	}
	ne := nr.Nodes[w]
	ip.Worker = &Ref{ID: nr.workers[w].ID, Name: cmp.Or(ne.CandidateName, nr.workers[w].Name)}
	ip.NodePasses = new(ne.Passed)
	ip.NodeStillChosen = new(nr.ChosenNode != nil && nr.ChosenNode.ID == ip.Worker.ID)
	return ip
}

func explanationSummary(ex Explanation) string {
	var b strings.Builder
	name := ex.Service.Name
	if name == "" {
		name = ex.Service.ID
	} else if ex.Service.ID != "" {
		name += " (" + ex.Service.ID + ")"
	}
	fmt.Fprintf(&b, "Service %s is %s", name, cmp.Or(ex.Service.Status, "of unknown status"))
	if ex.Service.StatusDetail != "" {
		fmt.Fprintf(&b, " (%s)", ex.Service.StatusDetail)
	}
	b.WriteString(".")
	if len(ex.Instances) == 0 {
		b.WriteString(" It has no instances.")
	}
	for _, in := range ex.Instances {
		fmt.Fprintf(&b, " Instance %d %s", in.Number, cmp.Or(in.Status, "of unknown status"))
		if in.StatusDetail != "" {
			fmt.Fprintf(&b, " (%s)", in.StatusDetail)
		}
		if in.Cluster != nil {
			fmt.Fprintf(&b, " on cluster %s", in.Cluster)
			if in.Worker != nil {
				fmt.Fprintf(&b, ", node %s", in.Worker)
			}
		}
		b.WriteString(verdict(in))
		b.WriteString(".")
	}
	b.WriteString(" Dry run now: ")
	b.WriteString(ex.DryRun.Outcome.Summary)
	return b.String()
}

func verdict(in InstancePlacement) string {
	switch {
	case in.Note != "":
		return "; " + in.Note
	case in.ClusterPasses == nil:
		return ""
	case !*in.ClusterPasses:
		return "; that cluster fails the cluster-level checks now"
	case in.NodeStillChosen != nil && *in.NodeStillChosen && in.ClusterStillChosen:
		return "; the scheduler would still pick exactly this spot"
	case in.NodePasses != nil && !*in.NodePasses:
		return "; that node fails the node-level checks now"
	case !in.ClusterStillChosen:
		return "; that cluster still qualifies but the root would now pick another"
	case in.NodeStillChosen != nil && !*in.NodeStillChosen:
		return "; that node still qualifies but the cluster would now pick another"
	}
	return ""
}
