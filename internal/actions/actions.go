// Package actions plans and executes changes to Oakestra in two steps.
// Prepare computes a plan without side effects and hands out a signed,
// short-lived confirmation token; Confirm checks the token, recomputes the
// plan from fresh state, refuses if anything changed, and only then runs
// the steps with the caller's own Oakestra client.
package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/explain"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
)

// Kind names what an action does.
type Kind string

const (
	KindDeployApplication Kind = "deploy_application"
	KindScaleService      Kind = "scale_service"
	KindUndeployService   Kind = "undeploy_service"
	KindDeleteApplication Kind = "delete_application"
)

// Step is one Oakestra call of a plan.
type Step struct {
	Op     string `json:"op" jsonschema:"create_application, deploy_instance, undeploy_instance or delete_application"`
	Target string `json:"target" jsonschema:"what the step acts on, e.g. an application name or service ID with instance number"`
	Detail string `json:"detail,omitempty"`
}

// DryRun is the placement prediction for one microservice of a plan.
type DryRun struct {
	Application  string `json:"application,omitempty"`
	Microservice string `json:"microservice,omitempty"`
	explain.Result
}

// Plan is what an action would do, computed from the state Oakestra
// reported when it was prepared.
type Plan struct {
	Kind     Kind     `json:"kind"`
	Summary  string   `json:"summary" jsonschema:"one paragraph for the human who has to approve the action"`
	Steps    []Step   `json:"steps" jsonschema:"executed in this order; execution stops at the first failing step"`
	Warnings []string `json:"warnings,omitempty" jsonschema:"things the human should know before approving; they do not block the action"`
	DryRun   []DryRun `json:"dry_run,omitempty" jsonschema:"placement prediction per microservice for steps that deploy instances"`
	Hash     string   `json:"plan_hash" jsonschema:"fingerprint of the steps and the Oakestra state they depend on"`
}

// Prepared is a stored plan together with the token that executes it.
type Prepared struct {
	ActionID          string    `json:"action_id"`
	ConfirmationToken string    `json:"confirmation_token" jsonschema:"pass this to confirm_action after the human approved the plan"`
	ExpiresAt         time.Time `json:"expires_at"`
	Plan
}

