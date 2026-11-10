package placement

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Level is the scheduling level a candidate belongs to.
type Level string

const (
	// LevelCluster is the root scheduler choosing among clusters.
	LevelCluster Level = "cluster"
	// LevelNode is a cluster scheduler choosing among its workers.
	LevelNode Level = "node"
)

// Status is the outcome of one check.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	// StatusNotEnforced marks constraints the SLA sets but Oakestra ignores.
	StatusNotEnforced Status = "not_enforced"
	// StatusUnknown means the data to decide is missing. It never fails a
	// candidate, because the scheduler would not have failed it either.
	StatusUnknown Status = "unknown"
)

// Check names, in the order the scheduler applies them.
const (
	CheckActive          = "active"
	CheckPinnedTarget    = "pinned_target"
	CheckVirtualization  = "virtualization"
	CheckCPU             = "cpu"
	CheckMemory          = "memory"
	CheckGPUCount        = "gpu_count"
	CheckVRAM            = "vram"
	CheckCSIDriver       = "csi_driver"
	CheckArchitecture    = "architecture"
	CheckGeoArea         = "geo_area"
	CheckLatency         = "latency"
	CheckAddons          = "addons"
	CheckAllowedClusters = "allowed_clusters"
)

// CheckResult is the outcome of one scheduler check for one candidate.
type CheckResult struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Evaluation is the full per-check verdict for one candidate.
type Evaluation struct {
	CandidateID   string        `json:"candidate_id"`
	CandidateName string        `json:"candidate_name"`
	Level         Level         `json:"level"`
	Passed        bool          `json:"passed"`
	Checks        []CheckResult `json:"checks"`
	// Score is cpumemfit's ranking value, (100 - cpu_percent) + memory. The
	// highest score among passing candidates wins.
	Score float64 `json:"score"`
}

// Check returns the result for the named check, if it was evaluated.
func (e Evaluation) Check(name string) (CheckResult, bool) {
	for _, c := range e.Checks {
		if c.Check == name {
			return c, true
		}
	}
	return CheckResult{}, false
}

// gpuRuntime is the only virtualization that gets physical NVIDIA GPUs. For
// crosvm vgpus means virtio-gpu devices and unikernels ignore it.
const gpuRuntime = "docker"

// RequiredGPUs returns how many NVIDIA GPUs the scheduler thinks the job
// needs: vram without vgpus implies one, non-container jobs need none.
func RequiredGPUs(job Resources) int {
	if len(job.Virtualization) == 0 || job.Virtualization[0] != gpuRuntime {
		return 0
	}
	if job.Vgpus <= 0 && job.Vram > 0 {
		return 1
	}
	return max(job.Vgpus, 0)
}

// PinnedName returns the candidate name a "direct" constraint pins the job to
// at this level. Like ResourceConstraints in cpumemfit, the last direct
// constraint wins and an empty name means no filter, because the scheduler
// drops empty query values.
func PinnedName(job Resources, level Level) (string, bool) {
	var name string
	var found bool
	for _, c := range job.Constraints {
		if c.Type != "direct" {
			continue
		}
		found = true
		if level == LevelNode {
			name = c.Node
		} else {
			name = c.Cluster
		}
	}
	return name, found && name != ""
}

// Score is cpumemfit's ranking value for a candidate. Cores play no part in
// the ranking, and memory in MB easily dwarfs the CPU headroom term.
func Score(c Resources) float64 {
	return (100.0 - c.CPUPercent) + c.Memory
}

