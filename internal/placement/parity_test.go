package placement

// Ports of the upstream table tests in
// scheduler/calculate/schedulers/placement/placement_test.go and
// scheduler/calculate/schedulers/cpumemfit/cpumemfit_test.go at the commit
// named in doc.go. Each case keeps its upstream name and expectation so a
// drift between this package and the scheduler shows up as a failing case.

import (
	"encoding/json"
	"reflect"
	"testing"
)

func base(id string, virt []string, mem, cpu, cpuPct float64) Resources {
	return Resources{ID: id, Virtualization: virt, Memory: mem, VCPUs: cpu, CPUPercent: cpuPct}
}

func TestNormalizeVirtualization(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    []string
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"single string", "docker", []string{"docker"}, false},
		{"string array", []any{"docker", "unikernel"}, []string{"docker", "unikernel"}, false},
		{"empty array", []any{}, []string{}, false},
		{"non-string element in array", []any{42}, nil, true},
		{"unknown type", 3.14, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeVirtualization(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestMeetsBasicRequirements mirrors upstream. Without volumes, constraints
// and GPUs, Evaluate().Passed must equal MeetsBasicRequirements.
func TestMeetsBasicRequirements(t *testing.T) {
	enough := Resources{Virtualization: []string{"docker"}, Memory: 1000, VCPUs: 4}
	tests := []struct {
		name      string
		job       Resources
		candidate Resources
		want      bool
	}{
		{"all met", Resources{Virtualization: []string{"docker"}, Memory: 500, VCPUs: 2}, enough, true},
		{"exact resource match", Resources{Virtualization: []string{"docker"}, Memory: 1000, VCPUs: 4}, enough, true},
		{
			"candidate supports multiple virt types",
			Resources{Virtualization: []string{"docker"}, Memory: 100, VCPUs: 1},
			Resources{Virtualization: []string{"unikernel", "docker"}, Memory: 1000, VCPUs: 4},
			true,
		},
		{"empty job virtualization", Resources{Memory: 100, VCPUs: 1}, enough, false},
		{"virtualization mismatch", Resources{Virtualization: []string{"unikernel"}, Memory: 100, VCPUs: 1}, enough, false},
		{"insufficient CPU", Resources{Virtualization: []string{"docker"}, Memory: 100, VCPUs: 8}, enough, false},
		{"insufficient memory", Resources{Virtualization: []string{"docker"}, Memory: 2000, VCPUs: 1}, enough, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, level := range []Level{LevelCluster, LevelNode} {
				if got := Evaluate(tt.job, tt.candidate, level).Passed; got != tt.want {
					t.Errorf("%s: Passed = %v, want %v", level, got, tt.want)
				}
			}
		})
	}
}

func TestMeetsGPURequirements(t *testing.T) {
	docker := []string{"docker"}
	job := func(vgpus int, vram float64) Resources {
		return Resources{Virtualization: docker, Memory: 100, VCPUs: 1, Vgpus: vgpus, Vram: vram}
	}
	node := func(free ...float64) Resources {
		return Resources{Virtualization: docker, Memory: 1000, VCPUs: 4, GpuVramFree: free}
	}
	tests := []struct {
		name      string
		job       Resources
		candidate Resources
		want      bool
	}{
		{"no GPU needed on GPU-less node", job(0, 0), node(), true},
		{"one GPU on GPU-less node", job(1, 0), node(), false},
		{"two GPUs on single-GPU node", job(2, 0), node(8000), false},
		{"GPU count only, no VRAM request", job(1, 0), node(10), true},
		{"VRAM fits across top two GPUs", job(2, 12000), node(8000, 6000), true},
		{"VRAM does not fit on one GPU", job(1, 12000), node(8000, 6000), false},
		{"VRAM exactly fits", job(1, 8000), node(8000, 6000), true},
		{"VRAM without vgpus implies one GPU, fits", job(0, 7000), node(8000), true},
		{"VRAM without vgpus implies one GPU, GPU-less node", job(0, 7000), node(), false},
		{"VRAM without vgpus implies one GPU, too little", job(0, 9000), node(8000, 6000), false},
		{"unsorted list uses largest GPUs", job(2, 13000), node(1000, 6000, 8000), true},
		{
			"legacy fallback splits installed VRAM evenly",
			job(2, 16000),
			Resources{Virtualization: docker, Memory: 1000, VCPUs: 4, Vgpus: 2, Vram: 16000},
			true,
		},
		{
			"legacy fallback rejects VRAM above installed total",
			job(2, 16001),
			Resources{Virtualization: docker, Memory: 1000, VCPUs: 4, Vgpus: 2, Vram: 16000},
			false,
		},
		{
			"legacy fallback rejects more GPUs than installed",
			job(3, 0),
			Resources{Virtualization: docker, Memory: 1000, VCPUs: 4, Vgpus: 2, Vram: 16000},
			false,
		},
		{
			"reported free VRAM wins over installed total",
			job(1, 8000),
			Resources{Virtualization: docker, Memory: 1000, VCPUs: 4, Vgpus: 1, Vram: 16000, GpuVramFree: []float64{4000}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := Evaluate(tt.job, tt.candidate, LevelNode)
			if ev.Passed != tt.want {
				t.Errorf("Passed = %v, want %v; checks %+v", ev.Passed, tt.want, ev.Checks)
			}
			// CPU, memory and virtualization always fit here, so the GPU
			// checks alone must carry the verdict.
			gpuOK := !failed(ev, CheckGPUCount) && !failed(ev, CheckVRAM) && statusOf(ev, CheckVRAM) != StatusUnknown
			if gpuOK != tt.want {
				t.Errorf("GPU checks say %v, want %v; checks %+v", gpuOK, tt.want, ev.Checks)
			}
		})
	}
}

func TestMeetsGPURequirements_NonContainerRuntimes(t *testing.T) {
	gpuLess := Resources{Virtualization: []string{"crosvm", "unikernel"}, Memory: 1000, VCPUs: 4}
	for _, virt := range []string{"crosvm", "unikernel"} {
		t.Run(virt, func(t *testing.T) {
			job := Resources{Virtualization: []string{virt}, Vgpus: 2, Vram: 8000}
			if !Evaluate(job, gpuLess, LevelNode).Passed {
				t.Errorf("%s job must not be filtered on NVIDIA GPU capacity", virt)
			}
		})
	}
}

func TestMeetsGPURequirements_DoesNotMutateCandidate(t *testing.T) {
	candidate := Resources{Virtualization: []string{"docker"}, GpuVramFree: []float64{1000, 6000, 8000}}
	Evaluate(Resources{Virtualization: []string{"docker"}, Vgpus: 2, Vram: 1}, candidate, LevelNode)
	if want := []float64{1000, 6000, 8000}; !reflect.DeepEqual(candidate.GpuVramFree, want) {
		t.Fatalf("GpuVramFree mutated: got %v, want %v", candidate.GpuVramFree, want)
	}
}

func TestNoCapacityStatus(t *testing.T) {
	always := func(bool) bool { return true }
	never := func(bool) bool { return false }
	docker := []string{"docker"}
	gpuJob := Resources{Virtualization: docker, Vgpus: 1}
	tests := []struct {
		name string
		job  Resources
		keep func(bool) bool
		want string
	}{
		{"GPU job, near miss exists", gpuJob, always, StatusNoGpuCapacity},
		{"GPU job, nothing passes other checks", gpuJob, never, StatusNoActiveClusterWithCapacity},
		{"VRAM-only job counts as GPU job", Resources{Virtualization: docker, Vram: 100}, always, StatusNoGpuCapacity},
		{"non-GPU job", Resources{Virtualization: docker}, always, StatusNoActiveClusterWithCapacity},
		{"crosvm job with vgpus", Resources{Virtualization: []string{"crosvm"}, Vgpus: 1}, always, StatusNoActiveClusterWithCapacity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noCapacityStatus(tt.job, []bool{true}, tt.keep); got != tt.want {
				t.Errorf("noCapacityStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSchedulingErrorString pins the wire strings to upstream's
// NegativeSchedulingStatusName.
func TestSchedulingErrorString(t *testing.T) {
	want := map[string]string{
		StatusTargetClusterNotFound:       "TargetClusterNotFound",
		StatusTargetClusterNotActive:      "TargetClusterNotActive",
		StatusNoActiveClusterWithCapacity: "NoActiveClusterWithCapacity",
		StatusNoWorkerCapacity:            "NO_WORKER_CAPACITY",
		StatusNoQualifiedWorkerFound:      "NO_QUALIFIED_WORKER_FOUND",
		StatusNoNodeFound:                 "NO_NODE_FOUND",
		StatusNoGpuCapacity:               "NoGpuCapacity",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("status %q, want %q", got, w)
		}
	}
}

// calculate runs Evaluate over every candidate and Choose, the way
// cpumemfit.Calculate sees them. It returns the winner's ID.
func calculate(job Resources, candidates []Resources) (string, string) {
	evals := make([]Evaluation, len(candidates))
	for i, c := range candidates {
		evals[i] = Evaluate(job, c, LevelCluster)
	}
	idx, status := Choose(job, evals)
	if idx < 0 {
		return "", status
	}
	return candidates[idx].ID, status
}

type calcCase struct {
	name       string
	job        Resources
	candidates []Resources
	wantID     string
	wantStatus string
}

func runCalcCases(t *testing.T, tests []calcCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, status := calculate(tt.job, tt.candidates)
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if tt.wantStatus == "" && id != tt.wantID {
				t.Errorf("winner = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestCalculateConstraints(t *testing.T) {
	job1 := base("1", []string{"docker"}, 500, 1, 0)
	job2 := base("2", []string{"docker"}, 5000, 20, 0)
	job3 := base("3", []string{"unikernel"}, 500, 1, 0)

	cluster1 := base("1", []string{"docker"}, 2000, 4, 10)
	cluster2 := base("2", []string{"docker"}, 2000, 4, 20)
	cluster3 := base("3", []string{"unikernel"}, 4000, 8, 10)
	cluster4 := base("4", []string{"docker", "unikernel"}, 4000, 8, 5)
	cluster5 := base("5", []string{"docker"}, 2000, 4, 10)
	cluster5.CSIDrivers = []string{"nfs.csi.k8s.io"}
	cluster6 := base("6", []string{"docker"}, 2000, 4, 5)
	cluster6.CSIDrivers = []string{"nfs.csi.k8s.io", "rbd.csi.ceph.com"}

	jobWithCSI := base("csi-job", []string{"docker"}, 500, 1, 0)
	jobWithCSI.Volumes = []VolumeSpec{{VolumeID: "vol-1", CSIDriver: "nfs.csi.k8s.io", MountPath: "/data"}}
	jobWithUnknownCSI := base("csi-job-missing", []string{"docker"}, 500, 1, 0)
	jobWithUnknownCSI.Volumes = []VolumeSpec{{VolumeID: "vol-2", CSIDriver: "unknown.csi.driver", MountPath: "/data"}}

	loMem := base("lo-mem", []string{"docker"}, 600, 4, 10)
	hiMem := base("hi-mem", []string{"docker"}, 1800, 4, 10)

	runCalcCases(t, []calcCase{
		{"Docker best fit", job1, []Resources{cluster1, cluster2, cluster3}, "1", ""},
		{"Unikernel best fit", job3, []Resources{cluster1, cluster2, cluster3}, "3", ""},
		{"Docker best fit dual cluster", job1, []Resources{cluster1, cluster2, cluster3, cluster4}, "4", ""},
		{"Unikernel best fit dual cluster", job3, []Resources{cluster1, cluster2, cluster3, cluster4}, "4", ""},
		{"Docker no capacity", job2, []Resources{cluster1, cluster2, cluster3, cluster4}, "", StatusNoActiveClusterWithCapacity},
		{"CSI driver available", jobWithCSI, []Resources{cluster1, cluster5, cluster6}, "6", ""},
		{"CSI driver not available", jobWithUnknownCSI, []Resources{cluster1, cluster5, cluster6}, "", StatusNoActiveClusterWithCapacity},
		{"Memory wins when CPU percent is equal", job1, []Resources{loMem, hiMem}, "hi-mem", ""},
	})
}

func TestCsiDriversUnmarshal(t *testing.T) {
	t.Run("flat string array (root level)", func(t *testing.T) {
		raw := `{"_id":"c1","virtualization":["docker"],"memory":2000,"vcpus":4,"cpu_percent":10,
			"csi_drivers":["nfs.csi.k8s.io","rbd.csi.ceph.com"]}`
		var r Resources
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if want := []string{"nfs.csi.k8s.io", "rbd.csi.ceph.com"}; !reflect.DeepEqual(r.CSIDrivers, want) {
			t.Errorf("got %v, want %v", r.CSIDrivers, want)
		}
	})
	t.Run("object array (cluster level / node engine)", func(t *testing.T) {
		raw := `{"_id":"w1","virtualization":["docker"],"memory":2000,"vcpus":4,"cpu_percent":5,
			"csi_drivers":[
				{"csi_driver_name":"nfs.csi.k8s.io","csi_driver_endpoint":"/var/lib/kubelet/plugins/nfs.sock"},
				{"csi_driver_name":"nfs.csi.k8s.io","csi_driver_endpoint":"/other/path"},
				{"csi_driver_name":"rbd.csi.ceph.com","csi_driver_endpoint":"/var/lib/ceph.sock"}
			]}`
		var r Resources
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if want := []string{"nfs.csi.k8s.io", "rbd.csi.ceph.com"}; !reflect.DeepEqual(r.CSIDrivers, want) {
			t.Errorf("got %v, want %v", r.CSIDrivers, want)
		}
	})
	t.Run("volumes from job descriptor (only volumes, no csi_drivers key)", func(t *testing.T) {
		raw := `{"_id":"job1","virtualization":["docker"],"memory":500,"vcpus":1,"cpu_percent":0,
			"volumes":[
				{"volume_id":"vol-1","csi_driver":"nfs.csi.k8s.io","mount_path":"/data","config":{"server":"192.168.1.1"}}
			]}`
		var r Resources
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if r.CSIDrivers != nil {
			t.Errorf("expected nil CSIDrivers for a job descriptor, got %v", r.CSIDrivers)
		}
		if len(r.Volumes) != 1 {
			t.Fatalf("expected 1 volume, got %d", len(r.Volumes))
		}
		v := r.Volumes[0]
		if v.CSIDriver != "nfs.csi.k8s.io" || v.MountPath != "/data" || v.Config["server"] != "192.168.1.1" {
			t.Errorf("unexpected volume %+v", v)
		}
	})
}

func TestFilterWithObjectFormatCSIDrivers(t *testing.T) {
	worker := base("w1", []string{"docker"}, 2000, 4, 10)
	worker.CSIDrivers = []string{"nfs.csi.k8s.io"}
	job := base("j1", []string{"docker"}, 500, 1, 0)
	job.Volumes = []VolumeSpec{{VolumeID: "v1", CSIDriver: "nfs.csi.k8s.io"}}
	runCalcCases(t, []calcCase{{"worker with NFS", job, []Resources{worker}, "w1", ""}})
}

func withGPU(r Resources, vram float64, free ...float64) Resources {
	r.Vgpus = 1
	r.Vram = vram
	r.GpuVramFree = free
	return r
}

func TestCalculateGPU(t *testing.T) {
	gpuJob := withGPU(base("gpu-job", []string{"docker"}, 500, 1, 0), 4000)
	bigGpuJob := withGPU(base("big-gpu-job", []string{"docker"}, 5000, 1, 0), 4000)
	csiGpuJob := withGPU(base("csi-gpu-job", []string{"docker"}, 500, 1, 0), 4000)
	csiGpuJob.Volumes = []VolumeSpec{{VolumeID: "v1", CSIDriver: "nfs.csi.k8s.io"}}

	cpuOnly := base("cpu-only", []string{"docker"}, 4000, 8, 0)
	gpuNode := withGPU(base("gpu", []string{"docker"}, 2000, 4, 50), 8000, 8000)
	smallGpuNode := withGPU(base("small-gpu", []string{"docker"}, 2000, 4, 10), 2000, 2000)

	runCalcCases(t, []calcCase{
		{"GPU job picks the GPU candidate", gpuJob, []Resources{cpuOnly, gpuNode, smallGpuNode}, "gpu", ""},
		{"GPU job on CPU-sufficient GPU-less candidates", gpuJob, []Resources{cpuOnly, smallGpuNode}, "", StatusNoGpuCapacity},
		{"GPU job where nothing has enough memory", bigGpuJob, []Resources{cpuOnly, smallGpuNode}, "", StatusNoActiveClusterWithCapacity},
		{"GPU job where the CSI check also fails", csiGpuJob, []Resources{gpuNode}, "", StatusNoActiveClusterWithCapacity},
	})
}

func TestGPUFieldsUnmarshal(t *testing.T) {
	raw := `{"_id":"w1","virtualization":["docker"],"memory":2000,"vcpus":4,
		"vgpus":2,"vram":16000,"gpu_vram_free":[8000.5,6000]}`
	var r Resources
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.Vgpus != 2 || r.Vram != 16000 || !reflect.DeepEqual(r.GpuVramFree, []float64{8000.5, 6000}) {
		t.Errorf("unexpected GPU fields %d %v %v", r.Vgpus, r.Vram, r.GpuVramFree)
	}
}

func statusOf(e Evaluation, check string) Status {
	c, _ := e.Check(check)
	return c.Status
}
