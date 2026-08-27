package route

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/goccy/go-yaml"
)

type TraefikRegistryConfig struct {
	Provider   string
	BaseURL    string
	UpsertPath string
	DeletePath string
	AuthToken  string
	Timeout    time.Duration
	FilePath   string
}

type TraefikRegistry struct {
	provider   string
	baseURL    string
	upsertPath string
	deletePath string
	authToken  string
	filePath   string
	fileMu     sync.Mutex
	client     *http.Client
}

type traefikRoutePayload struct {
	RouteKey   string            `json:"route_key"`
	PathPrefix string            `json:"path_prefix"`
	BackendURL string            `json:"backend_url"`
	Backend    Backend           `json:"backend"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Traefik    traefikHTTPConfig `json:"traefik"`
}

type traefikHTTPConfig struct {
	Router      traefikRouterConfig              `json:"router"`
	Service     traefikServiceConfig             `json:"service"`
	Middlewares map[string]traefikMiddlewareSpec `json:"middlewares,omitempty"`
}

type traefikRouterConfig struct {
	Name        string   `json:"name"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entry_points"`
	Middlewares []string `json:"middlewares,omitempty"`
}

type traefikServiceConfig struct {
	Name         string                    `json:"name"`
	LoadBalancer traefikLoadBalancerConfig `json:"load_balancer"`
}

type traefikLoadBalancerConfig struct {
	Servers []traefikServer `json:"servers"`
}

type traefikServer struct {
	URL string `json:"url"`
}

type traefikMiddlewareSpec struct {
	StripPrefix *traefikStripPrefixConfig `json:"stripPrefix,omitempty" yaml:"stripPrefix,omitempty"`
	Headers     *traefikHeadersConfig     `json:"headers,omitempty" yaml:"headers,omitempty"`
}

type traefikStripPrefixConfig struct {
	Prefixes   []string `json:"prefixes" yaml:"prefixes"`
	ForceSlash bool     `json:"forceSlash" yaml:"forceSlash"`
}

type traefikHeadersConfig struct {
	CustomRequestHeaders map[string]string `json:"customRequestHeaders,omitempty" yaml:"customRequestHeaders,omitempty"`
}

type traefikFileConfig struct {
	HTTP traefikFileHTTPConfig `yaml:"http"`
}

type traefikFileHTTPConfig struct {
	Routers     map[string]traefikFileRouter     `yaml:"routers"`
	Services    map[string]traefikFileService    `yaml:"services"`
	Middlewares map[string]traefikMiddlewareSpec `yaml:"middlewares,omitempty"`
}

type traefikFileRouter struct {
	Rule        string   `yaml:"rule"`
	Service     string   `yaml:"service"`
	EntryPoints []string `yaml:"entryPoints"`
	Middlewares []string `yaml:"middlewares,omitempty"`
}

type traefikFileService struct {
	LoadBalancer traefikFileLoadBalancer `yaml:"loadBalancer"`
}

type traefikFileLoadBalancer struct {
	Servers []traefikFileServer `yaml:"servers"`
}

type traefikFileServer struct {
	URL string `yaml:"url"`
}

type traefikProfileMiddlewareBuilder struct {
	build func(route Registration) ([]string, map[string]traefikMiddlewareSpec)
}

var traefikProfileMiddlewareBuilders = map[string]traefikProfileMiddlewareBuilder{
	"default": {
		build: buildSCodeMiddlewares,
	},
	"rstudio": {
		build: buildRStudioMiddlewares,
	},
	"code-server": {
		build: buildSCodeMiddlewares,
	},
	"notebook": {
		build: buildNotebookMiddlewares,
	},
}

