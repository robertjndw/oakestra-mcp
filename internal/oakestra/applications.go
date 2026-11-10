package oakestra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/auth"
)

// ListApplications returns the applications p may see. GET /api/applications
// is admin-only in the system manager, so everyone else has to ask for
// GET /api/applications/<userid> with their own user document ID (the
// endpoint answers 401 for any other ID). Nothing is cached: the ID is one
// cheap request away and a stale list would hide fresh applications.
func ListApplications(ctx context.Context, c *oakapi.Client, p auth.Principal) ([]oakapi.Application, error) {
	if p.IsAdmin() {
		apps, _, err := c.Applications.List(ctx)
		return apps, err
	}
	return ListOwnApplications(ctx, c, p)
}

// ListOwnApplications returns only the applications p owns, also for
// admins. Name and namespace are unique per owner in Oakestra, so clash
// checks before a create must not look at other users' applications.
func ListOwnApplications(ctx context.Context, c *oakapi.Client, p auth.Principal) ([]oakapi.Application, error) {
	id, err := userID(ctx, c, p.User)
	if err != nil {
		return nil, err
	}
	req, err := c.NewRequest(ctx, http.MethodGet, "api/applications/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var apps []oakapi.Application
	if _, err := c.Do(ctx, req, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// ResolveApplication finds an application by ID or, failing that, by exact
// name. It mirrors oakestra-go's ResolveByNameOrID (same error types, so
// callers' error mapping keeps working) but lists through ListApplications
// so the name lookup also works for non-admins.
func ResolveApplication(ctx context.Context, c *oakapi.Client, p auth.Principal, idOrName string) (*oakapi.Application, error) {
	if app, _, err := c.Applications.Get(ctx, idOrName); err == nil {
		return app, nil
	}
	all, err := ListApplications(ctx, c, p)
	if err != nil {
		return nil, err
	}
	var matches []oakapi.Application
	for _, a := range all {
		if a.ApplicationName == idOrName {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return nil, &oakapi.NotFoundError{Kind: "application", Query: idOrName}
	case 1:
		return &matches[0], nil
	}
	ms := make([]oakapi.Match, 0, len(matches))
	for _, a := range matches {
		ms = append(ms, oakapi.Match{ID: a.ApplicationID, Name: a.ApplicationName, Detail: a.ApplicationNamespace})
	}
	return nil, &oakapi.MultipleMatchesError{Kind: "application", Query: idOrName, Matches: ms}
}

// userID reads the caller's own user document. Depending on how the system
// manager serializes the Mongo ObjectId, _id is either a plain string or an
// extended-JSON {"$oid": "..."} object.
func userID(ctx context.Context, c *oakapi.Client, username string) (string, error) {
	raw, err := GetRaw(ctx, c, "api/user/"+url.PathEscape(username))
	if err != nil {
		return "", err
	}
	var doc struct {
		ID json.RawMessage `json:"_id"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("decoding user %q: %w", username, err)
	}
	var s string
	if json.Unmarshal(doc.ID, &s) == nil && s != "" {
		return s, nil
	}
	var oid struct {
		OID string `json:"$oid"`
	}
	if json.Unmarshal(doc.ID, &oid) == nil && oid.OID != "" {
		return oid.OID, nil
	}
	return "", errors.New("the system manager returned no _id for the user " + username)
}
