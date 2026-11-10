package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Middleware rejects requests without a valid Oakestra token and puts the
// Principal on the request context for everything downstream.
func (a *Authenticator) Middleware(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := BearerToken(r)
			if !ok {
				unauthorized(w, "missing bearer token: send an Oakestra access or refresh token as 'Authorization: Bearer <token>'")
				return
			}
			p, err := a.Authenticate(r.Context(), token)
			if err != nil {
				if errors.Is(err, ErrUnauthorized) {
					unauthorized(w, err.Error())
					return
				}
				// Only the error text is logged; it never contains the token.
				log.Warn("token verification failed", "err", err)
				// The caller is not authenticated yet, so the upstream error (with
				// internal host names and addresses) stays in the log.
				writeError(w, http.StatusBadGateway, "could not verify the token with Oakestra; try again later")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="oakestra-mcp"`)
	writeError(w, http.StatusUnauthorized, msg)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"error": msg})
}