// Evaluate runs every scheduler check for one candidate without short
// circuiting, so all reasons show up. Passed is exactly what the real
// scheduler would decide for this candidate, including the resource
// abstractor's active=true and candidate_name filtering.
func Evaluate(job, candidate Resources, level Level) Evaluation {
	ev := Evaluation{
		CandidateID:   candidate.ID,
		CandidateName: candidate.CandidateName,
		Level:         level,
		Score:         Score(candidate),
	}

	add := func(name string, s Status, format string, args ...any) {
		ev.Checks = append(ev.Checks, CheckResult{Check: name, Status: s, Detail: fmt.Sprintf(format, args...)})
	}

	active := checkActive(candidate, level)
	ev.Checks = append(ev.Checks, active)
	pinned := checkPinned(job, candidate, level)
	ev.Checks = append(ev.Checks, pinned)

	virtOK := checkVirtualization(job, candidate, level, add)
	cpuOK := checkCPU(job, candidate, level, add)
	memOK := checkMemory(job, candidate, level, add)
	gpuOK := checkGPU(job, candidate, level, add)
	csiOK := checkCSI(job, candidate, level, add)

	addNotEnforced(job, candidate, level, add)

	queried := active.Status != StatusFail && pinned.Status != StatusFail
	ev.Passed = queried && virtOK && cpuOK && memOK && gpuOK && csiOK
	return ev
}

func checkActive(c Resources, level Level) CheckResult {
	r := CheckResult{Check: CheckActive}
	seen := ""
	if c.LastModifiedTimestamp > 0 {
		sec := int64(c.LastModifiedTimestamp)
		seen = fmt.Sprintf(", last report at %s", time.Unix(sec, 0).UTC().Format(time.RFC3339))
	}
	switch {
	case c.Active == nil:
		r.Status = StatusUnknown
		r.Detail = fmt.Sprintf("the %s document has no active flag%s; assuming it is active. "+
			"The scheduler only sees candidates that reported within the last 30 s", level, seen)
	case *c.Active:
		r.Status = StatusPass
		r.Detail = fmt.Sprintf("%s reported within the last 30 s%s", level, seen)
	default:
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("%s has not reported within the last 30 s%s; the resource abstractor "+
			"hides it from the scheduler (active=true query)", level, seen)
	}
	return r
}

func checkPinned(job, c Resources, level Level) CheckResult {
	r := CheckResult{Check: CheckPinnedTarget}
	name, pinned := PinnedName(job, level)
	if !pinned {
		r.Status = StatusPass
		r.Detail = fmt.Sprintf("no direct constraint names a %s", level)
		if level == LevelCluster {
			if node, ok := PinnedName(job, LevelNode); ok {
				r.Detail = fmt.Sprintf("direct constraint pins node %q but no cluster, so every cluster "+
					"is considered and the node is only enforced inside the chosen cluster", node)
			}
		}
		return r
	}
	if c.CandidateName == name {
		r.Status = StatusPass
		r.Detail = fmt.Sprintf("direct constraint pins %s %q, which is this candidate", level, name)
		return r
	}
	r.Status = StatusFail
	r.Detail = fmt.Sprintf("direct constraint pins %s %q, this candidate is %q; the scheduler only "+
		"asks the resource abstractor for candidate_name=%q", level, name, c.CandidateName, name)
	return r
}

type addFunc func(name string, s Status, format string, args ...any)

func aggregateNote(level Level, what string) string {
	if level != LevelCluster {
		return ""
	}
	return fmt.Sprintf(" (cluster value is %s over its active workers, so a single worker may have less)", what)
}

func checkVirtualization(job, c Resources, level Level, add addFunc) bool {
	if len(job.Virtualization) == 0 {
		add(CheckVirtualization, StatusFail, "job has no virtualization; the scheduler rejects every candidate")
		return false
	}
	want := job.Virtualization[0]
	have := dedupe(c.Virtualization)
	note := ""
	if len(job.Virtualization) > 1 {
		note = fmt.Sprintf("; only the first entry is considered (job lists %s)",
			strings.Join(job.Virtualization, ", "))
	}
	if want == "container" {
		note += "; the scheduler expects \"docker\", the system manager only maps a plain string \"container\" to it"
	}
	if level == LevelCluster {
		note += " (cluster value is the union over its active workers)"
	}
	if slices.Contains(c.Virtualization, want) {
		add(CheckVirtualization, StatusPass, "needs %s, candidate supports %s%s", want, listOrNone(have), note)
		return true
	}
	add(CheckVirtualization, StatusFail, "needs %s, candidate supports %s%s", want, listOrNone(have), note)
	return false
}

