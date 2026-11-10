package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
)

type prepareDeployIn struct {
	SLA                 any  `json:"sla" jsonschema:"a full Oakestra application SLA (sla_version, customerID, applications[] with application_name, application_namespace and microservices[]) or a single application object; sla_version defaults to v2.0 and customerID to the caller"`
	InstancesPerService *int `json:"instances_per_service,omitempty" jsonschema:"instances to request for every microservice after creating the application, 0 to 20, default 1"`
}

type prepareScaleIn struct {
	Service  string `json:"service" jsonschema:"service ID or service (microservice) name"`
	Replicas int    `json:"replicas" jsonschema:"desired number of instances, 0 to 20; counted from the instances Oakestra lists for the service"`
}

type prepareUndeployIn struct {
	Service        string `json:"service" jsonschema:"service ID or service (microservice) name"`
	InstanceNumber *int   `json:"instance_number,omitempty" jsonschema:"undeploy only this instance (see get_service); omit to undeploy every instance of the service"`
}

type prepareDeleteIn struct {
	Application string `json:"application" jsonschema:"application ID or application name"`
}

type confirmIn struct {
	ConfirmationToken string `json:"confirmation_token" jsonschema:"the confirmation_token a prepare_* call returned, unchanged"`
}

func registerWriteTools(r *registry) {
	ttl := r.d.Actions.TTL()
	prepareDoc := fmt.Sprintf(" This call has no side effects: it only computes a plan from Oakestra's current state "+
		"and returns it with a confirmation_token. Show the summary, steps and warnings to the human and call "+
		"confirm_action only after the human explicitly approves; never confirm on your own. The token is valid for %s "+
		"and only for the user who prepared it. If Oakestra's state changes before confirmation in a way that changes "+
		"the plan, confirm_action refuses and you have to prepare again. Requires the Admin, Organization_Admin or "+
		"Application_Provider role.", formatTTL(ttl))

	addTool(r, ToolInfo{
		Name:  "prepare_deploy_application",
		Title: "Prepare: deploy application",
		Description: "Plan deploying a new application from an SLA: create the application with its microservices, " +
			"then request instances_per_service instances (default 1) of each microservice. Validates the SLA and " +
			"includes a placement dry run per microservice; a microservice that would not be placed right now is a " +
			"warning, not a refusal." + prepareDoc,
		Kind:     KindWrite,
		planOnly: true,
	}, r.prepareDeploy)

	addTool(r, ToolInfo{
		Name:  "prepare_scale_service",
		Title: "Prepare: scale service",
		Description: "Plan bringing a service to a number of instances. Scaling up requests new instances (with a " +
			"placement dry run); scaling down undeploys the highest-numbered instances. Every instance Oakestra lists " +
			"counts, whatever its status." + prepareDoc,
		Kind:     KindWrite,
		planOnly: true,
	}, r.prepareScale)

	addTool(r, ToolInfo{
		Name:  "prepare_undeploy_service",
		Title: "Prepare: undeploy service",
		Description: "Plan undeploying one instance of a service (instance_number) or all of its instances. The " +
			"service stays registered and can be scaled up again; use prepare_delete_application to remove it." + prepareDoc,
		Kind:     KindWrite,
		planOnly: true,
	}, r.prepareUndeploy)

	addTool(r, ToolInfo{
		Name:  "prepare_delete_application",
		Title: "Prepare: delete application",
		Description: "Plan deleting an application. The plan lists every service and instance that goes away with " +
			"it; this cannot be undone." + prepareDoc,
		Kind:     KindWrite,
		planOnly: true,
	}, r.prepareDelete)

	addTool(r, ToolInfo{
		Name:  "confirm_action",
		Title: "Confirm action",
		Description: "Execute an action prepared by a prepare_* tool. Call this only after the human has seen the " +
			"plan and explicitly approved it. It re-checks Oakestra's state and refuses if the plan changed, if the " +
			fmt.Sprintf("token expired (tokens last %s) or was already used, or if another user prepared it. ", formatTTL(ttl)) +
			"Steps run in order and stop at the first failure; the result lists each step as done, failed or " +
			"skipped, and completed steps are not rolled back. New instances are scheduled asynchronously, so check " +
			"them afterwards with get_service or explain_placement.",
		Kind: KindWrite,
	}, r.confirmAction)
}

