package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/candidates"
)

// Resource is a cluster or worker as the scheduler sees it.
type Resource struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Location       string   `json:"location,omitempty" jsonschema:"geographic location as reported by the cluster or worker, usually lat,long,radius"`
	IP             string   `json:"ip,omitempty"`
	Active         *bool    `json:"active,omitempty" jsonschema:"whether the resource reported in recently; the scheduler only places on active resources"`
	ActiveNodes    int      `json:"active_nodes,omitempty" jsonschema:"number of active workers (clusters only)"`
	Architecture   string   `json:"architecture,omitempty" jsonschema:"CPU architecture, e.g. amd64 or arm64 (workers only)"`
	VCPUs          int      `json:"vcpus" jsonschema:"total CPU cores (summed over workers for a cluster)"`
	MemoryMB       int      `json:"memory_mb" jsonschema:"memory in MB (summed over workers for a cluster)"`
	CPUPercent     float64  `json:"cpu_percent" jsonschema:"current CPU usage in percent"`
	MemoryPercent  float64  `json:"memory_percent" jsonschema:"current memory usage in percent"`
	VGPUs          int      `json:"vgpus,omitempty" jsonschema:"number of GPUs"`
	VRAM           int      `json:"vram,omitempty" jsonschema:"total GPU memory in MiB"`
	Virtualization []string `json:"virtualization,omitempty" jsonschema:"supported runtimes, e.g. docker, unikernel, wasm"`
	LastSeen       string   `json:"last_seen,omitempty" jsonschema:"RFC 3339 time of the last resource report"`
}

func resourceFrom(c candidates.Candidate) Resource {
	r := Resource{
		ID: c.ID, Name: c.Name, Location: c.Location, IP: c.IP, Active: c.Active,
		ActiveNodes: c.ActiveNodes, Architecture: c.Architecture, VCPUs: c.VCPUs, MemoryMB: c.MemoryMB,
		CPUPercent: c.CPUPercent, MemoryPercent: c.MemoryPercent, VGPUs: c.VGPUs, VRAM: c.VRAM,
		Virtualization: c.Virtualization,
	}
	if !c.LastModifiedTimestamp.IsZero() {
		r.LastSeen = c.LastModifiedTimestamp.Format(time.RFC3339)
	}
	return r
}

type listClustersIn struct{}

type listClustersOut struct {
	Clusters []Resource `json:"clusters"`
}

type listNodesIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name or ID, as returned by list_clusters"`
}

type listNodesOut struct {
	Cluster   string     `json:"cluster"`
	ClusterID string     `json:"cluster_id"`
	Nodes     []Resource `json:"nodes"`
}

func registerInfraTools(r *registry) {
	addTool(r, ToolInfo{
		Name:  "list_clusters",
		Title: "List clusters",
		Description: "List every Oakestra cluster known to the root orchestrator, including inactive ones, " +
			"with its aggregated capacity (CPU cores, memory, GPUs, VRAM), current CPU/memory usage, " +
			"supported virtualization runtimes, number of active workers and whether it is active. " +
			"These are the same numbers the root scheduler uses to pick a cluster. Use the name or ID with list_nodes.",
		Kind: KindRead,
	}, r.listClusters)

	addTool(r, ToolInfo{
		Name:  "list_nodes",
		Title: "List worker nodes",
		Description: "List the worker nodes of one cluster with their architecture, virtualization runtimes, " +
			"CPU, memory, GPU and VRAM capacity, current usage and whether they are active. " +
			"Data comes straight from that cluster's resource abstractor, so it fails if the cluster is " +
			"not reachable from the root orchestrator (e.g. behind NAT).",
		Kind: KindRead,
	}, r.listNodes)
}

func (r *registry) listClusters(ctx context.Context, _ listClustersIn) (listClustersOut, string, error) {
	cs, err := r.d.Candidates.Root(ctx)
	if err != nil {
		return listClustersOut{}, "", err
	}
	out := listClustersOut{Clusters: make([]Resource, 0, len(cs))}
	active := 0
	var parts []string
	for _, c := range cs {
		res := resourceFrom(c)
		out.Clusters = append(out.Clusters, res)
		state := "inactive"
		if c.Active == nil {
			state = "activity unknown"
		} else if *c.Active {
			state = "active"
			active++
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %d workers)", c.Name, state, c.ActiveNodes))
	}
	return out, fmt.Sprintf("%d clusters, %d active: %s", len(cs), active, joinCapped(parts, 30)), nil
}

func (r *registry) listNodes(ctx context.Context, in listNodesIn) (listNodesOut, string, error) {
	cluster, err := r.findCluster(ctx, in.Cluster)
	if err != nil {
		return listNodesOut{}, "", err
	}
	ws, err := r.d.Candidates.Workers(ctx, cluster)
	if err != nil {
		return listNodesOut{}, "", fmt.Errorf("could not reach the resource abstractor of cluster %q: %w", cluster.Name, err)
	}
	out := listNodesOut{Cluster: cluster.Name, ClusterID: cluster.ID, Nodes: make([]Resource, 0, len(ws))}
	var parts []string
	for _, w := range ws {
		out.Nodes = append(out.Nodes, resourceFrom(w))
		desc := []string{}
		if w.Architecture != "" {
			desc = append(desc, w.Architecture)
		}
		desc = append(desc, fmt.Sprintf("%d cores, %d MB", w.VCPUs, w.MemoryMB))
		if w.VGPUs > 0 {
			desc = append(desc, fmt.Sprintf("%d GPUs", w.VGPUs))
		}
		if w.Active != nil && !*w.Active {
			desc = append(desc, "inactive")
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", w.Name, strings.Join(desc, ", ")))
	}
	return out, fmt.Sprintf("Cluster %s has %d workers: %s", cluster.Name, len(ws), joinCapped(parts, 30)), nil
}

// findCluster resolves a cluster by ID first (unique) and then by name.
func (r *registry) findCluster(ctx context.Context, ref string) (candidates.Candidate, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return candidates.Candidate{}, errors.New("cluster is required: pass a cluster name or ID from list_clusters")
	}
	cs, err := r.d.Candidates.Root(ctx)
	if err != nil {
		return candidates.Candidate{}, err
	}
	for _, c := range cs {
		if c.ID == ref {
			return c, nil
		}
	}
	var matches []candidates.Candidate
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Name)
		if c.Name == ref {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return candidates.Candidate{}, fmt.Errorf("no cluster named or with ID %q. Known clusters: %s", ref, joinCapped(names, 50))
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		return candidates.Candidate{}, fmt.Errorf("%d clusters are named %q; use one of these IDs: %s", len(matches), ref, strings.Join(ids, ", "))
	}
}

func joinCapped(parts []string, max int) string {
	if len(parts) == 0 {
		return "none"
	}
	if len(parts) <= max {
		return strings.Join(parts, ", ")
	}
	return strings.Join(parts[:max], ", ") + fmt.Sprintf(", and %d more", len(parts)-max)
}