func checkCPU(job, c Resources, level Level, add addFunc) bool {
	ok := c.VCPUs >= job.VCPUs
	add(CheckCPU, passFail(ok),
		"needs %s vCPUs, candidate has %s cores in total at %s%% CPU usage; vcpus is compared against "+
			"installed cores, not free capacity%s",
		num(job.VCPUs), num(c.VCPUs), num(c.CPUPercent), aggregateNote(level, "summed"))
	return ok
}

func checkMemory(job, c Resources, level Level, add addFunc) bool {
	ok := c.Memory >= job.Memory
	add(CheckMemory, passFail(ok), "needs %s MB, candidate reports %s MB%s",
		num(job.Memory), num(c.Memory), aggregateNote(level, "summed"))
	return ok
}

// checkGPU mirrors MeetsGPURequirements, reported as two checks.
func checkGPU(job, c Resources, level Level, add addFunc) bool {
	n := RequiredGPUs(job)
	if n == 0 {
		detail := "job needs no NVIDIA GPU"
		if job.Vgpus > 0 || job.Vram > 0 {
			virt := "no virtualization"
			if len(job.Virtualization) > 0 {
				virt = job.Virtualization[0]
			}
			detail = fmt.Sprintf("vgpus/vram are only matched against NVIDIA GPUs for docker jobs, "+
				"this job is %s, so they are not filtered on", virt)
		}
		add(CheckGPUCount, StatusPass, "%s", detail)
		add(CheckVRAM, StatusPass, "%s", detail)
		return true
	}

	free, legacy := freeVramPerGPU(c)
	source := "free VRAM per GPU (gpu_vram_free)"
	if legacy {
		source = fmt.Sprintf("no gpu_vram_free reported, so the scheduler assumes the installed %s MiB "+
			"is spread evenly over %d GPUs and entirely free", num(c.Vram), c.Vgpus)
	}
	clusterNote := ""
	if level == LevelCluster {
		clusterNote = " (cluster value is the per-GPU-slot maximum over its workers, so passing only " +
			"means some worker might fit)"
	}
	implied := ""
	if job.Vgpus <= 0 {
		implied = " (vram without vgpus implies one GPU)"
	}

	countOK := len(free) >= n
	add(CheckGPUCount, passFail(countOK), "needs %d GPU(s)%s, candidate has %d; %s%s",
		n, implied, len(free), source, clusterNote)

	if !countOK {
		if job.Vram > 0 {
			freeDesc := "no GPUs"
			if len(free) > 0 {
				freeDesc = "free per GPU: " + numList(free) + " MiB"
			}
			add(CheckVRAM, StatusUnknown, "needs %s MiB across %d GPU(s); not comparable because the "+
				"candidate has too few GPUs (%s)", num(job.Vram), n, freeDesc)
		} else {
			add(CheckVRAM, StatusPass, "no VRAM requested")
		}
		return false
	}
	var sum float64
	for _, v := range free[:n] {
		sum += v
	}
	ok := sum >= job.Vram
	add(CheckVRAM, passFail(ok), "needs %s MiB across %d GPU(s), best %d free: %s = %s MiB%s",
		num(job.Vram), n, n, joinNums(free[:n], "+"), num(sum), clusterNote)
	return ok
}

// freeVramPerGPU mirrors placement.freeVramPerGPU and also reports whether
// the legacy even-split fallback was used.
func freeVramPerGPU(c Resources) ([]float64, bool) {
	if len(c.GpuVramFree) == 0 {
		if c.Vgpus <= 0 {
			return nil, false
		}
		per := c.Vram / float64(c.Vgpus)
		free := make([]float64, c.Vgpus)
		for i := range free {
			free[i] = per
		}
		return free, true
	}
	free := slices.Clone(c.GpuVramFree)
	slices.SortFunc(free, func(a, b float64) int { return cmp.Compare(b, a) })
	return free, false
}

