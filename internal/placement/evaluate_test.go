package placement

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func named(name string, r Resources) Resources {
	r.CandidateName = name
	r.Active = boolPtr(true)
	return r
}

func TestEvaluateRunsAllChecksInOrder(t *testing.T) {
	job := base("j", []string{"docker"}, 99999, 64, 0)
	job.Vgpus = 4
	job.Volumes = []VolumeSpec{{CSIDriver: "nfs.csi.k8s.io"}}
	job.Arch = []string{"arm64"}
	job.Constraints = []Constraint{
		{Type: "direct", Node: "other"},
		{Type: "geo", Location: "48.1,11.5", Threshold: 100},
		{Type: "latency", Area: "munich-1", Threshold: 20, Rigidness: 0.9},
	}
	worker := named("w1", base("w1", []string{"unikernel"}, 100, 2, 50))
	worker.Active = boolPtr(false)
	worker.Architecture = "amd64"

	ev := Evaluate(job, worker, LevelNode)
	if ev.Passed {
		t.Fatal("expected failure")
	}
	var names []string
	for _, c := range ev.Checks {
		names = append(names, c.Check)
	}
	want := []string{CheckActive, CheckPinnedTarget, CheckVirtualization, CheckCPU, CheckMemory,
		CheckGPUCount, CheckVRAM, CheckCSIDriver, CheckArchitecture, CheckGeoArea, CheckLatency}
	if !slices.Equal(names, want) {
		t.Fatalf("checks %v, want %v", names, want)
	}
	for _, name := range []string{CheckActive, CheckPinnedTarget, CheckVirtualization, CheckCPU, CheckMemory, CheckGPUCount, CheckCSIDriver} {
		if statusOf(ev, name) != StatusFail {
			t.Errorf("%s = %s, want fail", name, statusOf(ev, name))
		}
	}
	arch, _ := ev.Check(CheckArchitecture)
	if arch.Status != StatusNotEnforced || !strings.Contains(arch.Detail, "SLA arch arm64, node architecture amd64") ||
		!strings.Contains(arch.Detail, "does not enforce") {
		t.Errorf("architecture check %+v", arch)
	}
	for _, c := range ev.Checks {
		if strings.ContainsAny(c.Detail, "\u2013\u2014") {
			t.Errorf("detail contains a dash character: %q", c.Detail)
		}
	}
}

func TestPinnedMismatch(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	job.Constraints = []Constraint{{Type: "direct", Cluster: "c2", Node: "w9"}}
	c1 := named("c1", base("1", []string{"docker"}, 1000, 4, 0))
	c2 := named("c2", base("2", []string{"docker"}, 500, 4, 0))

	evals := []Evaluation{Evaluate(job, c1, LevelCluster), Evaluate(job, c2, LevelCluster)}
	if evals[0].Passed || statusOf(evals[0], CheckPinnedTarget) != StatusFail {
		t.Errorf("c1 must fail the pin: %+v", evals[0].Checks)
	}
	if !evals[1].Passed {
		t.Errorf("c2 must pass: %+v", evals[1].Checks)
	}
	if idx, status := Choose(job, evals); idx != 1 || status != "" {
		t.Errorf("Choose = %d %q, want 1", idx, status)
	}

	// At node level the node name applies and no worker is called w9, so the
	// resource abstractor returns nothing and the scheduler reports this.
	w := named("w1", base("w1", []string{"docker"}, 1000, 4, 0))
	ev := Evaluate(job, w, LevelNode)
	if c, _ := ev.Check(CheckPinnedTarget); c.Status != StatusFail || !strings.Contains(c.Detail, `"w9"`) {
		t.Errorf("pinned check %+v", c)
	}
	if _, status := Choose(job, []Evaluation{ev}); status != StatusTargetClusterNotActive {
		t.Errorf("status = %q, want %q", status, StatusTargetClusterNotActive)
	}
}

