package mcpserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
)

// Application is an Oakestra application.
type Application struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Description string   `json:"description,omitempty"`
	ServiceIDs  []string `json:"service_ids" jsonschema:"IDs of the services (microservices) in this application"`
}

func applicationFrom(a oakapi.Application) Application {
	ids := a.Microservices
	if ids == nil {
		ids = []string{}
	}
	return Application{ID: a.ApplicationID, Name: a.ApplicationName, Namespace: a.ApplicationNamespace,
		Description: a.ApplicationDesc, ServiceIDs: ids}
}

// ServiceSummary is a service without its deployment details.
type ServiceSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ApplicationID   string `json:"application_id"`
	ApplicationName string `json:"application_name,omitempty"`
	Status          string `json:"status,omitempty" jsonschema:"service status as reported by Oakestra, e.g. RUNNING, SCHEDULED, FAILED"`
	StatusDetail    string `json:"status_detail,omitempty"`
	Instances       int    `json:"instances" jsonschema:"number of instances Oakestra knows about"`
	Running         int    `json:"running" jsonschema:"number of instances with status RUNNING"`
}

// Instance is one deployed replica of a service.
type Instance struct {
	Number          int    `json:"instance_number"`
	Status          string `json:"status"`
	StatusDetail    string `json:"status_detail,omitempty"`
	ClusterID       string `json:"cluster_id,omitempty"`
	ClusterName     string `json:"cluster_name,omitempty"`
	ClusterLocation string `json:"cluster_location,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	HostIP          string `json:"host_ip,omitempty"`
	HostPort        int    `json:"host_port,omitempty"`
	PublicIP        string `json:"public_ip,omitempty"`
	CPUPercent      string `json:"cpu_percent,omitempty"`
	MemoryPercent   string `json:"memory_percent,omitempty"`
}

// ServiceDetail is a service with its resource requirements and instances.
type ServiceDetail struct {
	ServiceSummary
	Image          string     `json:"image,omitempty" jsonschema:"container image or unikernel code reference"`
	Virtualization string     `json:"virtualization,omitempty"`
	VCPUs          int        `json:"vcpus" jsonschema:"requested CPU cores"`
	MemoryMB       int        `json:"memory_mb" jsonschema:"requested memory in MB"`
	VGPUs          int        `json:"vgpus,omitempty" jsonschema:"requested GPUs"`
	Port           string     `json:"port,omitempty" jsonschema:"port mapping as given in the SLA"`
	InstanceList   []Instance `json:"instance_list"`
}

const statusRunning = "RUNNING"

func summaryFrom(s oakapi.Service) ServiceSummary {
	sum := ServiceSummary{
		ID: s.MicroserviceID, Name: s.MicroserviceName, Namespace: s.MicroserviceNamespace,
		ApplicationID: s.ApplicationID, ApplicationName: s.GetApplicationName(),
		Status: s.Status, StatusDetail: s.StatusDetail, Instances: len(s.InstanceList),
	}
	for _, i := range s.InstanceList {
		if strings.EqualFold(i.Status, statusRunning) {
			sum.Running++
		}
	}
	return sum
}

type listApplicationsIn struct{}

type listApplicationsOut struct {
	Applications []Application `json:"applications"`
}

type getApplicationIn struct {
	IDOrName string `json:"id_or_name" jsonschema:"application ID or application name"`
}

type getApplicationOut struct {
	Application
	Services []ServiceSummary `json:"services"`
	// ServicesError is set when the application was found but its services
	// could not be listed, so the agent still gets the partial answer.
	ServicesError string `json:"services_error,omitempty"`
}

type listServicesIn struct {
	Application string `json:"application,omitempty" jsonschema:"optional application ID or name to restrict the list to"`
}

type listServicesOut struct {
	Services []ServiceSummary `json:"services"`
}

type getServiceIn struct {
	IDOrName string `json:"id_or_name" jsonschema:"service ID or service (microservice) name"`
}

func registerAppTools(r *registry) {
	addTool(r, ToolInfo{
		Name:  "list_applications",
		Title: "List applications",
		Description: "List the Oakestra applications visible to the caller (admins see all, others only their own) " +
			"with ID, name, namespace, description and the IDs of their services.",
		Kind: KindRead,
	}, r.listApplications)

	addTool(r, ToolInfo{
		Name:  "get_application",
		Title: "Get application",
		Description: "Get one application by ID or name, together with a status summary of each of its services " +
			"(status and how many instances are running). If several applications share the name, " +
			"the error lists their IDs; call again with an ID.",
		Kind: KindRead,
	}, r.getApplication)

	addTool(r, ToolInfo{
		Name:  "list_services",
		Title: "List services",
		Description: "List services (microservices) visible to the caller, optionally only those of one application " +
			"(by ID or name). Each entry has the service status and the number of total and running instances. " +
			"Use get_service for resource requirements and where each instance runs.",
		Kind: KindRead,
	}, r.listServices)

	addTool(r, ToolInfo{
		Name:  "get_service",
		Title: "Get service",
		Description: "Get one service by ID or name: its image, virtualization, requested CPU/memory/GPUs, status, " +
			"and every instance with its status, status detail, cluster and worker. " +
			"Use this to see where a service runs or why an instance is not running.",
		Kind: KindRead,
	}, r.getService)
}

func (r *registry) client() (*oakapi.Client, error) {
	return r.d.Oakestra.ClientFor(r.p)
}

func (r *registry) listApplications(ctx context.Context, _ listApplicationsIn) (listApplicationsOut, string, error) {
	c, err := r.client()
	if err != nil {
		return listApplicationsOut{}, "", err
	}
	apps, err := oakestra.ListApplications(ctx, c, r.p)
	if err != nil {
		return listApplicationsOut{}, "", err
	}
	out := listApplicationsOut{Applications: make([]Application, 0, len(apps))}
	parts := make([]string, 0, len(apps))
	for _, a := range apps {
		out.Applications = append(out.Applications, applicationFrom(a))
		parts = append(parts, fmt.Sprintf("%s (%s, %d services)", a.ApplicationName, a.ApplicationID, len(a.Microservices)))
	}
	return out, fmt.Sprintf("%d applications: %s", len(apps), joinCapped(parts, 30)), nil
}

func (r *registry) getApplication(ctx context.Context, in getApplicationIn) (getApplicationOut, string, error) {
	if strings.TrimSpace(in.IDOrName) == "" {
		return getApplicationOut{}, "", errors.New("id_or_name is required")
	}
	c, err := r.client()
	if err != nil {
		return getApplicationOut{}, "", err
	}
	app, err := oakestra.ResolveApplication(ctx, c, r.p, in.IDOrName)
	if err != nil {
		return getApplicationOut{}, "", err
	}
	out := getApplicationOut{Application: applicationFrom(*app), Services: []ServiceSummary{}}
	svcs, _, err := c.Services.List(ctx, &oakapi.ServiceListOptions{ApplicationID: app.ApplicationID})
	if err != nil {
		out.ServicesError = toolError(err).Error()
	}
	parts := make([]string, 0, len(svcs))
	for _, s := range svcs {
		sum := summaryFrom(s)
		out.Services = append(out.Services, sum)
		parts = append(parts, fmt.Sprintf("%s [%s, %d/%d running]", sum.Name, cmp.Or(sum.Status, "-"), sum.Running, sum.Instances))
	}
	summary := fmt.Sprintf("Application %s (%s), namespace %s, %d services: %s",
		app.ApplicationName, app.ApplicationID, app.ApplicationNamespace, len(out.Services), joinCapped(parts, 30))
	if out.ServicesError != "" {
		summary += ". Services could not be listed: " + out.ServicesError
	}
	return out, summary, nil
}

func (r *registry) listServices(ctx context.Context, in listServicesIn) (listServicesOut, string, error) {
	c, err := r.client()
	if err != nil {
		return listServicesOut{}, "", err
	}
	var opts *oakapi.ServiceListOptions
	scope := ""
	if ref := strings.TrimSpace(in.Application); ref != "" {
		app, err := oakestra.ResolveApplication(ctx, c, r.p, ref)
		if err != nil {
			return listServicesOut{}, "", err
		}
		opts = &oakapi.ServiceListOptions{ApplicationID: app.ApplicationID}
		scope = " in application " + app.ApplicationName
	}
	svcs, _, err := c.Services.List(ctx, opts)
	if err != nil {
		return listServicesOut{}, "", err
	}
	out := listServicesOut{Services: make([]ServiceSummary, 0, len(svcs))}
	parts := make([]string, 0, len(svcs))
	for _, s := range svcs {
		sum := summaryFrom(s)
		out.Services = append(out.Services, sum)
		parts = append(parts, fmt.Sprintf("%s (%s) [%s, %d/%d running]", sum.Name, sum.ID, cmp.Or(sum.Status, "-"), sum.Running, sum.Instances))
	}
	return out, fmt.Sprintf("%d services%s: %s", len(svcs), scope, joinCapped(parts, 30)), nil
}

func (r *registry) getService(ctx context.Context, in getServiceIn) (ServiceDetail, string, error) {
	if strings.TrimSpace(in.IDOrName) == "" {
		return ServiceDetail{}, "", errors.New("id_or_name is required")
	}
	c, err := r.client()
	if err != nil {
		return ServiceDetail{}, "", err
	}
	s, _, err := c.Services.ResolveByNameOrID(ctx, in.IDOrName)
	if err != nil {
		return ServiceDetail{}, "", err
	}
	// A name match comes from the list endpoint, which leaves out the
	// deployment fields; fetch the full document by ID.
	if s.Code == "" && s.MicroserviceID != "" && s.MicroserviceID != in.IDOrName {
		if full, _, err := c.Services.Get(ctx, s.MicroserviceID); err == nil {
			s = full
		}
	}

	clusterNames := r.clusterNames(ctx)
	d := ServiceDetail{
		ServiceSummary: summaryFrom(*s),
		Image:          s.Code, Virtualization: s.Virtualization,
		VCPUs: s.VCPUs, MemoryMB: s.Memory, VGPUs: s.VGPUs, Port: s.Port,
		InstanceList: make([]Instance, 0, len(s.InstanceList)),
	}
	parts := make([]string, 0, len(s.InstanceList))
	for _, i := range s.InstanceList {
		inst := Instance{
			Number: i.InstanceNumber, Status: i.Status, StatusDetail: i.StatusDetail,
			ClusterID: i.ClusterID, ClusterName: clusterNames[i.ClusterID], ClusterLocation: i.ClusterLocation,
			WorkerID: i.WorkerID, HostIP: i.HostIP, HostPort: i.HostPort, PublicIP: i.PublicIP,
			CPUPercent: i.CPUPercent, MemoryPercent: i.MemoryPercent,
		}
		d.InstanceList = append(d.InstanceList, inst)
		where := cmp.Or(inst.ClusterName, inst.ClusterID)
		if inst.WorkerID != "" {
			where += "/" + inst.WorkerID
		}
		desc := fmt.Sprintf("#%d %s", inst.Number, cmp.Or(inst.Status, "-"))
		if where != "" {
			desc += " on " + where
		}
		if inst.StatusDetail != "" {
			desc += " (" + inst.StatusDetail + ")"
		}
		parts = append(parts, desc)
	}
	summary := fmt.Sprintf("Service %s (%s) of application %s: status %s, %d instances (%d running): %s",
		d.Name, d.ID, d.ApplicationName, cmp.Or(d.Status, "-"), d.Instances, d.Running, joinCapped(parts, 30))
	return d, summary, nil
}

// clusterNames maps cluster IDs to names for nicer output. It is best
// effort: the IDs alone are still correct when the resource abstractor
// cannot be reached.
func (r *registry) clusterNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if r.d.Candidates == nil {
		return names
	}
	cs, err := r.d.Candidates.Root(ctx)
	if err != nil {
		return names
	}
	for _, c := range cs {
		names[c.ID] = c.Name
	}
	return names
}
