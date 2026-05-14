package auth

import "context"

type Principal struct {
	Key           string   `json:"key"`
	Name          string   `json:"name,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Purpose       string   `json:"purpose,omitempty"`
	ModelAccess   string   `json:"model_access,omitempty"`
	ModelRouteIDs []string `json:"model_route_ids,omitempty"`
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
