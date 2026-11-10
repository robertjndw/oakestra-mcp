// Package mcpserver defines the MCP tools and serves them over stateless
// Streamable HTTP. A fresh server is built per request for the
// authenticated principal, so every tool call runs with that user's
// Oakestra token and Oakestra's own ownership checks apply.
package mcpserver

import (
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/explain"
)

// OakestraClients hands out system manager clients acting as a principal.
// *oakestra.Factory implements it.
type OakestraClients interface {
	ClientFor(p auth.Principal) (*oakapi.Client, error)
}

// Deps are the services behind the tools. A nil field means the tools that
// need it are not offered, which lets later phases add their dependencies
// without touching the wiring of earlier ones.
type Deps struct {
	Oakestra   OakestraClients
	Candidates explain.CandidateSource
	// Placement enables dry_run_placement, and explain_placement together
	// with Oakestra. *candidates.Client implements it.
	Placement explain.CandidateSource
	// Actions enables the prepare_* and confirm_action tools together
	// with Oakestra. Set it whether or not writes are enabled, so Catalog
	// lists them; EnableWrites alone decides whether they are served.
	Actions *actions.Engine
	// Recorder receives one Call per tool invocation (the activity log).
	Recorder Recorder
	// EnableWrites gates write tools on the MCP server. Catalog lists them
	// either way so the UI can show them as unavailable.
	EnableWrites bool
	Version      string
}

const instructions = `This server exposes an Oakestra edge orchestration deployment.
Oakestra has a root orchestrator, clusters (each with their own cluster orchestrator) and
worker nodes inside each cluster. Users own applications; an application contains services
(microservices), and each service runs as one or more numbered instances placed on a worker
in some cluster. All calls run with the caller's own Oakestra token, so non-admin users only
see their own applications and services. Clusters and nodes are visible to everyone.
Start with list_clusters or list_applications to discover names and IDs.`

const writeInstructions = `
Changes are two-step: a prepare_* tool returns a plan and a confirmation token without changing
anything; show the plan to the human and call confirm_action only after explicit approval.`

func (d Deps) version() string {
	if d.Version == "" {
		return "dev"
	}
	return d.Version
}

// hasWriteTools reports whether the write tools are registered. Whether
// they are also served is up to EnableWrites.
func (d Deps) hasWriteTools() bool { return d.Actions != nil && d.Oakestra != nil }

// NewServer builds the MCP server for one principal.
func NewServer(d Deps, p auth.Principal) *sdk.Server {
	return newServer(d, p, nil)
}

func newServer(d Deps, p auth.Principal, schemas *sdk.SchemaCache) *sdk.Server {
	instr := instructions
	if d.EnableWrites && d.hasWriteTools() {
		instr += writeInstructions
	}
	s := sdk.NewServer(&sdk.Implementation{Name: "oakestra-mcp", Title: "Oakestra", Version: d.version()},
		&sdk.ServerOptions{Instructions: instr, SchemaCache: schemas})
	register(&registry{d: d, p: p, server: s})
	return s
}

// Catalog lists every tool this build knows, for /info. Write tools are
// included even when writes are disabled.
func Catalog(d Deps) []ToolInfo {
	r := &registry{d: d}
	register(r)
	return r.infos
}

// Handler serves MCP at the mount point. It must sit behind the auth
// middleware; without a principal on the context the SDK answers 400.
func Handler(d Deps) http.Handler {
	// A server is built per request, so without a shared cache every request
	// would reflect and resolve the input and output schema of every tool.
	schemas := sdk.NewSchemaCache()
	return sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		p, ok := auth.FromContext(r.Context())
		if !ok {
			return nil
		}
		return newServer(d, p, schemas)
	}, &sdk.StreamableHTTPOptions{
		// Stateless JSON keeps every call a plain request/response, which
		// survives the dashboard's reverse proxy (60s timeout, no sticky
		// sessions) and needs no session store.
		Stateless:    true,
		JSONResponse: true,
	})
}

// register is the single place that decides which tools exist. Each group
// is gated on the deps it needs.
func register(r *registry) {
	if r.d.Candidates != nil {
		registerInfraTools(r)
	}
	if r.d.Oakestra != nil {
		registerAppTools(r)
	}
	if r.d.Placement != nil {
		registerPlacementTools(r)
	}
	if r.d.hasWriteTools() {
		registerWriteTools(r)
	}
}