func NewTraefikRegistry(cfg TraefikRegistryConfig) (*TraefikRegistry, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "api"
	}
	if provider != "api" && provider != "file" {
		return nil, fmt.Errorf("unsupported traefik provider: %s", cfg.Provider)
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if provider == "api" && baseURL == "" {
		return nil, fmt.Errorf("traefik base url is required for api provider")
	}

	filePath := strings.TrimSpace(cfg.FilePath)
	if provider == "file" {
		if filePath == "" {
			return nil, fmt.Errorf("traefik file path is required for file provider")
		}
		filePath = filepath.Clean(filePath)
	}

	upsertPath := strings.TrimSpace(cfg.UpsertPath)
	if upsertPath == "" {
		upsertPath = "/api/providers/http/routes/{route_key}"
	}
	deletePath := strings.TrimSpace(cfg.DeletePath)
	if deletePath == "" {
		deletePath = "/api/providers/http/routes/{route_key}"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &TraefikRegistry{
		provider:   provider,
		baseURL:    strings.TrimRight(baseURL, "/"),
		upsertPath: upsertPath,
		deletePath: deletePath,
		authToken:  strings.TrimSpace(cfg.AuthToken),
		filePath:   filePath,
		client:     &http.Client{Timeout: timeout},
	}, nil
}

func (r *TraefikRegistry) UpsertRoute(ctx context.Context, route Registration) error {
	if err := validateRegistration(route); err != nil {
		return err
	}
	if r.provider == "file" {
		if err := r.upsertRouteToFile(route); err != nil {
			return err
		}
		logger.Infof(ctx, "[TraefikRegistry] upsert route(file) key=%s prefix=%s backend=%s:%d", route.RouteKey, route.PathPrefix, route.Backend.Host, route.Backend.Port)
		return nil
	}

	payload := buildTraefikRoutePayload(route)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := r.resolvePath(r.upsertPath, route.RouteKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	r.attachAuth(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		hint := ""
		if resp.StatusCode == http.StatusNotFound {
			hint = " (endpoint not found; check route.traefik.base_url and route.traefik.upsert_path, or switch route.registry=gateway if no writable Traefik provider API is deployed)"
		}
		return fmt.Errorf("traefik upsert route failed: method=%s url=%s status=%d body=%s%s", req.Method, req.URL.String(), resp.StatusCode, strings.TrimSpace(string(respBody)), hint)
	}

	logger.Infof(ctx, "[TraefikRegistry] upsert route key=%s prefix=%s backend=%s:%d", route.RouteKey, route.PathPrefix, route.Backend.Host, route.Backend.Port)
	return nil
}

func (r *TraefikRegistry) DeleteRoute(ctx context.Context, routeKey string) error {
	if strings.TrimSpace(routeKey) == "" {
		return fmt.Errorf("route key is required")
	}
	if r.provider == "file" {
		if err := r.deleteRouteFromFile(routeKey); err != nil {
			return err
		}
		logger.Infof(ctx, "[TraefikRegistry] delete route(file) key=%s", routeKey)
		return nil
	}

	endpoint := r.resolvePath(r.deletePath, routeKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	r.attachAuth(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		hint := ""
		if resp.StatusCode == http.StatusNotFound {
			hint = " (endpoint not found; check route.traefik.base_url and route.traefik.delete_path, or switch route.registry=gateway if no writable Traefik provider API is deployed)"
		}
		return fmt.Errorf("traefik delete route failed: method=%s url=%s status=%d body=%s%s", req.Method, req.URL.String(), resp.StatusCode, strings.TrimSpace(string(respBody)), hint)
	}

	logger.Infof(ctx, "[TraefikRegistry] delete route key=%s", routeKey)
	return nil
}

func (r *TraefikRegistry) resolvePath(pathTemplate string, routeKey string) string {
	path := strings.TrimSpace(pathTemplate)
	if path == "" {
		path = "/api/providers/http/routes/{route_key}"
	}
	path = strings.ReplaceAll(path, "{route_key}", routeKey)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return r.baseURL + path
}

func (r *TraefikRegistry) attachAuth(req *http.Request) {
	if req == nil || r.authToken == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+r.authToken)
}

func (r *TraefikRegistry) upsertRouteToFile(route Registration) error {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()

	cfg, err := r.readFileConfig()
	if err != nil {
		return err
	}

	serviceName := route.RouteKey + "-svc"
	backendURL := "http://" + route.Backend.Host + ":" + strconv.Itoa(route.Backend.Port)
	routerMiddlewares, middlewareDefs := buildProfileMiddlewares(route)
	cfg.HTTP.Routers[route.RouteKey] = traefikFileRouter{
		Rule:        "PathPrefix(`" + route.PathPrefix + "`)",
		Service:     serviceName,
		EntryPoints: []string{"web"},
		Middlewares: routerMiddlewares,
	}
	cfg.HTTP.Services[serviceName] = traefikFileService{
		LoadBalancer: traefikFileLoadBalancer{
			Servers: []traefikFileServer{{URL: backendURL}},
		},
	}
	for key, middleware := range middlewareDefs {
		cfg.HTTP.Middlewares[key] = middleware
	}

	return r.writeFileConfig(cfg)
}

func (r *TraefikRegistry) deleteRouteFromFile(routeKey string) error {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()

	cfg, err := r.readFileConfig()
	if err != nil {
		return err
	}

	delete(cfg.HTTP.Routers, routeKey)
	delete(cfg.HTTP.Services, routeKey+"-svc")
	for key := range cfg.HTTP.Middlewares {
		if strings.HasPrefix(key, routeKey+"-") {
			delete(cfg.HTTP.Middlewares, key)
		}
	}

	return r.writeFileConfig(cfg)
}

func (r *TraefikRegistry) readFileConfig() (*traefikFileConfig, error) {
	cfg := &traefikFileConfig{
		HTTP: traefikFileHTTPConfig{
			Routers:     map[string]traefikFileRouter{},
			Services:    map[string]traefikFileService{},
			Middlewares: map[string]traefikMiddlewareSpec{},
		},
	}

	content, err := os.ReadFile(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read traefik file provider config failed: %w", err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(content, cfg); err != nil {
		return nil, fmt.Errorf("parse traefik file provider config failed: %w", err)
	}
	if cfg.HTTP.Routers == nil {
		cfg.HTTP.Routers = map[string]traefikFileRouter{}
	}
	if cfg.HTTP.Services == nil {
		cfg.HTTP.Services = map[string]traefikFileService{}
	}
	if cfg.HTTP.Middlewares == nil {
		cfg.HTTP.Middlewares = map[string]traefikMiddlewareSpec{}
	}

	return cfg, nil
}

func (r *TraefikRegistry) writeFileConfig(cfg *traefikFileConfig) error {
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0o755); err != nil {
		return fmt.Errorf("create traefik file provider dir failed: %w", err)
	}

	content, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal traefik file provider config failed: %w", err)
	}

	if err := os.WriteFile(r.filePath, content, 0o644); err != nil {
		return fmt.Errorf("write traefik file provider config failed: %w", err)
	}
	return nil
}