// Step outcomes in a Result.
const (
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// StepResult is how one step went.
type StepResult struct {
	Step
	Status  string `json:"status" jsonschema:"done, failed or skipped"`
	Message string `json:"message,omitempty"`
}

// Result is the outcome of a confirmed action.
type Result struct {
	ActionID string       `json:"action_id"`
	Kind     Kind         `json:"kind"`
	Status   string       `json:"status" jsonschema:"done when every step succeeded, failed otherwise"`
	Steps    []StepResult `json:"steps"`
	Message  string       `json:"message"`
}

var (
	ErrNotFound     = errors.New("pending action not found")
	ErrInvalidToken = errors.New("the confirmation token is malformed or was altered; pass the token exactly as prepare returned it, or prepare the action again")
	ErrUnknown      = errors.New("no pending action matches this confirmation token; prepare the action again")
	ErrWrongUser    = errors.New("this confirmation token was issued to another user; only the user who prepared an action can confirm it")
	ErrExpired      = errors.New("the confirmation token has expired; prepare the action again and ask the human to approve the new plan")
	ErrConsumed     = errors.New("this action was already confirmed; a token runs at most once. Prepare a new action if more changes are needed")
	ErrPlanChanged  = errors.New("the plan changed since it was prepared because Oakestra's state changed (for example instances were added or removed meanwhile); nothing was executed. Prepare the action again and show the new plan to the human")
)

// Pending is a prepared action as stored until it is confirmed or expires.
type Pending struct {
	ID           string
	User         string
	Organization string
	Kind         Kind
	ParamsJSON   string
	PlanHash     string
	Created      time.Time
	Expires      time.Time
	ConsumedAt   *time.Time
}

// Store persists pending actions. ConsumePending must be atomic: of any
// number of concurrent calls for the same ID, exactly one may return true.
// Load returns ErrNotFound for unknown IDs.
type Store interface {
	SavePending(ctx context.Context, a Pending) error
	LoadPending(ctx context.Context, id string) (Pending, error)
	ConsumePending(ctx context.Context, id string, at time.Time) (bool, error)
}

// Engine prepares and confirms actions.
type Engine struct {
	store     Store
	secret    []byte
	ttl       time.Duration
	placement explain.CandidateSource
	now       func() time.Time
}

// NewEngine returns an engine. placement may be nil, in which case plans
// carry no dry run.
func NewEngine(store Store, secret []byte, ttl time.Duration, placement explain.CandidateSource) *Engine {
	return &Engine{store: store, secret: secret, ttl: ttl, placement: placement, now: time.Now}
}

// TTL is how long a confirmation token stays valid.
func (e *Engine) TTL() time.Duration { return e.ttl }

// AllowedRoles may use write actions. Oakestra checks ownership on every
// call on top of this.
var AllowedRoles = []string{auth.RoleAdmin, auth.RoleOrganizationAdmin, auth.RoleApplicationProvider}

// ExecutionError reports a confirmed action that stopped at a failing
// step. Steps before it stay in effect; nothing is rolled back.
type ExecutionError struct{ Result Result }

func (e *ExecutionError) Error() string { return RenderResult(e.Result) }

// step is a plan step plus the call that performs it.
type step struct {
	Step
	run func(ctx context.Context, c *oakapi.Client, st *execState) (string, error)
}

// execState carries results from earlier steps to later ones, e.g. the
// service IDs Oakestra assigns when an application is created.
type execState struct {
	serviceIDs map[string]string
}

// planned is a plan before it is stored. params and state go into the hash
// together with the steps' op and target. Detail is left out on purpose:
// it carries status text that changes on its own (an instance moving from
// CLUSTER_SCHEDULED to RUNNING) without changing what the plan does.
type planned struct {
	kind     Kind
	summary  string
	steps    []step
	warnings []string
	dryRun   []DryRun
	params   any
	state    any
}

func (p planned) hash() (string, error) {
	type opTarget struct {
		Op     string `json:"op"`
		Target string `json:"target"`
	}
	ots := make([]opTarget, 0, len(p.steps))
	for _, s := range p.steps {
		ots = append(ots, opTarget{s.Op, s.Target})
	}
	b, err := json.Marshal(struct {
		Kind   Kind       `json:"kind"`
		Params any        `json:"params"`
		State  any        `json:"state"`
		Steps  []opTarget `json:"steps"`
	}{p.kind, p.params, p.state, ots})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (p planned) public(hash string) Plan {
	steps := make([]Step, 0, len(p.steps))
	for _, s := range p.steps {
		steps = append(steps, s.Step)
	}
	return Plan{Kind: p.kind, Summary: p.summary, Steps: steps, Warnings: p.warnings, DryRun: p.dryRun, Hash: hash}
}

// save stores the plan as a pending action and signs a token for it.
func (e *Engine) save(ctx context.Context, p auth.Principal, pl planned) (Prepared, error) {
	if len(pl.steps) == 0 {
		return Prepared{}, errors.New("nothing to do: " + pl.summary)
	}
	hash, err := pl.hash()
	if err != nil {
		return Prepared{}, err
	}
	params, err := json.Marshal(pl.params)
	if err != nil {
		return Prepared{}, err
	}
	id, err := newActionID()
	if err != nil {
		return Prepared{}, err
	}
	now := e.now().UTC().Truncate(time.Second)
	exp := now.Add(e.ttl)
	err = e.store.SavePending(ctx, Pending{
		ID: id, User: p.User, Organization: p.Organization, Kind: pl.kind,
		ParamsJSON: string(params), PlanHash: hash, Created: now, Expires: exp,
	})
	if err != nil {
		return Prepared{}, fmt.Errorf("storing the pending action failed: %w", err)
	}
	return Prepared{
		ActionID:          id,
		ConfirmationToken: makeToken(e.secret, id, p.User, hash, exp.Unix()),
		ExpiresAt:         exp,
		Plan:              pl.public(hash),
	}, nil
}

// Confirm executes the action behind token as p, with c acting as p. The
// returned Result carries the action ID as soon as the token's signature
// checks out, also when an error follows, so callers can link log entries.
func (e *Engine) Confirm(ctx context.Context, p auth.Principal, c *oakapi.Client, token string) (Result, error) {
	t, err := parseToken(token)
	if err != nil {
		return Result{}, err
	}
	a, err := e.store.LoadPending(ctx, t.actionID)
	if errors.Is(err, ErrNotFound) {
		return Result{}, ErrUnknown
	}
	if err != nil {
		return Result{}, err
	}
	if !validMAC(e.secret, t, p.User, a.PlanHash) {
		if a.User != p.User && validMAC(e.secret, t, a.User, a.PlanHash) {
			return Result{ActionID: a.ID}, ErrWrongUser
		}
		return Result{}, ErrInvalidToken
	}
	res := Result{ActionID: a.ID, Kind: a.Kind}
	// The MAC already binds the user; this guards against a store row that
	// was edited by hand.
	if a.User != p.User {
		return res, ErrWrongUser
	}
	if t.expires != a.Expires.Unix() {
		return res, ErrInvalidToken
	}
	if !e.now().Before(a.Expires) {
		return res, ErrExpired
	}
	if a.ConsumedAt != nil {
		return res, ErrConsumed
	}

	// Recompute before consuming, so a transient Oakestra error does not
	// burn a token the human already approved.
	pl, err := e.replan(ctx, p, c, a)
	if err != nil {
		return res, fmt.Errorf("could not re-check the plan against Oakestra's current state, so nothing was executed: %s", describeErr(err))
	}
	hash, err := pl.hash()
	if err != nil {
		return res, err
	}
	if hash != a.PlanHash {
		return res, ErrPlanChanged
	}

	// The re-check above makes several Oakestra calls, so the token can run
	// out while it is in flight. An approval is only valid until its expiry.
	if !e.now().Before(a.Expires) {
		return res, ErrExpired
	}
	ok, err := e.store.ConsumePending(ctx, a.ID, e.now())
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrConsumed
	}

	return e.execute(ctx, c, res, pl)
}

func (e *Engine) replan(ctx context.Context, p auth.Principal, c *oakapi.Client, a Pending) (planned, error) {
	switch a.Kind {
	case KindDeployApplication:
		return replanWith(a, func(in deployParams) (planned, error) { return e.planDeploy(ctx, p, c, in, false) })
	case KindScaleService:
		return replanWith(a, func(in scaleParams) (planned, error) {
			s, err := loadService(ctx, c, in.ServiceID)
			if err != nil {
				return planned{}, err
			}
			return e.planScale(ctx, s, in, false)
		})
	case KindUndeployService:
		return replanWith(a, func(in undeployParams) (planned, error) {
			s, err := loadService(ctx, c, in.ServiceID)
			if err != nil {
				return planned{}, err
			}
			return e.planUndeploy(s, in)
		})
	case KindDeleteApplication:
		return replanWith(a, func(in deleteParams) (planned, error) { return e.planDelete(ctx, c, in, nil) })
	}
	return planned{}, fmt.Errorf("unknown action kind %q", a.Kind)
}

func replanWith[T any](a Pending, plan func(T) (planned, error)) (planned, error) {
	var in T
	if err := json.Unmarshal([]byte(a.ParamsJSON), &in); err != nil {
		return planned{}, err
	}
	return plan(in)
}

// execute runs the steps in order and stops at the first error. The
// context is detached from the request: once a step has been sent, a
// client disconnect should not leave the action half done for no reason.
func (e *Engine) execute(ctx context.Context, c *oakapi.Client, res Result, pl planned) (Result, error) {
	ctx = context.WithoutCancel(ctx)
	st := &execState{serviceIDs: map[string]string{}}
	failed := false
	for _, s := range pl.steps {
		sr := StepResult{Step: s.Step, Status: StepSkipped}
		if !failed {
			msg, err := s.run(ctx, c, st)
			if err != nil {
				failed = true
				sr.Status, sr.Message = StepFailed, describeErr(err)
			} else {
				sr.Status, sr.Message = StepDone, msg
			}
		}
		res.Steps = append(res.Steps, sr)
	}
	res.Status = StepDone
	if failed {
		res.Status = StepFailed
	}
	res.Message = resultMessage(res)
	if failed {
		return res, &ExecutionError{Result: res}
	}
	return res, nil
}

func resultMessage(r Result) string {
	var done, failedAt, skipped int
	for i, s := range r.Steps {
		switch s.Status {
		case StepDone:
			done++
		case StepFailed:
			failedAt = i + 1
		case StepSkipped:
			skipped++
		}
	}
	if failedAt == 0 {
		msg := fmt.Sprintf("Action %s done: all %d steps succeeded.", r.ActionID, done)
		if slices.ContainsFunc(r.Steps, func(s StepResult) bool { return s.Op == opDeployInstance }) {
			msg += " Oakestra schedules new instances asynchronously; follow them with get_service or explain_placement."
		}
		return msg
	}
	return fmt.Sprintf("Action %s stopped at step %d of %d. %d steps before it were done and stay in effect "+
		"(nothing is rolled back); %d later steps were skipped. Check the current state with list_services or "+
		"get_service before preparing a new action.", r.ActionID, failedAt, len(r.Steps), done, skipped)
}

// RenderResult formats a result as text for humans and agents.
func RenderResult(r Result) string {
	var b strings.Builder
	b.WriteString(r.Message)
	for i, s := range r.Steps {
		fmt.Fprintf(&b, "\n%d. [%s] %s %s", i+1, s.Status, s.Op, s.Target)
		if s.Message != "" {
			b.WriteString(": " + s.Message)
		}
	}
	return b.String()
}

// describeErr keeps Oakestra's answer but drops the URL oakestra-go puts
// into its messages, which only adds noise for the agent.
func describeErr(err error) string {
	var (
		respErr *oakapi.ErrorResponse
		authErr *oakapi.AuthError
		connErr *oakapi.ConnectionError
	)
	switch {
	case errors.As(err, &respErr):
		return oakestra.DescribeResponse(respErr)
	case errors.As(err, &authErr):
		return fmt.Sprintf("Oakestra refused the request (HTTP %d)", authErr.StatusCode)
	case errors.As(err, &connErr):
		return fmt.Sprintf("the Oakestra system manager was not reachable (%v)", connErr.Err)
	}
	return err.Error()
}