// checkWriter refuses principals without a write role. The refusal is a
// DeniedError so the activity log records it as denied, not failed.
func (r *registry) checkWriter() error {
	if !r.p.HasRole(actions.AllowedRoles...) {
		return Deny(fmt.Sprintf("write actions need one of the roles %s; user %s has %s",
			strings.Join(actions.AllowedRoles, ", "), r.p.User, orNone(r.p.Roles)))
	}
	return nil
}

// writeClient returns a client for the caller once it passed the role check.
func (r *registry) writeClient() (*oakapi.Client, error) {
	if err := r.checkWriter(); err != nil {
		return nil, err
	}
	return r.client()
}

func (r *registry) prepare(ctx context.Context, do func(c *oakapi.Client) (actions.Prepared, error)) (actions.Prepared, string, error) {
	c, err := r.writeClient()
	if err != nil {
		return actions.Prepared{}, "", err
	}
	out, err := do(c)
	if err != nil {
		return actions.Prepared{}, "", err
	}
	SetActionID(ctx, out.ActionID)
	return out, preparedText(out), nil
}

func (r *registry) prepareDeploy(ctx context.Context, in prepareDeployIn) (actions.Prepared, string, error) {
	return r.prepare(ctx, func(c *oakapi.Client) (actions.Prepared, error) {
		raw, err := slaJSON(in.SLA)
		if err != nil {
			return actions.Prepared{}, err
		}
		return r.d.Actions.PrepareDeploy(ctx, r.p, c, raw, in.InstancesPerService)
	})
}

func (r *registry) prepareScale(ctx context.Context, in prepareScaleIn) (actions.Prepared, string, error) {
	return r.prepare(ctx, func(c *oakapi.Client) (actions.Prepared, error) {
		return r.d.Actions.PrepareScale(ctx, r.p, c, in.Service, in.Replicas)
	})
}

func (r *registry) prepareUndeploy(ctx context.Context, in prepareUndeployIn) (actions.Prepared, string, error) {
	return r.prepare(ctx, func(c *oakapi.Client) (actions.Prepared, error) {
		return r.d.Actions.PrepareUndeploy(ctx, r.p, c, in.Service, in.InstanceNumber)
	})
}

func (r *registry) prepareDelete(ctx context.Context, in prepareDeleteIn) (actions.Prepared, string, error) {
	return r.prepare(ctx, func(c *oakapi.Client) (actions.Prepared, error) {
		return r.d.Actions.PrepareDelete(ctx, r.p, c, in.Application)
	})
}

func (r *registry) confirmAction(ctx context.Context, in confirmIn) (actions.Result, string, error) {
	if err := r.checkWriter(); err != nil {
		return actions.Result{}, "", err
	}
	if strings.TrimSpace(in.ConfirmationToken) == "" {
		return actions.Result{}, "", errors.New("confirmation_token is required: pass the token a prepare_* call returned")
	}
	c, err := r.client()
	if err != nil {
		return actions.Result{}, "", err
	}
	res, err := r.d.Actions.Confirm(ctx, r.p, c, in.ConfirmationToken)
	if res.ActionID != "" {
		SetActionID(ctx, res.ActionID)
	}
	if err != nil {
		return actions.Result{}, "", err
	}
	return res, actions.RenderResult(res), nil
}

func preparedText(p actions.Prepared) string {
	var b strings.Builder
	b.WriteString(p.Summary)
	b.WriteString("\nSteps:")
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "\n%d. %s %s", i+1, s.Op, s.Target)
		if s.Detail != "" {
			b.WriteString(" (" + s.Detail + ")")
		}
	}
	for _, w := range p.Warnings {
		b.WriteString("\nWarning: " + w)
	}
	fmt.Fprintf(&b, "\nNothing has changed yet. Show this plan to the human. Only after explicit approval, call "+
		"confirm_action with confirmation_token from this result before %s (action %s).",
		p.ExpiresAt.UTC().Format(time.RFC3339), p.ActionID)
	return b.String()
}

func formatTTL(d time.Duration) string {
	if d%time.Minute == 0 {
		return actions.Plural(int(d/time.Minute), "minute")
	}
	return d.String()
}

func orNone(roles []string) string {
	if len(roles) == 0 {
		return "no roles"
	}
	return strings.Join(roles, ", ")
}
