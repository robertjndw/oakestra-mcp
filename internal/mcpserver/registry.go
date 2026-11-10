package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
)

// Kind says whether a tool changes anything in Oakestra.
type Kind string

const (
	KindRead  Kind = "read"
	KindWrite Kind = "write"
)

// ToolInfo is the catalog entry for a tool, as /info reports it.
type ToolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Kind        Kind   `json:"kind"`
	// planOnly marks write tools that only plan (prepare_*). They stay
	// "write" in the catalog because they belong to the write workflow and
	// are gated with it, but they are annotated read-only so clients can
	// run them without a permission prompt and save the prompt for
	// confirm_action, the call that changes Oakestra.
	planOnly bool
}

// Outcome classifies a finished tool call.
type Outcome string

const (
	OutcomeOK     Outcome = "ok"
	OutcomeError  Outcome = "error"
	OutcomeDenied Outcome = "denied"
)

// Call describes one finished tool invocation. Args is the decoded input
// struct of the tool, unredacted; the recorder is responsible for removing
// secrets before storing it.
type Call struct {
	Tool       string
	Kind       Kind
	Args       any
	Outcome    Outcome
	Err        string
	Duration   time.Duration
	ClientName string
	// ActionID links prepare_* and confirm_action calls; set by the tool
	// through SetActionID.
	ActionID string
}

// Recorder receives every tool call. The context carries the principal
// (auth.FromContext) and is detached from the request's cancellation, so a
// recorder may write after the HTTP response has gone out.
type Recorder interface {
	Record(ctx context.Context, c Call)
}

// DeniedError is a refusal on policy grounds (missing role, writes
// disabled). It is recorded with OutcomeDenied instead of OutcomeError.
type DeniedError struct{ Reason string }

func (e *DeniedError) Error() string { return "denied: " + e.Reason }

// Deny returns a DeniedError.
func Deny(reason string) error { return &DeniedError{Reason: reason} }

type callSlot struct{ actionID string }

type callSlotKey struct{}

// SetActionID attaches an action ID to the activity record of the current
// tool call. It is a no-op outside a tool handler.
func SetActionID(ctx context.Context, id string) {
	if s, ok := ctx.Value(callSlotKey{}).(*callSlot); ok {
		s.actionID = id
	}
}

// toolFunc is what tool implementations provide: the structured result, a
// short human-readable summary of it, or an error. The principal is on ctx.
type toolFunc[In, Out any] func(ctx context.Context, in In) (Out, string, error)

// registry collects tools. With a nil server it only builds the catalog,
// so the catalog and the live tool set cannot drift apart.
type registry struct {
	d      Deps
	p      auth.Principal
	server *sdk.Server
	infos  []ToolInfo
}

func addTool[In, Out any](r *registry, info ToolInfo, h toolFunc[In, Out]) {
	r.infos = append(r.infos, info)
	if r.server == nil {
		return
	}
	if info.Kind == KindWrite && !r.d.EnableWrites {
		return
	}
	readOnly := info.Kind == KindRead || info.planOnly
	tool := &sdk.Tool{
		Name:        info.Name,
		Title:       info.Title,
		Description: info.Description,
		Annotations: &sdk.ToolAnnotations{
			Title:          info.Title,
			ReadOnlyHint:   readOnly,
			IdempotentHint: info.Kind == KindRead,
			OpenWorldHint:  new(false),
		},
	}
	if !readOnly {
		tool.Annotations.DestructiveHint = new(true)
	}
	sdk.AddTool(r.server, tool, wrap(r, info, h))
}

// wrap adapts a toolFunc to the SDK: it puts the principal on the context,
// turns errors into tool errors the agent can act on, and records the call.
func wrap[In, Out any](r *registry, info ToolInfo, h toolFunc[In, Out]) sdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		start := time.Now()
		slot := &callSlot{}
		ctx = auth.WithPrincipal(ctx, r.p)
		ctx = context.WithValue(ctx, callSlotKey{}, slot)

		out, summary, err := h(ctx, in)

		call := Call{
			Tool:       info.Name,
			Kind:       info.Kind,
			Args:       in,
			Outcome:    OutcomeOK,
			Duration:   time.Since(start),
			ClientName: clientName(req),
		}
		var zero Out
		if err != nil {
			err = toolError(err)
			call.Outcome = outcomeOf(err)
			call.Err = err.Error()
		}
		call.ActionID = slot.actionID
		if r.d.Recorder != nil {
			r.d.Recorder.Record(context.WithoutCancel(ctx), call)
		}
		if err != nil {
			// A plain error becomes an IsError result, not a protocol
			// error, so the agent sees the message and can correct itself.
			return nil, zero, err
		}

		res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: summary}}}
		// Clients that ignore structuredContent still get the full data.
		if b, mErr := json.Marshal(out); mErr == nil {
			res.Content = append(res.Content, &sdk.TextContent{Text: string(b)})
		}
		return res, out, nil
	}
}

func outcomeOf(err error) Outcome {
	var d *DeniedError
	if errors.As(err, &d) {
		return OutcomeDenied
	}
	return OutcomeError
}

// clientName identifies the agent. Stateless HTTP sessions do not keep the
// initialize handshake, so this only works for clients on the newer
// per-request protocol (or in-process transports); everyone else is
// identified by User-Agent.
func clientName(req *sdk.CallToolRequest) string {
	if req == nil {
		return ""
	}
	if ci := req.ClientInfo(); ci != nil && ci.Name != "" {
		return ci.Name
	}
	if req.Extra != nil && req.Extra.Header != nil {
		return req.Extra.Header.Get("User-Agent")
	}
	return ""
}
