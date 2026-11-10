// Package placement is a port of Oakestra's scheduler filter and choice logic
// that reports every individual check instead of a single bool.
//
// Source of truth: oakestra branch robertjndw/fix-vgpus-and-vram-requirement-sla
// at commit 81ecea8465da063d038ec66f1de0223b5f6304da, files
//
//	scheduler/calculate/schedulers/placement/placement.go
//	scheduler/calculate/schedulers/cpumemfit/cpumemfit.go
//	scheduler/calculate/scheduling.go
//	scheduler/requests/resource/resource.go
//	root_orchestrator/system-manager-python/services/service_management.py (generate_db_structure)
//
// The scheduler module is not importable, so this is a copy. When the scheduler
// changes, update this package and re-run the parity tests in parity_test.go,
// which mirror the upstream table tests case by case.
//
// The same scheduler binary runs at both levels. On the root it picks a cluster
// from the root resource abstractor (cluster documents are aggregates over the
// cluster's active workers). Inside a cluster it picks a worker from the cluster
// resource abstractor. Neither level retries elsewhere: the root reports a
// failed cluster choice back to the system manager, and a cluster that finds no
// worker only marks its own job as failed, so the root never tries the next
// cluster.
package placement
