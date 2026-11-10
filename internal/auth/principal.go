package auth

import (
	"context"
	"slices"
)

// Oakestra role names as they appear in the JWT "roles" claim.
const (
	RoleAdmin                  = "Admin"
	RoleOrganizationAdmin      = "Organization_Admin"
	RoleApplicationProvider    = "Application_Provider"
	RoleInfrastructureProvider = "Infrastructure_Provider"
)

// Principal is the authenticated caller. AccessToken is always a verified
// Oakestra access token (never the refresh token the caller may have sent),
// so it can be forwarded to the system manager as-is.
type Principal struct {
	User         string
	Roles        []string
	Organization string
	AccessToken  string
}

// HasRole reports whether the principal holds any of the given roles.
func (p Principal) HasRole(roles ...string) bool {
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}

// IsAdmin is a shorthand for HasRole(RoleAdmin).
func (p Principal) IsAdmin() bool { return p.HasRole(RoleAdmin) }

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal stored by the middleware, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
