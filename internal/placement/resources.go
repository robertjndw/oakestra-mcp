package placement

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Constraint is one entry of a job's "constraints" list. Only "direct" is
// enforced by the scheduler; the other fields are kept so the not-enforced
// constraint types can be reported back with their data.
type Constraint struct {
	Type      string   `json:"type"`
	Node      string   `json:"node,omitempty"`
	Cluster   string   `json:"cluster,omitempty"`
	Area      string   `json:"area,omitempty"`
	Location  string   `json:"location,omitempty"`
	Threshold float64  `json:"threshold,omitempty"`
	Rigidness float64  `json:"rigidness,omitempty"`
	Needs     []string `json:"needs,omitempty"`
	Allowed   []string `json:"allowed,omitempty"`
}

// UnmarshalJSON decodes type, node and cluster as strictly as the scheduler
// does, but the informational fields leniently: the scheduler never decodes
// them, so a malformed threshold must not make us reject a job it accepts.
func (c *Constraint) UnmarshalJSON(data []byte) error {
	var strict struct {
		Type    string `json:"type"`
		Node    string `json:"node"`
		Cluster string `json:"cluster"`
	}
	if err := json.Unmarshal(data, &strict); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*c = Constraint{Type: strict.Type, Node: strict.Node, Cluster: strict.Cluster}
	c.Area = lenientString(raw["area"])
	c.Location = lenientString(raw["location"])
	_ = json.Unmarshal(raw["threshold"], &c.Threshold)
	_ = json.Unmarshal(raw["rigidness"], &c.Rigidness)
	_ = json.Unmarshal(raw["needs"], &c.Needs)
	_ = json.Unmarshal(raw["allowed"], &c.Allowed)
	return nil
}

// lenientString returns a JSON string's value, or the raw JSON text for any
// other value so it can still be shown to the user.
func lenientString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// VolumeSpec mirrors a "volumes" entry. Only CSIDriver matters for placement.
type VolumeSpec struct {
	VolumeID  string            `json:"volume_id"`
	CSIDriver string            `json:"csi_driver"`
	MountPath string            `json:"mount_path,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
}

// Resources is both a job and a candidate, exactly like cpumemfit.Resources,
// because the resource abstractor returns candidates in the same schema as
// the job payload. The fields after Volumes are not read by the scheduler and
// only feed the explanations.
type Resources struct {
	ID             string   `json:"_id"`
	Virtualization []string `json:"virtualization"`
	// Memory is the job's request or the candidate's reported memory in MB.
	Memory float64 `json:"memory"`
	// VCPUs is the job's request or, on a candidate, the installed core count
	// (summed over workers on a cluster). It is not free capacity.
	VCPUs      float64 `json:"vcpus"`
	CPUPercent float64 `json:"cpu_percent"`
	// On a job, Vgpus is the number of GPUs and Vram the total MiB needed
	// across them. On a candidate they are the installed GPU count and VRAM,
	// used only when GpuVramFree is missing.
	Vgpus       int          `json:"vgpus"`
	Vram        float64      `json:"vram"`
	GpuVramFree []float64    `json:"gpu_vram_free"`
	Constraints []Constraint `json:"constraints"`
	CSIDrivers  []string     `json:"csi_drivers"`
	Volumes     []VolumeSpec `json:"volumes"`

	CandidateName         string  `json:"candidate_name,omitempty"`
	CandidateLocation     string  `json:"candidate_location,omitempty"`
	Architecture          string  `json:"architecture,omitempty"`
	Active                *bool   `json:"active,omitempty"`
	ActiveNodes           int     `json:"active_nodes,omitempty"`
	LastModifiedTimestamp float64 `json:"last_modified_timestamp,omitempty"`
	// AggregationPerArchitecture is what a cluster reports instead of a single
	// architecture. Only the keys are used.
	AggregationPerArchitecture map[string]json.RawMessage `json:"aggregation_per_architecture,omitempty"`
	// Arch is the SLA "arch" field (string or list), required by Oakestra for
	// unikernels and never checked by the scheduler.
	Arch []string `json:"arch,omitempty"`
}

// UnmarshalJSON normalises the polymorphic fields the same way cpumemfit does.
func (r *Resources) UnmarshalJSON(data []byte) error {
	type alias Resources
	aux := &struct {
		// The scheduler always receives _id as a string. Service documents
		// fetched through the system manager may carry the Mongo extended JSON
		// form instead, so accept {"$oid": ...} too.
		ID             json.RawMessage `json:"_id"`
		Virtualization any             `json:"virtualization"`
		CSIDrivers     any             `json:"csi_drivers"`
		Arch           any             `json:"arch"`
		*alias
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	id, err := decodeID(aux.ID)
	if err != nil {
		return err
	}
	r.ID = id

	virt, err := normalizeVirtualization(aux.Virtualization)
	if err != nil {
		return err
	}
	r.Virtualization = virt
	r.CSIDrivers = normaliseCsiDrivers(aux.CSIDrivers)

	arch, err := normalizeVirtualization(aux.Arch)
	if err != nil {
		return fmt.Errorf("arch: %w", err)
	}
	r.Arch = arch
	return nil
}

func decodeID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var oid struct {
		OID string `json:"$oid"`
	}
	if err := json.Unmarshal(raw, &oid); err != nil || oid.OID == "" {
		return "", errors.New("_id must be a string or {\"$oid\": string}")
	}
	return oid.OID, nil
}

// normalizeVirtualization is placement.NormalizeVirtualization: string,
// []string or null become a []string.
func normalizeVirtualization(raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		return []string{v}, nil
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, errors.New("invalid type in virtualization array")
			}
			result = append(result, str)
		}
		return result, nil
	case nil:
		return nil, nil
	default:
		return nil, errors.New("unexpected type for virtualization")
	}
}

// normaliseCsiDrivers accepts the flat string list clusters report and the
// {csi_driver_name, csi_driver_endpoint} objects workers report, and returns
// the deduplicated driver names in order. Copied from cpumemfit.
func normaliseCsiDrivers(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		var name string
		switch v := item.(type) {
		case string:
			name = v
		case map[string]any:
			if n, ok := v["csi_driver_name"].(string); ok {
				name = n
			}
		}
		if name == "" {
			continue
		}
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// InstanceNumber decodes an instance_list entry's instance_number. Both
// forms show up in the service document: the system manager stores an int,
// the cluster report schema types it as a string.
func InstanceNumber(raw json.RawMessage) (int, bool) {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n, true
		}
	}
	return 0, false
}
