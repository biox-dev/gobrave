package route

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
)

// Registry is the route registration factory. It holds named RouteRegistry
// implementations and resolves them by name at startup / runtime.
type Registry struct {
	registries map[string]RouteRegistry
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		registries: map[string]RouteRegistry{},
	}
}

// Register registers a RouteRegistry under a normalized name; same-name
// registrations overwrite the previous one.
func (r *Registry) Register(name string, reg RouteRegistry) {
	r.registries[normalizeRouteRegistry(name)] = reg
}

// Get returns the RouteRegistry registered under the given name, or nil.
func (r *Registry) Get(name string) RouteRegistry {
	return r.registries[normalizeRouteRegistry(name)]
}

// Has reports whether a RouteRegistry is registered under the given name.
func (r *Registry) Has(name string) bool {
	_, ok := r.registries[normalizeRouteRegistry(name)]
	return ok
}

// List returns all registered RouteRegistry instances.
func (r *Registry) List() []RouteRegistry {
	items := make([]RouteRegistry, 0, len(r.registries))
	for _, reg := range r.registries {
		items = append(items, reg)
	}
	return items
}

// Names returns the normalized names of registered registries, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.registries))
	for name := range r.registries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// normalizeRouteRegistry maps a route registry name to its canonical form.
// Unknown names are preserved (lowercased/trimmed) so callers can return a
// descriptive "unsupported registry" error.
func normalizeRouteRegistry(name string) string {
	v := strings.ToLower(strings.TrimSpace(name))
	switch v {
	case "traefik":
		return "traefik"
	case "k8s-ingress", "k8s_ingress", "k8singress", "k8s", "kubernetes":
		return "k8s-ingress"
	default:
		return v
	}
}

// NewRegistryFromConfig builds a Registry populated with the external route
// registries selected by cfg.Route.Registrys, in declaration order. The
// in-process gateway is managed separately and is not part of this registry.
func NewRegistryFromConfig(cfg *config.Config) (*Registry, error) {
	reg := NewRegistry()

	var names []string
	if cfg == nil || cfg.Route == nil || len(cfg.Route.Registrys) == 0 {
		names = []string{}
	} else {
		names = cfg.Route.Registrys
	}

	for _, name := range names {
		normalized := normalizeRouteRegistry(name)
		if reg.Has(normalized) {
			continue
		}
		built, err := buildRouteRegistry(cfg, normalized)
		if err != nil {
			return nil, err
		}
		reg.Register(normalized, built)
	}

	return reg, nil
}

// buildRouteRegistry constructs a single RouteRegistry for the given
// normalized name.
func buildRouteRegistry(cfg *config.Config, name string) (RouteRegistry, error) {
	switch name {
	case "traefik":
		if cfg == nil || cfg.Route == nil || cfg.Route.Traefik == nil {
			return nil, fmt.Errorf("route.traefik config is required when route.registrys contains traefik")
		}
		t := cfg.Route.Traefik
		return NewTraefikRegistry(TraefikRegistryConfig{
			Provider:   t.Provider,
			BaseURL:    t.BaseURL,
			UpsertPath: t.UpsertPath,
			DeletePath: t.DeletePath,
			AuthToken:  t.AuthToken,
			Timeout:    time.Duration(t.TimeoutSecond) * time.Second,
			FilePath:   t.FilePath,
		})
	case "k8s-ingress":
		return buildK8sIngressRegistry(cfg)
	default:
		return nil, fmt.Errorf("unsupported route registry: %s", name)
	}
}

// buildK8sIngressRegistry constructs the Kubernetes Ingress based route
// registry, falling back to the container Kubernetes settings for namespace /
// kubeconfig / in-cluster when not explicitly set in the ingress config.
func buildK8sIngressRegistry(cfg *config.Config) (RouteRegistry, error) {
	ingressCfg := &config.K8sIngressRouteConfig{}
	if cfg != nil && cfg.Route != nil && cfg.Route.K8sIngress != nil {
		ingressCfg = cfg.Route.K8sIngress
	}

	namespace := strings.TrimSpace(ingressCfg.Namespace)
	if namespace == "" && cfg != nil && cfg.Container != nil && cfg.Container.Kubernetes != nil {
		namespace = strings.TrimSpace(cfg.Container.Kubernetes.Namespace)
	}
	if namespace == "" {
		namespace = "default"
	}

	kubeconfig := strings.TrimSpace(ingressCfg.Kubeconfig)
	inCluster := ingressCfg.InCluster
	if cfg != nil && cfg.Container != nil && cfg.Container.Kubernetes != nil {
		if kubeconfig == "" {
			kubeconfig = strings.TrimSpace(cfg.Container.Kubernetes.Kubeconfig)
		}
		if !inCluster {
			inCluster = cfg.Container.Kubernetes.InCluster
		}
	}

	return NewK8sIngressRegistry(K8sIngressRegistryConfig{
		Namespace:        namespace,
		Kubeconfig:       kubeconfig,
		InCluster:        inCluster,
		IngressClassName: ingressCfg.IngressClassName,
		Host:             ingressCfg.Host,
		PathType:         ingressCfg.PathType,
		Annotations:      ingressCfg.Annotations,
	})
}
