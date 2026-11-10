package candidates

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

const rootDoc = `[
 {"_id":"c1","candidate_name":"cluster-a","candidate_location":"48.1,11.6,1000","ip":"127.0.0.1","port":"10100",
  "active":true,"vcpus":16,"memory":32000,"cpu_percent":12.5,"memory_percent":40,"vgpus":2,"vram":"24576",
  "virtualization":["docker","unikernel"],"active_nodes":3,"last_modified_timestamp":1762776000.5,
  "csi_drivers":["nfs.csi.k8s.io"],"gpu_vram_free":[8000,6000]},
 {"_id":{"$oid":"c2"},"candidate_name":"cluster-b","ip":"10.0.0.2","port":10100,"active":false,"virtualization":"docker"}
]`

const workerDoc = `[{"_id":"w1","candidate_name":"worker-1","architecture":"arm64","vcpus":4,"memory":"8000",
 "virtualization":"docker","csi_drivers":[{"csi_driver_name":"nfs.csi.k8s.io"}],"active":true}]`

func TestRootAndWorkers(t *testing.T) {
	var gotQuery string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/" {
			t.Errorf("worker RA path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(workerDoc))
	}))
	defer worker.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("resources")
		if r.URL.Query().Has("active") {
			t.Error("root query must not filter on active")
		}
		_, _ = w.Write([]byte(rootDoc))
	}))
	defer root.Close()

	_, port, _ := net.SplitHostPort(worker.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	c := New(root.URL, p, nil)

	clusters, err := c.Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery == "" {
		t.Error("expected extra resources to be requested")
	}
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters", len(clusters))
	}
	a := clusters[0]
	if a.ID != "c1" || a.Name != "cluster-a" || a.Active == nil || !*a.Active || a.VCPUs != 16 ||
		a.MemoryMB != 32000 || a.VRAM != 24576 || a.ActiveNodes != 3 || len(a.Virtualization) != 2 ||
		a.LastModifiedTimestamp.Unix() != 1762776000 {
		t.Fatalf("cluster-a decoded wrong: %+v", a)
	}
	b := clusters[1]
	if b.ID != "c2" || b.Port != "10100" || b.Active == nil || *b.Active || len(b.Virtualization) != 1 {
		t.Fatalf("cluster-b decoded wrong: %+v", b)
	}

	var raw map[string]any
	if err := json.Unmarshal(a.Raw, &raw); err != nil || raw["gpu_vram_free"] == nil || raw["csi_drivers"] == nil {
		t.Fatalf("raw document not preserved: %s", a.Raw)
	}

	workers, err := c.Workers(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].Architecture != "arm64" || workers[0].MemoryMB != 8000 ||
		workers[0].Virtualization[0] != "docker" {
		t.Fatalf("workers decoded wrong: %+v", workers)
	}
}

func TestUnreachableClusterIsUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	p, _ := strconv.Atoi(port)

	c := New("http://127.0.0.1:1", p, nil)
	c.SetTimeout(time.Second)
	if _, err := c.Workers(context.Background(), Candidate{Name: "x", IP: "127.0.0.1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if _, err := c.Workers(context.Background(), Candidate{Name: "no-ip"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable for missing IP, got %v", err)
	}
}

func TestNon2xxIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, 1, nil).Root(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