func TestPinnedLastDirectWinsAndEmptyIsIgnored(t *testing.T) {
	job := Resources{Constraints: []Constraint{{Type: "direct", Cluster: "a"}, {Type: "direct", Cluster: "b"}}}
	if name, ok := PinnedName(job, LevelCluster); !ok || name != "b" {
		t.Errorf("PinnedName = %q %v, want b", name, ok)
	}
	if _, ok := PinnedName(job, LevelNode); ok {
		t.Error("an empty node name must not filter")
	}
}

func TestGPUOnlyFailureGivesNoGpuCapacity(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	job.Vgpus = 2
	job.Vram = 16384
	w := named("w1", base("w1", []string{"docker"}, 4000, 8, 10))
	w.GpuVramFree = []float64{6000, 8000}

	ev := Evaluate(job, w, LevelNode)
	if ev.Passed {
		t.Fatal("expected VRAM failure")
	}
	vram, _ := ev.Check(CheckVRAM)
	if vram.Status != StatusFail || !strings.Contains(vram.Detail, "needs 16384 MiB across 2 GPU(s), best 2 free: 8000+6000 = 14000 MiB") {
		t.Errorf("vram check %+v", vram)
	}
	if statusOf(ev, CheckGPUCount) != StatusPass {
		t.Errorf("gpu_count should pass")
	}
	if _, status := Choose(job, []Evaluation{ev}); status != StatusNoGpuCapacity {
		t.Errorf("status = %q, want NoGpuCapacity", status)
	}
}

func TestLegacyVRAMFallbackIsExplained(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	job.Vram = 1000
	w := named("w1", base("w1", []string{"docker"}, 4000, 8, 10))
	w.Vgpus, w.Vram = 2, 16000
	ev := Evaluate(job, w, LevelNode)
	c, _ := ev.Check(CheckGPUCount)
	if !ev.Passed || !strings.Contains(c.Detail, "no gpu_vram_free reported") || !strings.Contains(c.Detail, "implies one GPU") {
		t.Errorf("unexpected %+v", ev.Checks)
	}
}

func TestInactiveCandidate(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	dead := named("c1", base("1", []string{"docker"}, 9000, 4, 0))
	dead.Active = boolPtr(false)
	dead.LastModifiedTimestamp = 1700000000
	alive := named("c2", base("2", []string{"docker"}, 1000, 4, 0))

	evDead := Evaluate(job, dead, LevelCluster)
	if evDead.Passed {
		t.Error("inactive candidate must not pass")
	}
	if c, _ := evDead.Check(CheckActive); c.Status != StatusFail || !strings.Contains(c.Detail, "2023-11-14T22:13:20Z") {
		t.Errorf("active check %+v", c)
	}
	// Resource checks still run so the user sees the whole picture.
	if statusOf(evDead, CheckMemory) != StatusPass {
		t.Error("memory check must still be reported")
	}
	evs := []Evaluation{evDead, Evaluate(job, alive, LevelCluster)}
	if idx, status := Choose(job, evs); idx != 1 || status != "" {
		t.Errorf("Choose = %d %q, want 1", idx, status)
	}
	if _, status := Choose(job, evs[:1]); status != StatusTargetClusterNotActive {
		t.Errorf("only inactive candidates: status %q", status)
	}
	if _, status := Choose(job, nil); status != StatusTargetClusterNotActive {
		t.Errorf("no candidates: status %q", status)
	}
}

func TestActiveUnknownDoesNotFail(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	ev := Evaluate(job, base("1", []string{"docker"}, 1000, 4, 0), LevelNode)
	if !ev.Passed || statusOf(ev, CheckActive) != StatusUnknown {
		t.Errorf("unexpected %+v", ev)
	}
}

// NoGpuCapacity is only reported when the near miss is among the candidates
// the scheduler actually saw. An inactive GPU-less candidate does not count.
func TestNoCapacityIgnoresUnqueriedCandidates(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	job.Vgpus = 1
	inactive := named("c1", base("1", []string{"docker"}, 1000, 4, 0))
	inactive.Active = boolPtr(false)
	small := named("c2", base("2", []string{"docker"}, 10, 4, 0))
	evs := []Evaluation{Evaluate(job, inactive, LevelCluster), Evaluate(job, small, LevelCluster)}
	if _, status := Choose(job, evs); status != StatusNoActiveClusterWithCapacity {
		t.Errorf("status = %q", status)
	}
}