func checkCSI(job, c Resources, level Level, add addFunc) bool {
	var needed, missing []string
	for _, v := range job.Volumes {
		if v.CSIDriver == "" {
			continue
		}
		needed = append(needed, v.CSIDriver)
		if !slices.Contains(c.CSIDrivers, v.CSIDriver) {
			missing = append(missing, v.CSIDriver)
		}
	}
	if len(needed) == 0 {
		add(CheckCSIDriver, StatusPass, "job has no CSI volumes")
		return true
	}
	note := ""
	if level == LevelCluster {
		note = " (cluster value is the union over its active workers)"
	}
	if len(missing) == 0 {
		add(CheckCSIDriver, StatusPass, "needs %s, candidate has %s%s",
			strings.Join(dedupe(needed), ", "), listOrNone(c.CSIDrivers), note)
		return true
	}
	add(CheckCSIDriver, StatusFail, "missing %s, candidate has %s%s",
		strings.Join(dedupe(missing), ", "), listOrNone(c.CSIDrivers), note)
	return false
}

const notEnforced = "; Oakestra does not enforce this"

func addNotEnforced(job, c Resources, level Level, add addFunc) {
	if len(job.Arch) > 0 {
		var have string
		switch {
		case c.Architecture != "":
			have = fmt.Sprintf("%s architecture %s", level, c.Architecture)
		case len(c.AggregationPerArchitecture) > 0:
			archs := make([]string, 0, len(c.AggregationPerArchitecture))
			for a := range c.AggregationPerArchitecture {
				archs = append(archs, a)
			}
			slices.Sort(archs)
			have = fmt.Sprintf("%s worker architectures %s", level, strings.Join(archs, ", "))
		default:
			have = fmt.Sprintf("%s reports no architecture", level)
		}
		add(CheckArchitecture, StatusNotEnforced, "SLA arch %s, %s%s", strings.Join(job.Arch, ", "), have, notEnforced)
	}

	loc := c.CandidateLocation
	if loc == "" {
		loc = "unknown"
	}
	for _, con := range job.Constraints {
		switch con.Type {
		case "geo":
			add(CheckGeoArea, StatusNotEnforced, "SLA asks for placement within %s km of %s, candidate location %s%s",
				num(con.Threshold), cmp.Or(con.Location, "(unset)"), loc, notEnforced)
		case "latency":
			add(CheckLatency, StatusNotEnforced, "SLA asks for at most %s ms latency in area %s (rigidness %s), "+
				"candidate location %s%s", num(con.Threshold), cmp.Or(con.Area, "(unset)"), num(con.Rigidness), loc, notEnforced)
		case "addons":
			add(CheckAddons, StatusNotEnforced, "SLA needs addons %s%s", listOrNone(con.Needs), notEnforced)
		case "clusters":
			add(CheckAllowedClusters, StatusNotEnforced, "SLA allows clusters %s, candidate is %q%s",
				listOrNone(con.Allowed), c.CandidateName, notEnforced)
		}
	}
}

func passFail(ok bool) Status {
	if ok {
		return StatusPass
	}
	return StatusFail
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func joinNums(vs []float64, sep string) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = num(v)
	}
	return strings.Join(parts, sep)
}

func numList(vs []float64) string {
	if len(vs) == 0 {
		return "none"
	}
	return joinNums(vs, ", ")
}

func listOrNone(vs []string) string {
	if len(vs) == 0 {
		return "none"
	}
	return strings.Join(vs, ", ")
}

// dedupe keeps first occurrences. Cluster documents concatenate their
// workers' lists, so "docker, docker, docker" is common.
func dedupe(vs []string) []string {
	var out []string
	for _, v := range vs {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
