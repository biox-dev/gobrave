package route

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

var _ event.Handler = (*RouteRegistryHandler)(nil)

type RouteRegistryHandler struct {
	repo     interfaces.ContainerRepository
	registry *Registry
	gateway  *Gateway
	cfg      *config.Config
}

func NewRouteRegistryHandler(repo interfaces.ContainerRepository, registry *Registry, gateway *Gateway, cfg *config.Config) *RouteRegistryHandler {
	return &RouteRegistryHandler{repo: repo, registry: registry, gateway: gateway, cfg: cfg}
}

func (h *RouteRegistryHandler) Handle(evt event.Event) {
	ce, ok := evt.(types.ContainerEvent)
	if !ok {
		return
	}

	ctx := context.Background()

	// runtime 到外部 route registry 映射（进程内 gateway 单独处理）：
	//  docker  -> gateway（进程内网关，Backend 使用容器 IP）
	//  k8s/k3s -> k8s-ingress（外部注册中心，Backend 使用容器代理地址）
	runtimeMap := map[string]string{
		"k8s": "k8s-ingress",
		"k3s": "k8s-ingress",
	}

	switch ce.Event {
	case "ContainerStarted", "ContainerResumed":
		routeKey, inst, appSession, tpl, err := h.loadContext(ctx, ce.ContainerInstanceID)
		if err != nil {
			logger.Warnf(ctx, "[RouteRegistryHandler] skip event=%s instance_id=%d: %v", ce.Event, ce.ContainerInstanceID, err)
			return
		}

		port := tpl.Port
		if port == 0 {
			logger.Warnf(ctx, "[RouteRegistryHandler] skip register route key=%s: unresolved backend port", routeKey)
			return
		}
		if strings.TrimSpace(inst.IPAddress) == "" {
			logger.Warnf(ctx, "[RouteRegistryHandler] skip register route key=%s: empty container ip", routeKey)
			return
		}

		runtimeName := inst.RuntimeName
		reg := Registration{
			RouteKey:            routeKey,
			ContainerInstanceID: inst.ID,
			IsTrimPrefix:        true,
			PathPrefix:          fmt.Sprintf("%s/%s/%d", config.ResolveAppsPathPrefix(h.cfg), appSession.AppType, appSession.ID),
			Backend: Backend{
				Host: strings.TrimSpace(inst.IPAddress),
				Port: port,
			},
			Metadata: map[string]string{
				"owner_type":            string(inst.OwnerType),
				"container_instance_id": strconv.FormatInt(inst.ID, 10),
				"app_session_id":        strconv.FormatInt(appSession.ID, 10),
				"container_template_id": strconv.FormatInt(tpl.ID, 10),
			},
		}
		if profile := normalizeTraefikProfile(tpl.AppType); profile != "" {
			reg.Metadata["traefik_profile"] = profile
		}
		extReg := reg
		if appSession.AppType == "notebook" {
			extReg.IsTrimPrefix = false
		}

		// UpsertRoute 仅针对外部注册中心（traefik / k8s-ingress）调用，
		// 其 Backend 指向容器代理（ProxyConfig.Container）。
		if registryName, ok := runtimeMap[runtimeName]; ok {
			if registry := h.registry.Get(registryName); registry != nil {

				if err := registry.UpsertRoute(ctx, reg); err != nil {
					logger.Errorf(ctx, "[RouteRegistryHandler] upsert route failed key=%s err=%v", reg.RouteKey, err)
					return
				}

				extReg.Backend = resolveContainerProxyBackend(h.cfg)
				extReg.IsTrimPrefix = false
			}
		}

		// 进程内网关：记录始终加入网关，Backend 使用容器 IP，供 AppSessionProxy 解析。
		if err := h.gateway.UpsertRoute(ctx, extReg); err != nil {
			logger.Errorf(ctx, "[RouteRegistryHandler] gateway upsert route failed key=%s err=%v", reg.RouteKey, err)
			return
		}
		logger.Infof(ctx, "[RouteRegistryHandler] route upserted key=%s event=%s runtime=%s", reg.RouteKey, ce.Event, runtimeName)

	case "ContainerStopped", "ContainerDeleted", "ContainerFailed":
		if err := h.gateway.DeleteRouteByContainerInstanceID(ctx, ce.ContainerInstanceID); err != nil {
			logger.Errorf(ctx, "[RouteRegistryHandler] gateway delete route failed instance_id=%d err=%v", ce.ContainerInstanceID, err)
		}
		for _, reg := range h.registry.List() {
			if err := reg.DeleteRouteByContainerInstanceID(ctx, ce.ContainerInstanceID); err != nil {
				logger.Errorf(ctx, "[RouteRegistryHandler] delete route failed instance_id=%d err=%v", ce.ContainerInstanceID, err)
			}
		}
		logger.Infof(ctx, "[RouteRegistryHandler] route deleted instance_id=%d event=%s", ce.ContainerInstanceID, ce.Event)
	}
}

// resolveContainerProxyBackend 将 ProxyConfig.Container 解析为路由 Backend。
// 供外部注册中心（traefik / k8s-ingress）转发到容器代理使用。
func resolveContainerProxyBackend(cfg *config.Config) Backend {
	target := "http://localhost:8089"
	if cfg != nil && cfg.Proxy != nil && strings.TrimSpace(cfg.Proxy.Container) != "" {
		target = strings.TrimSpace(cfg.Proxy.Container)
	}

	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return Backend{}
	}

	port := 80
	if strings.EqualFold(u.Scheme, "https") {
		port = 443
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	return Backend{Host: u.Hostname(), Port: port}
}

func normalizeTraefikProfile(profile string) string {
	return strings.ToLower(strings.TrimSpace(profile))
}

func (h *RouteRegistryHandler) loadContext(ctx context.Context, containerInstanceID int64) (string, *types.ContainerInstance, *types.AppSession, *types.ContainerTemplate, error) {
	inst, err := h.repo.GetContainerInstanceByID(ctx, containerInstanceID)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if inst.OwnerType != types.ContainerOwnerAppSession {
		return "", nil, nil, nil, fmt.Errorf("owner type is not app_session: %s", inst.OwnerType)
	}

	appSession, err := h.repo.GetAppSessionByID(ctx, inst.OwnerID)
	if err != nil {
		return "", nil, nil, nil, err
	}

	tpl, err := h.repo.GetContainerTemplateByID(ctx, inst.TemplateID)
	if err != nil {
		return "", nil, nil, nil, err
	}

	routeKey := fmt.Sprintf("app-session-%d", appSession.ID)
	return routeKey, inst, appSession, tpl, nil
}
