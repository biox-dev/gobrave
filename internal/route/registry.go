package route

import "context"

// Registration describes one app-session route bound to a concrete backend.
type Registration struct {
	RouteKey            string
	ContainerInstanceID int64
	IsTrimPrefix        bool
	PathPrefix          string
	Backend             Backend
	Metadata            map[string]string
}

type Backend struct {
	Host string
	Port int
}

// RouteRegistry is the abstraction for route management backends such as
// Traefik API/File, in-process gateway, or a composite implementation.
type RouteRegistry interface {
	UpsertRoute(ctx context.Context, route Registration) error
	DeleteRoute(ctx context.Context, routeKey string) error
	DeleteRouteByContainerInstanceID(ctx context.Context, containerInstanceID int64) error
}

// PathRouteResolver resolves an incoming request path to a registered route.
// matchedPrefix is the path prefix bound to the resolved backend.
type PathRouteResolver interface {
	ResolveByPath(path string) (route Registration, matchedPrefix string, ok bool)
}
