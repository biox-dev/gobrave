package manager

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

var _ event.Handler = (*AppSessionEventHandler)(nil)

// AppSessionEventHandler listens for ContainerEvent on the event bus and
// synchronizes the corresponding AppSession status accordingly.
//
// When a container transitions to creating/running/stopped/failed, this handler
// updates the owning AppSession's Status, StartedAt, and StoppedAt fields so
// that the AppSession reflects the actual container lifecycle.
//
// Only containers with OwnerType == ContainerOwnerAppSession are processed;
// DAG nodes and services are ignored.
type AppSessionEventHandler struct {
	repo interfaces.ContainerRepository
	hub  *realtime.Hub
}

func NewAppSessionEventHandler(repo interfaces.ContainerRepository, hub *realtime.Hub) *AppSessionEventHandler {
	return &AppSessionEventHandler{repo: repo, hub: hub}
}

func (h *AppSessionEventHandler) Handle(evt event.Event) {
	ce, ok := evt.(types.ContainerEvent)
	if !ok {
		return
	}

	ctx := context.Background()
	inst, err := h.repo.GetContainerInstanceByID(ctx, ce.ContainerInstanceID)
	if err != nil || inst == nil {
		return
	}
	if inst.OwnerType != types.ContainerOwnerAppSession {
		return
	}

	session, err := h.repo.GetAppSessionByID(ctx, inst.OwnerID)
	if err != nil || session == nil {
		return
	}

	now := time.Now()
	normalizedEvent := normalizeContainerEvent(ce.Event)
	switch normalizedEvent {
	case "running":
		session.Status = "RUNNING"
		session.StartedAt = &now
		session.StoppedAt = nil
	case "creating":
		session.Status = "CREATING"
		session.StoppedAt = nil
	case "starting":
		session.Status = "STARTING"
		session.StoppedAt = nil
	case "stopped":
		session.Status = "STOPPED"
		session.StoppedAt = &now
		session.StartedAt = nil
	case "failed":
		session.Status = "FAILED"
		session.StoppedAt = &now
	case "stopping":
		session.Status = "STOPPING"
		session.StoppedAt = &now
	// case "deleting":
	// 	session.Status = "DELETING"
	// 	session.StoppedAt = &now
	// case "deleted":
	// 	h.repo.DeleteAppSession(ctx, session.ID)
	default:
		return
	}

	if err := h.repo.UpdateAppSession(ctx, session); err != nil {
		logger.Warnf(ctx, "[AppSessionEventHandler] update app session failed, session_id=%d event=%s err=%v", session.ID, ce.Event, err)
		return
	}

	h.notifyStatusChanged(ctx, session, normalizedEvent)
}

func (h *AppSessionEventHandler) notifyStatusChanged(ctx context.Context, session *types.AppSession, normalizedEvent string) {
	if h.hub == nil || session == nil {
		return
	}
	userID := strings.TrimSpace(session.UserID)
	if userID == "" {
		return
	}

	method := appSessionRealtimeMethod(normalizedEvent)
	msg := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "app-session",
			"id":       strconv.FormatInt(session.ID, 10),
			"method":   method,
			"args": map[string]any{
				"id":     strconv.FormatInt(session.ID, 10),
				"status": strings.ToLower(strings.TrimSpace(session.Status)),
			},
		},
	}

	if err := h.hub.PushMessage(userID, msg); err != nil {
		if strings.Contains(err.Error(), "no_client_for_user:") {
			return
		}
		logger.Warnf(ctx, "[AppSessionEventHandler] push app session event failed user_id=%s session_id=%d event=%s err=%v", userID, session.ID, normalizedEvent, err)
	}
}

func appSessionRealtimeMethod(normalizedEvent string) string {
	switch normalizedEvent {
	case "creating", "starting":
		return "analysisSubmitted"
	case "running":
		return "analysisStarted"
	case "stopped", "failed", "stopping":
		return "analysisDone"
	default:
		return "analysisStarted"
	}
}

// normalizeContainerEvent maps the container-level event names (e.g. "ContainerStarted",
// "ContainerStopped") to a simplified AppSession status key: creating, running, stopped, or failed.
func normalizeContainerEvent(eventName string) string {
	eventName = strings.TrimSpace(eventName)
	switch eventName {
	case "ContainerCreating":
		return "creating"
	case "ContainerStarting":
		return "starting"
	case "ContainerStarted", "ContainerResumed":
		return "running"
	case "ContainerStopped", "ContainerExited":
		return "stopped"
	case "ContainerDeleting":
		return "deleting"
	case "ContainerDeleted":
		return "deleted"
	case "ContainerStopping":
		return "stopping"

	}

	if strings.Contains(strings.ToLower(eventName), "failed") {
		return "failed"
	}

	return ""
}
