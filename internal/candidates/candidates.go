// Package candidates reads scheduling candidates from Oakestra's resource
// abstractors: clusters from the root one, workers from each cluster's.
// These are the same documents the schedulers decide on, which is what
// makes placement explanations faithful.
//
// The resource abstractor endpoints are unauthenticated, so callers must
// only use this package on behalf of an already authenticated principal.
package candidates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable wraps every failure to get an answer from a resource
// abstractor (unreachable, timeout, non-2xx, garbage body). Explain uses it
// to degrade to cluster-level results when a cluster's RA is behind NAT.
var ErrUnavailable = errors.New("resource abstractor unavailable")

// Candidate is one cluster (root level) or worker (cluster level).
//
// The typed fields are for display. Raw is the document exactly as the
// resource abstractor sent it; consumers that mirror scheduler logic
// (internal/placement) must decode Raw themselves so they see the same
// fields and quirks the scheduler sees.
type Candidate struct {
	ID       string
	Name     string // candidate_name: cluster name or worker hostname
	Location string // candidate_location
	IP       string
	Port     string
	// Active is nil when the resource abstractor did not report it.
	Active                *bool
	Architecture          string
	VCPUs                 int
	MemoryMB              int
	CPUPercent            float64
	MemoryPercent         float64
	VGPUs                 int
	VRAM                  int
	Virtualization        []string
	ActiveNodes           int
	LastModifiedTimestamp time.Time

	Raw json.RawMessage
}

// UnmarshalJSON decodes leniently: Oakestra components disagree on whether
// numbers are numbers or strings, and virtualization is a string on some
// workers and a list everywhere else.
func (c *Candidate) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*c = Candidate{
		ID:                    str(m["_id"]),
		Name:                  str(m["candidate_name"]),
		Location:              str(m["candidate_location"]),
		IP:                    str(m["ip"]),
		Port:                  str(m["port"]),
		Architecture:          str(m["architecture"]),
		VCPUs:                 int(num(m["vcpus"])),
		MemoryMB:              int(num(m["memory"])),
		CPUPercent:            num(m["cpu_percent"]),
		MemoryPercent:         num(m["memory_percent"]),
		VGPUs:                 int(num(m["vgpus"])),
		VRAM:                  int(num(m["vram"])),
		Virtualization:        strList(m["virtualization"]),
		ActiveNodes:           int(num(m["active_nodes"])),
		LastModifiedTimestamp: unixTime(num(m["last_modified_timestamp"])),
		Raw:                   append(json.RawMessage(nil), data...),
	}
	if v, ok := m["active"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil {
			c.Active = &b
		}
	}
	// Mongo ObjectIds can come through as {"$oid": "..."} if a component
	// serialises with bson's extended JSON.
	if c.ID == "" {
		var oid struct {
			OID string `json:"$oid"`
		}
		if json.Unmarshal(m["_id"], &oid) == nil {
			c.ID = oid.OID
		}
	}
	return nil
}

// MarshalJSON returns Raw so a Candidate round-trips unchanged.
func (c Candidate) MarshalJSON() ([]byte, error) {
	if len(c.Raw) == 0 {
		return []byte("{}"), nil
	}
	return c.Raw, nil
}

func str(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

func num(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f
		}
	}
	return 0
}

func strList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []string{s}
	}
	return nil
}

func unixTime(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9)).UTC()
}

// Client talks to the root resource abstractor and, through the cluster IPs
// it reports, to every cluster's resource abstractor.
type Client struct {
	rootURL     string
	clusterPort int
	hc          *http.Client
	timeout     time.Duration
}

// New creates a client. hc may be nil. The per-request timeout is kept
// short because an unreachable cluster should degrade an answer, not stall
// the agent waiting on it.
func New(rootURL string, clusterPort int, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{
		rootURL:     strings.TrimRight(rootURL, "/"),
		clusterPort: clusterPort,
		hc:          hc,
		timeout:     5 * time.Second,
	}
}

// SetTimeout overrides the per-request timeout.
func (c *Client) SetTimeout(d time.Duration) { c.timeout = d }

// The resource abstractor projects its answer to CANONICAL_RESOURCES and
// only adds fields named in "resources". The computed "active" flag,
// "architecture" and the timestamp are not canonical, so ask for them.
// (The scheduler's own client forgets the "resources=" key and therefore
// never gets these; that is fine, extra fields do not change what placement
// would decide.)
const extraFields = "?resources=active,architecture,last_modified_timestamp,gpu_vram_free"

// Root returns every cluster the root resource abstractor knows about,
// including inactive ones, so they can be shown and explained.
func (c *Client) Root(ctx context.Context) ([]Candidate, error) {
	return c.fetch(ctx, c.rootURL+"/api/v1/resources/"+extraFields)
}

// Workers returns the workers of cluster, read from the cluster's own
// resource abstractor at <cluster ip>:<cluster RA port>.
func (c *Client) Workers(ctx context.Context, cluster Candidate) ([]Candidate, error) {
	if cluster.IP == "" {
		return nil, fmt.Errorf("%w: cluster %q has no IP in the root resource abstractor", ErrUnavailable, cluster.Name)
	}
	host := net.JoinHostPort(cluster.IP, strconv.Itoa(c.clusterPort))
	return c.fetch(ctx, "http://"+host+"/api/v1/resources/"+extraFields)
}

func (c *Client) fetch(ctx context.Context, url string) ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %v", ErrUnavailable, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: GET %s returned HTTP %d", ErrUnavailable, url, resp.StatusCode)
	}
	var out []Candidate
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: decoding %s: %v", ErrUnavailable, url, err)
	}
	return out, nil
}
