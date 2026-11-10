package explain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// NamedJob is one microservice of an SLA turned into a scheduler job.
type NamedJob struct {
	Application  string
	Microservice string
	Job          placement.Resources
}

// JobsFromSLA accepts what an agent is likely to have at hand: a full
// application SLA ({"sla_version", "customerID", "applications": [...]}),
// a single application object ({"microservices": [...]}) or a single
// microservice object. Every microservice is validated, and all problems
// are reported at once so the agent can fix the SLA in one go.
func JobsFromSLA(raw json.RawMessage) ([]NamedJob, error) {
	raw = bytes.TrimSpace(raw)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, errors.New("the SLA must be a JSON object: either a full application SLA with " +
			"\"applications\": [{\"microservices\": [...]}] or a single microservice object")
	}

	if apps, ok := top["applications"]; ok {
		var list []json.RawMessage
		if err := json.Unmarshal(apps, &list); err != nil {
			return nil, errors.New("\"applications\" must be a list of application objects")
		}
		if len(list) == 0 {
			return nil, errors.New("\"applications\" is empty; add at least one application with microservices")
		}
		var jobs []NamedJob
		var errs []error
		for i, app := range list {
			js, err := jobsFromApplication(app, fmt.Sprintf("applications[%d]", i))
			jobs = append(jobs, js...)
			if err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		return jobs, nil
	}

	if _, ok := top["microservices"]; ok {
		return jobsFromApplication(raw, "application")
	}

	job, err := placement.JobFromMicroservice(raw)
	if err != nil {
		return nil, fmt.Errorf("microservice %s: %w", describe(top, "microservice_name"), err)
	}
	return []NamedJob{{Microservice: stringField(top, "microservice_name"), Job: job}}, nil
}

func jobsFromApplication(raw json.RawMessage, path string) ([]NamedJob, error) {
	var app struct {
		Name          string            `json:"application_name"`
		Microservices []json.RawMessage `json:"microservices"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		return nil, fmt.Errorf("%s must be an object with application_name and a microservices list: %v", path, err)
	}
	if app.Name != "" {
		path += fmt.Sprintf(" (%q)", app.Name)
	}
	if len(app.Microservices) == 0 {
		return nil, fmt.Errorf("%s has no microservices", path)
	}
	var jobs []NamedJob
	var errs []error
	for j, msRaw := range app.Microservices {
		var ms map[string]json.RawMessage
		_ = json.Unmarshal(msRaw, &ms)
		job, err := placement.JobFromMicroservice(msRaw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s microservices[%d] %s: %w", path, j, describe(ms, "microservice_name"), err))
			continue
		}
		jobs = append(jobs, NamedJob{Application: app.Name, Microservice: stringField(ms, "microservice_name"), Job: job})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return jobs, nil
}

func stringField(m map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

func describe(m map[string]json.RawMessage, key string) string {
	if s := stringField(m, key); s != "" {
		return fmt.Sprintf("%q", s)
	}
	return "(unnamed)"
}