// Ties must break exactly like cpumemfit, which sorts the candidates with an
// unstable sort and takes the last one.
func TestTieBreakMatchesCpumemfit(t *testing.T) {
	job := base("j", []string{"docker"}, 1, 1, 0)
	for n := 1; n <= 40; n++ {
		var cands []Resources
		for i := range n {
			// Few distinct scores so there are plenty of ties.
			cands = append(cands, base(string(rune('a'+i%26))+string(rune('0'+i/26)), []string{"docker"}, float64(1000+(i%3)*10), 4, float64((i*7)%3)*10))
		}
		evals := make([]Evaluation, n)
		for i, c := range cands {
			evals[i] = Evaluate(job, c, LevelCluster)
		}
		idx, _ := Choose(job, evals)

		upstream := slices.Clone(cands)
		slices.SortFunc(upstream, func(a, b Resources) int {
			return cmp.Compare((100.0-a.CPUPercent)+a.Memory, (100.0-b.CPUPercent)+b.Memory)
		})
		if want := upstream[len(upstream)-1].ID; cands[idx].ID != want {
			t.Errorf("n=%d: winner %q, cpumemfit picks %q", n, cands[idx].ID, want)
		}
	}
}

func TestClusterLevelNotesAggregates(t *testing.T) {
	job := base("j", []string{"docker"}, 100, 1, 0)
	job.Vgpus = 1
	c := named("c1", base("1", []string{"docker", "docker"}, 1000, 4, 0))
	c.GpuVramFree = []float64{100}
	c.AggregationPerArchitecture = map[string]json.RawMessage{"arm64": nil, "amd64": nil}
	job.Arch = []string{"amd64"}
	ev := Evaluate(job, c, LevelCluster)
	for _, name := range []string{CheckCPU, CheckMemory, CheckGPUCount} {
		if c, _ := ev.Check(name); !strings.Contains(c.Detail, "cluster value") {
			t.Errorf("%s detail lacks the aggregate note: %q", name, c.Detail)
		}
	}
	if c, _ := ev.Check(CheckVirtualization); !strings.Contains(c.Detail, "supports docker (") {
		t.Errorf("virtualization should be deduplicated: %q", c.Detail)
	}
	if c, _ := ev.Check(CheckArchitecture); !strings.Contains(c.Detail, "amd64, arm64") {
		t.Errorf("architecture detail %q", c.Detail)
	}
}

func TestOnlyFirstVirtualizationCounts(t *testing.T) {
	job := base("j", []string{"unikernel", "docker"}, 1, 1, 0)
	ev := Evaluate(job, base("1", []string{"docker"}, 1000, 4, 0), LevelNode)
	c, _ := ev.Check(CheckVirtualization)
	if ev.Passed || !strings.Contains(c.Detail, "only the first entry") {
		t.Errorf("unexpected %+v", c)
	}
}

func TestResourcesUnmarshalExtras(t *testing.T) {
	raw := `{"_id":{"$oid":"abc"},"virtualization":"docker","arch":"arm64","active":false,
		"candidate_name":"w1","architecture":"amd64","last_modified_timestamp":1.5,
		"constraints":[{"type":"direct","node":"w1"},{"type":"geo","location":[1,2],"threshold":"far"}]}`
	var r Resources
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "abc" || r.Active == nil || *r.Active || !slices.Equal(r.Arch, []string{"arm64"}) ||
		!slices.Equal(r.Virtualization, []string{"docker"}) || r.Architecture != "amd64" {
		t.Errorf("unexpected %+v", r)
	}
	if len(r.Constraints) != 2 || r.Constraints[0].Node != "w1" || r.Constraints[1].Location != "[1,2]" {
		t.Errorf("constraints %+v", r.Constraints)
	}
	// The scheduler decodes node as a string, so a non-string must fail here too.
	if err := json.Unmarshal([]byte(`{"constraints":[{"type":"direct","node":5}]}`), &r); err == nil {
		t.Error("expected error for non-string node")
	}
}
