package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/explain"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
	"github.com/robertjndw/oakestra-mcp/internal/placement"
)

// MaxInstances caps instances per service in one action. Oakestra scales
// one instance per call, so a typo like 200 would otherwise turn into 200
// scheduler requests.
const MaxInstances = 20

const (
	opCreateApplication = "create_application"
	opDeployInstance    = "deploy_instance"
	opUndeployInstance  = "undeploy_instance"
	opDeleteApplication = "delete_application"
)

// statusRequested is the service status while a deploy request waits for
// the scheduler. Such an instance is not in instance_list yet.
const statusRequested = "REQUESTED"

// ---- deploy_application ----

type deployParams struct {
	SLA       json.RawMessage `json:"sla"`
	Instances int             `json:"instances_per_service"`
}

// PrepareDeploy plans creating the applications of an SLA and requesting
// instances for each microservice. instances defaults to 1.
func (e *Engine) PrepareDeploy(ctx context.Context, p auth.Principal, c *oakapi.Client, sla json.RawMessage, instances *int) (Prepared, error) {
	n := 1
	if instances != nil {
		n = *instances
	}
	if n < 0 || n > MaxInstances {
		return Prepared{}, fmt.Errorf("instances_per_service must be between 0 and %d, got %d", MaxInstances, n)
	}
	norm, err := normalizeSLA(sla, p.User)
	if err != nil {
		return Prepared{}, err
	}
	pl, err := e.planDeploy(ctx, p, c, deployParams{SLA: norm, Instances: n}, true)
	if err != nil {
		return Prepared{}, err
	}
	return e.save(ctx, p, pl)
}

type slaApp struct {
	name, namespace string
	microservices   []string
	raw             map[string]any
}

