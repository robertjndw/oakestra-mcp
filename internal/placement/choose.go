package placement

import (
	"cmp"
	"slices"
)

// Wire strings the scheduler reports to its manager on failure. They are part
// of Oakestra's API and show up as the service status.
const (
	StatusTargetClusterNotFound       = "TargetClusterNotFound"
	StatusTargetClusterNotActive      = "TargetClusterNotActive"
	StatusNoActiveClusterWithCapacity = "NoActiveClusterWithCapacity"
	StatusNoWorkerCapacity            = "NO_WORKER_CAPACITY"
	StatusNoQualifiedWorkerFound      = "NO_QUALIFIED_WORKER_FOUND"
	StatusNoNodeFound                 = "NO_NODE_FOUND"
	StatusNoGpuCapacity               = "NoGpuCapacity"
)

// Choose reproduces cpumemfit.Calculate over evaluations produced by Evaluate
// for one job at one level, in the order the resource abstractor returned the
// candidates. It returns the index of the winner in evals, or -1 and the
// status string the scheduler would report.
//
// The scheduler uses the same strings at both levels, so a cluster that has no
// fitting worker reports NoActiveClusterWithCapacity, and a pin to an unknown
// or inactive node reports TargetClusterNotActive.
func Choose(job Resources, evals []Evaluation) (winner int, status string) {
	// The scheduler never sees inactive or wrongly named candidates, they are
	// filtered out by the resource abstractor query.
	var queried []int
	for i, e := range evals {
		if !failed(e, CheckActive) && !failed(e, CheckPinnedTarget) {
			queried = append(queried, i)
		}
	}
	if len(queried) == 0 {
		return -1, StatusTargetClusterNotActive
	}

	var filtered []int
	for _, i := range queried {
		if evals[i].Passed {
			filtered = append(filtered, i)
		}
	}
	if len(filtered) == 0 {
		return -1, noCapacityStatus(job, queried, func(i int) bool { return meetsAllButGPU(evals[i]) })
	}

	// Sort exactly like cpumemfit (unstable pdqsort, ascending, take the last)
	// so ties resolve the same way. Sorting indices instead of candidates does
	// not change the permutation, pdqsort only depends on comparison results.
	slices.SortFunc(filtered, func(a, b int) int { return cmp.Compare(evals[a].Score, evals[b].Score) })
	return filtered[len(filtered)-1], ""
}

func failed(e Evaluation, check string) bool {
	c, ok := e.Check(check)
	return ok && c.Status == StatusFail
}

// meetsAllButGPU is cpumemfit's meetsAllButGPU expressed over check results.
func meetsAllButGPU(e Evaluation) bool {
	for _, name := range []string{CheckVirtualization, CheckCPU, CheckMemory, CheckCSIDriver} {
		if failed(e, name) {
			return false
		}
	}
	return true
}

// noCapacityStatus is placement.NoCapacityStatus: the specific GPU status
// only when the job needs a GPU and some candidate misses nothing else.
func noCapacityStatus[C any](job Resources, candidates []C, meetsAllButGPU func(C) bool) string {
	if RequiredGPUs(job) > 0 && slices.ContainsFunc(candidates, meetsAllButGPU) {
		return StatusNoGpuCapacity
	}
	return StatusNoActiveClusterWithCapacity
}