func buildTraefikRoutePayload(route Registration) *traefikRoutePayload {
	serviceName := route.RouteKey + "-svc"
	backendURL := "http://" + route.Backend.Host + ":" + strconv.Itoa(route.Backend.Port)
	routerMiddlewares, middlewareSpecs := buildProfileMiddlewares(route)
	return &traefikRoutePayload{
		RouteKey:   route.RouteKey,
		PathPrefix: route.PathPrefix,
		BackendURL: backendURL,
		Backend:    route.Backend,
		Metadata:   route.Metadata,
		Traefik: traefikHTTPConfig{
			Router: traefikRouterConfig{
				Name:        route.RouteKey,
				Rule:        "PathPrefix(`" + route.PathPrefix + "`)",
				Service:     serviceName,
				EntryPoints: []string{"web"},
				Middlewares: routerMiddlewares,
			},
			Service: traefikServiceConfig{
				Name: serviceName,
				LoadBalancer: traefikLoadBalancerConfig{
					Servers: []traefikServer{{URL: backendURL}},
				},
			},
			Middlewares: middlewareSpecs,
		},
	}
}

func buildProfileMiddlewares(route Registration) ([]string, map[string]traefikMiddlewareSpec) {
	profile := resolveTraefikProfile(route)
	builder, ok := traefikProfileMiddlewareBuilders[profile]
	if !ok || builder.build == nil {
		return nil, nil
	}
	return builder.build(route)
}

func resolveTraefikProfile(route Registration) string {
	if p := strings.ToLower(strings.TrimSpace(route.Metadata["traefik_profile"])); p != "" {
		if _, ok := traefikProfileMiddlewareBuilders[p]; ok {
			return p
		}
	}
	return "default"
}

func buildDefaultMiddlewares(_ Registration) ([]string, map[string]traefikMiddlewareSpec) {
	return nil, nil
}

func buildRStudioMiddlewares(route Registration) ([]string, map[string]traefikMiddlewareSpec) {
	stripName := route.RouteKey + "-strip"
	headerName := route.RouteKey + "-server-root-path-header"

	return []string{stripName, headerName}, map[string]traefikMiddlewareSpec{
		stripName: {
			StripPrefix: &traefikStripPrefixConfig{
				Prefixes:   []string{route.PathPrefix},
				ForceSlash: false,
			},
		},
		headerName: {
			Headers: &traefikHeadersConfig{
				CustomRequestHeaders: map[string]string{
					"X-RStudio-Root-Path": route.PathPrefix,
				},
			},
		},
	}
}

func buildSCodeMiddlewares(route Registration) ([]string, map[string]traefikMiddlewareSpec) {
	stripName := route.RouteKey + "-strip"

	return []string{stripName}, map[string]traefikMiddlewareSpec{
		stripName: {
			StripPrefix: &traefikStripPrefixConfig{
				Prefixes:   []string{route.PathPrefix},
				ForceSlash: false,
			},
		},
	}
}

func buildNotebookMiddlewares(route Registration) ([]string, map[string]traefikMiddlewareSpec) {
	headerName := route.RouteKey + "-forwarded-prefix-header"

	return []string{headerName}, map[string]traefikMiddlewareSpec{
		headerName: {
			Headers: &traefikHeadersConfig{
				CustomRequestHeaders: map[string]string{
					"X-Forwarded-Prefix": route.PathPrefix,
				},
			},
		},
	}
}

func validateRegistration(route Registration) error {
	if strings.TrimSpace(route.RouteKey) == "" {
		return fmt.Errorf("route key is required")
	}
	if strings.TrimSpace(route.PathPrefix) == "" {
		return fmt.Errorf("path prefix is required")
	}
	if strings.TrimSpace(route.Backend.Host) == "" {
		return fmt.Errorf("backend host is required")
	}
	if route.Backend.Port <= 0 || route.Backend.Port > 65535 {
		return fmt.Errorf("invalid backend port: %d", route.Backend.Port)
	}
	return nil
}