// Oakestra's SLA schema restricts these names to this pattern and answers
// 422 otherwise; checking here lets prepare reject the SLA instead of
// confirm.
var slaNameRe = regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`)

// normalizeSLA turns what the agent sent into the full SLA Oakestra's
// POST /api/application expects and returns it as canonical JSON, so the
// stored params (and with them the plan hash) do not depend on key order.
// A single application object is wrapped; a lone microservice is refused
// because creating it needs an application name and namespace.
func normalizeSLA(raw json.RawMessage, user string) (json.RawMessage, error) {
	top, err := decodeObject(raw)
	if err != nil {
		return nil, errors.New("sla must be a JSON object: a full application SLA ({\"sla_version\", \"customerID\", " +
			"\"applications\": [...]}) or a single application object with application_name, application_namespace and microservices")
	}
	if _, ok := top["applications"]; !ok {
		if _, ok := top["microservices"]; !ok {
			return nil, errors.New("sla looks like a single microservice; deploying needs an application around it: " +
				"pass {\"application_name\", \"application_namespace\", \"microservices\": [<this microservice>]} or a full SLA")
		}
		top = map[string]any{"applications": []any{top}}
	}
	if v, ok := top["sla_version"]; !ok || v == "" {
		top["sla_version"] = "v2.0"
	} else if v != "v2.0" {
		return nil, fmt.Errorf("sla_version %v is not supported; use \"v2.0\"", v)
	}
	// Oakestra ignores customerID and uses the token's user, but its schema
	// requires the field.
	if _, ok := top["customerID"]; !ok {
		top["customerID"] = user
	}
	return json.Marshal(top)
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as written, e.g. a memory of 512 does not become 512.0
	// on its way to Oakestra.
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not an object")
	}
	return m, nil
}

// slaApps checks what Oakestra's schema would reject on create and what the
// executor relies on (unique microservice names to map the new service
// IDs), reporting every problem at once.
func slaApps(top map[string]any) ([]slaApp, error) {
	list, ok := top["applications"].([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("\"applications\" must be a non-empty list")
	}
	var apps []slaApp
	var errs []error
	for i, a := range list {
		m, ok := a.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("applications[%d] is not an object", i))
			continue
		}
		app := slaApp{raw: m}
		app.name, _ = m["application_name"].(string)
		app.namespace, _ = m["application_namespace"].(string)
		where := fmt.Sprintf("applications[%d]", i)
		if !slaNameRe.MatchString(app.name) {
			errs = append(errs, fmt.Errorf("%s: application_name %q must be 1 to 32 letters or digits", where, app.name))
		}
		if !slaNameRe.MatchString(app.namespace) {
			errs = append(errs, fmt.Errorf("%s: application_namespace %q must be 1 to 32 letters or digits", where, app.namespace))
		}
		mss, _ := m["microservices"].([]any)
		if len(mss) == 0 {
			errs = append(errs, fmt.Errorf("%s has no microservices", where))
		}
		for j, x := range mss {
			ms, _ := x.(map[string]any)
			name, _ := ms["microservice_name"].(string)
			ns, _ := ms["microservice_namespace"].(string)
			code, _ := ms["code"].(string)
			msWhere := fmt.Sprintf("%s.microservices[%d]", where, j)
			if !slaNameRe.MatchString(name) {
				errs = append(errs, fmt.Errorf("%s: microservice_name %q must be 1 to 32 letters or digits", msWhere, name))
			}
			if !slaNameRe.MatchString(ns) {
				errs = append(errs, fmt.Errorf("%s: microservice_namespace %q must be 1 to 32 letters or digits", msWhere, ns))
			}
			if strings.TrimSpace(code) == "" {
				errs = append(errs, fmt.Errorf("%s: code (the image or unikernel reference) is missing", msWhere))
			}
			if slices.Contains(app.microservices, name) {
				errs = append(errs, fmt.Errorf("%s: microservice_name %q appears twice in this application", msWhere, name))
			}
			app.microservices = append(app.microservices, name)
		}
		apps = append(apps, app)
	}
	for i := range apps {
		for j := range i {
			if apps[i].name == apps[j].name && apps[i].namespace == apps[j].namespace {
				errs = append(errs, fmt.Errorf("application %s/%s appears twice in the SLA", apps[i].namespace, apps[i].name))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return apps, nil
}

func (e *Engine) planDeploy(ctx context.Context, p auth.Principal, c *oakapi.Client, in deployParams, dry bool) (planned, error) {
	jobs, err := explain.JobsFromSLA(in.SLA)
	if err != nil {
		return planned{}, fmt.Errorf("the SLA is not valid: %w", err)
	}
	top, err := decodeObject(in.SLA)
	if err != nil {
		return planned{}, err
	}
	apps, err := slaApps(top)
	if err != nil {
		return planned{}, fmt.Errorf("the SLA is not valid: %w", err)
	}

	pl := planned{kind: KindDeployApplication, params: in}
	// Oakestra answers 409 for a name clash only after creating the
	// applications before it in the same request, so catching it here
	// avoids a half-done action. If the list cannot be read the clash
	// surfaces at confirm time instead.
	if existing, err := oakestra.ListOwnApplications(ctx, c, p); err == nil {
		for _, a := range apps {
			for _, x := range existing {
				if x.ApplicationName == a.name && x.ApplicationNamespace == a.namespace {
					return planned{}, fmt.Errorf("an application %s in namespace %s already exists (ID %s); Oakestra "+
						"would refuse to create it. Pick another name or namespace, or delete the existing one first",
						a.name, a.namespace, x.ApplicationID)
				}
			}
		}
	} else {
		pl.warnings = append(pl.warnings, "Could not check whether the application names are already taken; "+
			"Oakestra refuses duplicates (same name and namespace) when the action runs.")
	}

	for i, a := range apps {
		pl.steps = append(pl.steps, step{
			Step: Step{Op: opCreateApplication, Target: a.namespace + "/" + a.name,
				Detail: fmt.Sprintf("registers the application and its microservices %s; deploys nothing yet", strings.Join(a.microservices, ", "))},
			run: createApplication(top, a, i),
		})
	}
	for i, a := range apps {
		for _, ms := range a.microservices {
			for k := range in.Instances {
				pl.steps = append(pl.steps, step{
					Step: Step{Op: opDeployInstance, Target: a.name + "/" + ms,
						Detail: fmt.Sprintf("instance request %d of %d; the service ID is known once the application exists", k+1, in.Instances)},
					run: deployCreated(serviceKey(i, ms)),
				})
			}
		}
	}

	var names []string
	for _, a := range apps {
		names = append(names, fmt.Sprintf("%s (namespace %s, microservices %s)", a.name, a.namespace, strings.Join(a.microservices, ", ")))
	}
	pl.summary = fmt.Sprintf("Create %s: %s", plural(len(apps), "application"), strings.Join(names, "; "))
	if in.Instances > 0 {
		pl.summary += fmt.Sprintf(". Then request %s of every microservice (%s in total).",
			plural(in.Instances, "instance"), plural(in.Instances*len(jobs), "deploy request"))
	} else {
		pl.summary += ". No instances are requested (instances_per_service is 0); use prepare_scale_service later."
	}

	if dry && in.Instances > 0 {
		e.addDryRuns(ctx, &pl, jobs)
	}
	return pl, nil
}

func (e *Engine) addDryRuns(ctx context.Context, pl *planned, jobs []explain.NamedJob) {
	if e.placement == nil {
		pl.warnings = append(pl.warnings, "No placement dry run: resource data is not configured on this server.")
		return
	}
	src := explain.Memoize(e.placement)
	for _, j := range jobs {
		res, err := explain.DryRun(ctx, src, j.Job, explain.Options{})
		if err != nil {
			pl.warnings = append(pl.warnings, fmt.Sprintf("Placement dry run unavailable (%v); the action can still run.", err))
			return
		}
		pl.dryRun = append(pl.dryRun, DryRun{Application: j.Application, Microservice: j.Microservice, Result: res})
		if !res.Outcome.WouldDeploy {
			pl.warnings = append(pl.warnings, fmt.Sprintf("%s would NOT be placed right now: %s Confirming still runs every step, "+
				"but the instance requests will fail to schedule until resources free up or the SLA changes.",
				jobName(j), res.Outcome.Summary))
		}
	}
}

func jobName(j explain.NamedJob) string {
	switch {
	case j.Application != "" && j.Microservice != "":
		return j.Application + "/" + j.Microservice
	case j.Microservice != "":
		return j.Microservice
	}
	return "the service"
}

func serviceKey(app int, ms string) string { return strconv.Itoa(app) + "/" + ms }

func createApplication(top map[string]any, a slaApp, idx int) func(context.Context, *oakapi.Client, *execState) (string, error) {
	return func(ctx context.Context, c *oakapi.Client, st *execState) (string, error) {
		// One request per application, so a failure on the second one
		// leaves a clear partial state instead of an unknown one.
		body := make(map[string]any, len(top))
		for k, v := range top {
			body[k] = v
		}
		body["applications"] = []any{a.raw}
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		// The answer is every application of the user, not only the new
		// one, so the new one is found by name and namespace (unique per
		// user in Oakestra).
		apps, _, err := c.Applications.Create(ctx, json.RawMessage(b))
		if err != nil {
			return "", err
		}
		var appID string
		for _, x := range apps {
			if x.ApplicationName == a.name && x.ApplicationNamespace == a.namespace {
				appID = x.ApplicationID
			}
		}
		if appID == "" {
			return "", fmt.Errorf("Oakestra accepted the request but application %s/%s is not in its answer", a.namespace, a.name)
		}
		svcs, _, err := c.Services.List(ctx, &oakapi.ServiceListOptions{ApplicationID: appID})
		if err != nil {
			return "", fmt.Errorf("application %s was created but its services could not be listed: %w", appID, err)
		}
		var parts, missing []string
		for _, ms := range a.microservices {
			id := ""
			for _, s := range svcs {
				if s.MicroserviceName == ms {
					id = s.MicroserviceID
				}
			}
			if id == "" {
				missing = append(missing, ms)
				continue
			}
			st.serviceIDs[serviceKey(idx, ms)] = id
			parts = append(parts, ms+"="+id)
		}
		// Oakestra skips microservices it fails to register and still
		// answers 200, so a missing one only shows up here.
		if len(missing) > 0 {
			return "", fmt.Errorf("application %s was created, but Oakestra did not register microservices %s",
				appID, strings.Join(missing, ", "))
		}
		return fmt.Sprintf("application ID %s, services %s", appID, strings.Join(parts, ", ")), nil
	}
}

func deployCreated(key string) func(context.Context, *oakapi.Client, *execState) (string, error) {
	return func(ctx context.Context, c *oakapi.Client, st *execState) (string, error) {
		id := st.serviceIDs[key]
		if id == "" {
			return "", errors.New("the service ID is unknown because its application was not created")
		}
		if _, err := c.Services.DeployInstance(ctx, id); err != nil {
			return "", err
		}
		return "requested for service " + id, nil
	}
}

// ---- service state shared by scale and undeploy ----

type serviceDoc struct {
	ID          string `json:"microserviceID"`
	Name        string `json:"microservice_name"`
	AppName     string `json:"app_name"`
	Application string `json:"application_name"`
	Status      string `json:"status"`
	Instances   []struct {
		Number json.RawMessage `json:"instance_number"`
		Status string          `json:"status"`
	} `json:"instance_list"`
}

type instance struct {
	Number int
	Status string
}

type serviceState struct {
	doc       serviceDoc
	raw       json.RawMessage
	instances []instance // ascending by number
}

func (s serviceState) label() string {
	app := s.doc.Application
	if app == "" {
		app = s.doc.AppName
	}
	l := fmt.Sprintf("service %s (%s)", s.doc.Name, s.doc.ID)
	if app != "" {
		l += " of application " + app
	}
	return l
}

func (s serviceState) numbers() []int {
	ns := make([]int, 0, len(s.instances))
	for _, in := range s.instances {
		ns = append(ns, in.Number)
	}
	return ns
}

// loadService reads the service document. Every entry of instance_list
// counts as an instance, whatever its status: it occupies a number and
// Oakestra undeploys it like any other. Instances still waiting for the
// scheduler are not in the list yet (the system manager appends them once
// a cluster accepts), which is why plans warn while the service is
// REQUESTED.
func loadService(ctx context.Context, c *oakapi.Client, id string) (serviceState, error) {
	raw, err := oakestra.GetServiceRaw(ctx, c, id)
	if err != nil {
		return serviceState{}, err
	}
	var doc serviceDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return serviceState{}, fmt.Errorf("service %s: unexpected document: %w", id, err)
	}
	if doc.ID == "" {
		doc.ID = id
	}
	st := serviceState{doc: doc, raw: raw}
	for _, in := range doc.Instances {
		n, ok := instanceNumber(in.Number)
		if !ok {
			return serviceState{}, fmt.Errorf("service %s has an instance without a usable instance_number", id)
		}
		st.instances = append(st.instances, instance{Number: n, Status: in.Status})
	}
	slices.SortFunc(st.instances, func(a, b instance) int { return a.Number - b.Number })
	return st, nil
}

// instanceNumber accepts an int or a numeric string; the system manager
// stores an int, but cluster reports type it as a string.
func instanceNumber(raw json.RawMessage) (int, bool) {
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

func pendingWarning(s serviceState) []string {
	if strings.EqualFold(s.doc.Status, statusRequested) {
		return []string{"A deploy request for this service is still waiting for the scheduler. That instance is not " +
			"counted yet; if it lands before you confirm, the plan changes and confirm_action refuses."}
	}
	return nil
}

func undeployStep(s serviceState, in instance) step {
	id, n := s.doc.ID, in.Number
	return step{
		Step: Step{Op: opUndeployInstance, Target: fmt.Sprintf("%s#%d", id, n),
			Detail: fmt.Sprintf("instance %d of %s, currently %s", n, s.doc.Name, orUnknown(in.Status))},
		run: func(ctx context.Context, c *oakapi.Client, _ *execState) (string, error) {
			if _, err := c.Services.UndeployInstance(ctx, id, n); err != nil {
				return "", err
			}
			return "undeploy requested", nil
		},
	}
}

// ---- scale_service ----

type scaleParams struct {
	ServiceID string `json:"service_id"`
	Replicas  int    `json:"replicas"`
}

// PrepareScale plans bringing a service to replicas instances.
func (e *Engine) PrepareScale(ctx context.Context, p auth.Principal, c *oakapi.Client, service string, replicas int) (Prepared, error) {
	if replicas < 0 || replicas > MaxInstances {
		return Prepared{}, fmt.Errorf("replicas must be between 0 and %d, got %d", MaxInstances, replicas)
	}
	id, err := resolveService(ctx, c, service)
	if err != nil {
		return Prepared{}, err
	}
	pl, err := e.planScale(ctx, c, scaleParams{ServiceID: id, Replicas: replicas}, true)
	if err != nil {
		return Prepared{}, err
	}
	return e.save(ctx, p, pl)
}

func (e *Engine) planScale(ctx context.Context, c *oakapi.Client, in scaleParams, dry bool) (planned, error) {
	s, err := loadService(ctx, c, in.ServiceID)
	if err != nil {
		return planned{}, err
	}
	cur := len(s.instances)
	pl := planned{
		kind: KindScaleService, params: in,
		state:    map[string]any{"instances": s.numbers()},
		warnings: pendingWarning(s),
	}
	switch {
	case in.Replicas > cur:
		k := in.Replicas - cur
		id := s.doc.ID
		for i := range k {
			pl.steps = append(pl.steps, step{
				Step: Step{Op: opDeployInstance, Target: id,
					Detail: fmt.Sprintf("new instance %d of %d; Oakestra assigns the instance number", i+1, k)},
				run: func(ctx context.Context, c *oakapi.Client, _ *execState) (string, error) {
					if _, err := c.Services.DeployInstance(ctx, id); err != nil {
						return "", err
					}
					return "requested", nil
				},
			})
		}
		pl.summary = fmt.Sprintf("Scale %s from %d to %d instances: request %s.",
			s.label(), cur, in.Replicas, plural(k, "new instance"))
		if dry {
			e.addServiceDryRun(ctx, &pl, s, k)
		}
	case in.Replicas < cur:
		// Highest numbers first: they are the newest instances, and
		// removing them keeps the long-running ones in place.
		gone := slices.Clone(s.instances[in.Replicas:])
		slices.Reverse(gone)
		var nums []string
		for _, inst := range gone {
			pl.steps = append(pl.steps, undeployStep(s, inst))
			nums = append(nums, "#"+strconv.Itoa(inst.Number))
		}
		pl.summary = fmt.Sprintf("Scale %s from %d to %d instances: undeploy instances %s (highest numbers first).",
			s.label(), cur, in.Replicas, strings.Join(nums, ", "))
	default:
		pl.summary = fmt.Sprintf("%s already has %s.", s.label(), plural(cur, "instance"))
	}
	return pl, nil
}

func (e *Engine) addServiceDryRun(ctx context.Context, pl *planned, s serviceState, k int) {
	job, err := placement.JobFromMicroservice(s.raw)
	if err != nil {
		pl.warnings = append(pl.warnings, fmt.Sprintf("No placement dry run: the stored service is not a valid scheduler job (%v).", err))
		return
	}
	e.addDryRuns(ctx, pl, []explain.NamedJob{{Application: s.doc.Application, Microservice: s.doc.Name, Job: job}})
	if k > 1 && len(pl.dryRun) > 0 {
		pl.warnings = append(pl.warnings, fmt.Sprintf("The dry run predicts the first new instance only; the other %d "+
			"are scheduled one after another and each one uses up capacity the next one could have used.", k-1))
	}
}

// ---- undeploy_service ----

type undeployParams struct {
	ServiceID      string `json:"service_id"`
	InstanceNumber *int   `json:"instance_number,omitempty"`
}

// PrepareUndeploy plans removing one instance, or all instances when
// instanceNumber is nil. The service itself stays registered.
func (e *Engine) PrepareUndeploy(ctx context.Context, p auth.Principal, c *oakapi.Client, service string, instanceNumber *int) (Prepared, error) {
	if instanceNumber != nil && *instanceNumber < 0 {
		return Prepared{}, fmt.Errorf("instance_number must be 0 or greater, got %d", *instanceNumber)
	}
	id, err := resolveService(ctx, c, service)
	if err != nil {
		return Prepared{}, err
	}
	pl, err := e.planUndeploy(ctx, c, undeployParams{ServiceID: id, InstanceNumber: instanceNumber})
	if err != nil {
		return Prepared{}, err
	}
	return e.save(ctx, p, pl)
}

func (e *Engine) planUndeploy(ctx context.Context, c *oakapi.Client, in undeployParams) (planned, error) {
	s, err := loadService(ctx, c, in.ServiceID)
	if err != nil {
		return planned{}, err
	}
	pl := planned{kind: KindUndeployService, params: in, state: map[string]any{"instances": s.numbers()},
		warnings: pendingWarning(s)}
	if in.InstanceNumber != nil {
		i := slices.IndexFunc(s.instances, func(x instance) bool { return x.Number == *in.InstanceNumber })
		if i < 0 {
			// Oakestra answers 200 for an unknown instance number and does
			// nothing, so this has to be caught here.
			return planned{}, fmt.Errorf("%s has no instance %d; its instances are %s",
				s.label(), *in.InstanceNumber, numberList(s.numbers()))
		}
		pl.steps = []step{undeployStep(s, s.instances[i])}
		pl.summary = fmt.Sprintf("Undeploy instance #%d of %s, leaving %s. The service stays registered.",
			*in.InstanceNumber, s.label(), plural(len(s.instances)-1, "instance"))
		return pl, nil
	}
	for i := len(s.instances) - 1; i >= 0; i-- {
		pl.steps = append(pl.steps, undeployStep(s, s.instances[i]))
	}
	if len(s.instances) == 0 {
		pl.summary = s.label() + " has no instances to undeploy."
	} else {
		pl.summary = fmt.Sprintf("Undeploy all %s of %s (%s). The service stays registered with 0 instances; "+
			"use prepare_delete_application to remove it entirely.", plural(len(s.instances), "instance"), s.label(), numberList(s.numbers()))
	}
	return pl, nil
}

// ---- delete_application ----

type deleteParams struct {
	ApplicationID string `json:"application_id"`
}

// PrepareDelete plans deleting an application with all its services.
func (e *Engine) PrepareDelete(ctx context.Context, p auth.Principal, c *oakapi.Client, application string) (Prepared, error) {
	ref := strings.TrimSpace(application)
	if ref == "" {
		return Prepared{}, errors.New("application is required: pass an application ID or name (see list_applications)")
	}
	app, err := oakestra.ResolveApplication(ctx, c, p, ref)
	if err != nil {
		return Prepared{}, err
	}
	pl, err := e.planDelete(ctx, c, deleteParams{ApplicationID: app.ApplicationID})
	if err != nil {
		return Prepared{}, err
	}
	return e.save(ctx, p, pl)
}

func (e *Engine) planDelete(ctx context.Context, c *oakapi.Client, in deleteParams) (planned, error) {
	app, _, err := c.Applications.Get(ctx, in.ApplicationID)
	if err != nil {
		return planned{}, err
	}
	svcs, _, err := c.Services.List(ctx, &oakapi.ServiceListOptions{ApplicationID: in.ApplicationID})
	if err != nil {
		return planned{}, err
	}
	slices.SortFunc(svcs, func(a, b oakapi.Service) int { return strings.Compare(a.MicroserviceID, b.MicroserviceID) })

	type svcState struct {
		ID        string `json:"id"`
		Instances []int  `json:"instances"`
	}
	state := make([]svcState, 0, len(svcs))
	var gone []string
	total := 0
	for _, s := range svcs {
		var nums []int
		for _, in := range s.InstanceList {
			nums = append(nums, in.InstanceNumber)
		}
		slices.Sort(nums)
		total += len(nums)
		state = append(state, svcState{ID: s.MicroserviceID, Instances: nums})
		gone = append(gone, fmt.Sprintf("%s (%s, instances %s)", s.MicroserviceName, s.MicroserviceID, numberList(nums)))
	}
	label := fmt.Sprintf("%s (%s, namespace %s)", app.ApplicationName, app.ApplicationID, app.ApplicationNamespace)
	detail := "no services"
	if len(gone) > 0 {
		detail = "removes " + strings.Join(gone, "; ")
	}
	id := app.ApplicationID
	pl := planned{
		kind: KindDeleteApplication, params: in, state: state,
		steps: []step{{
			Step: Step{Op: opDeleteApplication, Target: id, Detail: detail},
			run: func(ctx context.Context, c *oakapi.Client, _ *execState) (string, error) {
				if _, err := c.Applications.Delete(ctx, id); err != nil {
					return "", err
				}
				return "deleted", nil
			},
		}},
		summary: fmt.Sprintf("Delete application %s together with its %s and their %s. This cannot be undone.",
			label, plural(len(svcs), "service"), plural(total, "instance")),
	}
	if len(gone) > 0 {
		pl.summary += " Going away: " + strings.Join(gone, "; ") + "."
	}
	return pl, nil
}

// ---- helpers ----

func resolveService(ctx context.Context, c *oakapi.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("service is required: pass a service ID or name (see list_services)")
	}
	// Resolving with the caller's client limits non-admins to their own
	// services, and storing the ID keeps a later rename or a new service
	// with the same name from changing what confirm acts on.
	s, _, err := c.Services.ResolveByNameOrID(ctx, ref)
	if err != nil {
		return "", err
	}
	return s.MicroserviceID, nil
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

func numberList(ns []int) string {
	if len(ns) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, "#"+strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}

func orUnknown(s string) string {
	if s == "" {
		return "in unknown status"
	}
	return s
}
