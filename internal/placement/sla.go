package placement

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// JobFromMicroservice turns one SLA microservice object into the job document
// the scheduler receives, applying the same rewrites as the system manager's
// generate_db_structure: "container" becomes "docker", and a docker job with
// vram but no vgpus asks for one GPU. It also accepts an already stored job
// document, for which these rewrites are no-ops.
//
// Validation is limited to what placement depends on. Missing memory or vcpus
// are rejected even though Oakestra would schedule them as 0, because a dry
// run on a zero request says nothing useful.
func JobFromMicroservice(raw json.RawMessage) (Resources, error) {
	var ms map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&ms); err != nil {
		return Resources{}, fmt.Errorf("microservice must be a JSON object: %w", err)
	}
	if ms == nil {
		return Resources{}, errors.New("microservice must be a JSON object, got null")
	}

	var errs []error

	virt, present := ms["virtualization"]
	switch v := virt.(type) {
	case nil:
		if present {
			errs = append(errs, errors.New("virtualization is null; set it to \"container\", \"unikernel\" or another runtime"))
		} else {
			errs = append(errs, errors.New("virtualization is missing; set it to \"container\", \"unikernel\" or another runtime"))
		}
	case string:
		switch v {
		case "":
			errs = append(errs, errors.New("virtualization is empty"))
		case "container":
			ms["virtualization"] = "docker"
		}
	case []any:
		// Stored job documents may carry a list. A submitted SLA cannot,
		// its schema types virtualization as a string, but the scheduler
		// copes with a list, so only reject an empty one.
		if len(v) == 0 {
			errs = append(errs, errors.New("virtualization is an empty list"))
		}
	default:
		errs = append(errs, fmt.Errorf("virtualization must be a string, got %s", jsonType(virt)))
	}

	for _, field := range []string{"memory", "vcpus"} {
		if err := requireWholeNumber(ms, field); err != nil {
			errs = append(errs, err)
		}
	}
	if err := optionalNumber(ms, "vgpus", true); err != nil {
		errs = append(errs, err)
	}
	if err := optionalNumber(ms, "vram", false); err != nil {
		errs = append(errs, err)
	}

	// generate_db_structure reads microservice["arch"] for unikernels without
	// a default, so the system manager fails the request when it is missing.
	if ms["virtualization"] == "unikernel" {
		if _, ok := ms["arch"]; !ok {
			errs = append(errs, errors.New("arch is required for unikernel services"))
		}
	}

	if len(errs) > 0 {
		return Resources{}, errors.Join(errs...)
	}

	if ms["virtualization"] == "docker" && positive(ms["vram"]) && !positive(ms["vgpus"]) {
		ms["vgpus"] = json.Number("1")
	}

	normalized, err := json.Marshal(ms)
	if err != nil {
		return Resources{}, err
	}
	var job Resources
	if err := json.Unmarshal(normalized, &job); err != nil {
		return Resources{}, fmt.Errorf("microservice does not decode as a scheduler job: %w", err)
	}
	if job.ID == "" {
		if id, ok := ms["microserviceID"].(string); ok {
			job.ID = id
		}
	}
	return job, nil
}

func requireWholeNumber(ms map[string]any, field string) error {
	v, ok := ms[field]
	if !ok || v == nil {
		return fmt.Errorf("%s is missing; Oakestra would schedule it as 0, set the real requirement", field)
	}
	f, err := asFloat(v)
	if err != nil {
		return fmt.Errorf("%s must be a number, got %s", field, jsonType(v))
	}
	if f < 0 || f != math.Trunc(f) {
		return fmt.Errorf("%s must be a non-negative integer, got %v", field, v)
	}
	return nil
}

func optionalNumber(ms map[string]any, field string, integer bool) error {
	v, ok := ms[field]
	if !ok || v == nil {
		return nil
	}
	f, err := asFloat(v)
	if err != nil {
		return fmt.Errorf("%s must be a number, got %s", field, jsonType(v))
	}
	if f < 0 {
		return fmt.Errorf("%s must not be negative, got %v", field, v)
	}
	if integer && f != math.Trunc(f) {
		return fmt.Errorf("%s must be an integer, got %v", field, v)
	}
	return nil
}

func asFloat(v any) (float64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("not a number")
	}
	return n.Float64()
}

// positive mirrors Python truthiness for the numeric checks in
// generate_db_structure, where None, 0 and a missing key are all falsy.
func positive(v any) bool {
	f, err := asFloat(v)
	return err == nil && f > 0
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
