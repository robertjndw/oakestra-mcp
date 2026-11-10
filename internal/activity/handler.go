package activity

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/httpapi"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

type item struct {
	ID           int64           `json:"id"`
	TS           string          `json:"ts"`
	User         string          `json:"user"`
	Organization string          `json:"organization"`
	ClientName   string          `json:"client_name"`
	Tool         string          `json:"tool"`
	Kind         string          `json:"kind"`
	Args         json.RawMessage `json:"args"`
	Outcome      string          `json:"outcome"`
	Error        string          `json:"error"`
	DurationMS   int64           `json:"duration_ms"`
	ActionID     string          `json:"action_id"`
}

type page struct {
	Items      []item `json:"items"`
	NextBefore *int64 `json:"next_before"`
}

// Handler serves GET /activity. It must run behind the auth middleware.
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		p, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		q := r.URL.Query()
		f := Filter{Tool: q.Get("tool"), Outcome: q.Get("outcome"), Limit: defaultLimit}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				httpapi.WriteError(w, http.StatusBadRequest, "invalid limit")
				return
			}
			f.Limit = min(n, maxLimit)
		}
		if v := q.Get("before"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				httpapi.WriteError(w, http.StatusBadRequest, "invalid before")
				return
			}
			f.Before = n
		}
		if p.IsAdmin() {
			f.User = q.Get("user")
		} else {
			f.User = p.User
		}

		// One extra row tells us whether a further page exists.
		show := f.Limit
		f.Limit = show + 1
		rows, err := s.List(r.Context(), f)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "could not read activity")
			return
		}
		out := page{Items: []item{}}
		if len(rows) > show {
			rows = rows[:show]
			last := rows[len(rows)-1].ID
			out.NextBefore = &last
		}
		for _, e := range rows {
			args := json.RawMessage(e.ArgsJSON)
			if !json.Valid(args) {
				args = json.RawMessage("null")
			}
			out.Items = append(out.Items, item{
				ID: e.ID, TS: e.TS.UTC().Format(tsFormat), User: e.User, Organization: e.Organization,
				ClientName: e.ClientName, Tool: e.Tool, Kind: e.Kind, Args: args, Outcome: e.Outcome,
				Error: e.Error, DurationMS: e.DurationMS, ActionID: e.ActionID,
			})
		}
		httpapi.WriteJSON(w, http.StatusOK, out)
	})
}
