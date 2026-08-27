package route

import (
	"context"
	"fmt"
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
	registry RouteRegistry
	cfg      *config.Config
}

func NewRouteRegistryHandler(repo interfaces.ContainerRepository, registry RouteRegistry, cfg *config.Config) *RouteRegistryHandler {
	return &RouteRegistryHandler{repo: repo, registry: registry, cfg: cfg}
}

func (h *RouteRegistryHandler) Handle(evt event.Event) {
	ce, ok := evt.(types.ContainerEvent)
	if !ok {
		return
	}

	ctx := context.Background()
	routeKey, inst, appSession, tpl, err := h.loadContext(ctx, ce.ContainerInstanceID)
	if err != nil {
		logger.Warnf(ctx, "[RouteRegistryHandler] skip event=%s instance_id=%d: %v", ce.Event, ce.ContainerInstanceID, err)
		return
	}

	switch ce.Event {
	case "ContainerStarted", "ContainerResumed":
		port := tpl.Port
		if port == 0 {
			logger.Warnf(ctx, "[RouteRegistryHandler] skip register route key=%s: unresolved backend port", routeKey)
			return
		}
		if strings.TrimSpace(inst.IPAddress) == "" {
			logger.Warnf(ctx, "[RouteRegistryHandler] skip register route key=%s: empty container ip", routeKey)
			return
		}

		reg := Registration{
			RouteKey:   routeKey,
			PathPrefix: fmt.Sprintf("%s/%s/%d", config.ResolveAppsPathPrefix(h.cfg), appSession.AppType, appSession.ID),
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
		if err := h.registry.UpsertRoute(ctx, reg); err != nil {
			logger.Errorf(ctx, "[RouteRegistryHandler] upsert route failed key=%s err=%v", reg.RouteKey, err)
			return
		}
		logger.Infof(ctx, "[RouteRegistryHandler] route upserted key=%s event=%s", reg.RouteKey, ce.Event)

	case "ContainerStopped", "ContainerDeleted", "ContainerFailed":
		if err := h.registry.DeleteRoute(ctx, routeKey); err != nil {
			logger.Errorf(ctx, "[RouteRegistryHandler] delete route failed key=%s err=%v", routeKey, err)
			return
		}
		logger.Infof(ctx, "[RouteRegistryHandler] route deleted key=%s event=%s", routeKey, ce.Event)
	}
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
