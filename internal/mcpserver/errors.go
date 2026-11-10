package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	oakapi "github.com/oakestra/oakestra-cli/oakestra-go"

	"github.com/robertjndw/oakestra-mcp/internal/candidates"
)

// toolError rewrites errors into messages that tell an agent what went
// wrong and what to do next. oakestra-go's messages are written for CLI
// users and include URLs and raw bodies that only add noise.
func toolError(err error) error {
	var (
		denied   *DeniedError
		notFound *oakapi.NotFoundError
		multi    *oakapi.MultipleMatchesError
		authErr  *oakapi.AuthError
		connErr  *oakapi.ConnectionError
		respErr  *oakapi.ErrorResponse
	)
	switch {
	case errors.As(err, &denied):
		return err
	case errors.As(err, &notFound):
		return fmt.Errorf("no %s found with ID or name %q. Call list_%ss to see the ones you can access.",
			notFound.Kind, notFound.Query, notFound.Kind)
	case errors.As(err, &multi):
		ids := make([]string, 0, len(multi.Matches))
		for _, m := range multi.Matches {
			if m.Detail != "" {
				ids = append(ids, fmt.Sprintf("%s (%s)", m.ID, m.Detail))
			} else {
				ids = append(ids, m.ID)
			}
		}
		return fmt.Errorf("%d %ss are named %q. Call again with one of these IDs instead: %s",
			len(multi.Matches), multi.Kind, multi.Query, strings.Join(ids, ", "))
	case errors.As(err, &authErr):
		if authErr.StatusCode == http.StatusForbidden {
			return Deny("Oakestra refused the request (HTTP 403): this user lacks the permission or does not own the resource.")
		}
		return fmt.Errorf("Oakestra rejected the token (HTTP %d). It may have expired; reconnect with a fresh Oakestra refresh token.", authErr.StatusCode)
	case errors.As(err, &connErr):
		return fmt.Errorf("the Oakestra system manager is not reachable right now (%v). Retry later; this is not a problem with the request.", connErr.Err)
	case errors.As(err, &respErr):
		return fmt.Errorf("Oakestra returned HTTP %d: %s", respErr.StatusCode, truncate(strings.TrimSpace(respErr.Body), 300))
	case errors.Is(err, candidates.ErrUnavailable):
		return fmt.Errorf("resource data is unavailable: %v. Retry later; application and service tools may still work.", err)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the request to Oakestra timed out. Retry later.")
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
