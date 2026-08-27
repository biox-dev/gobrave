package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/realtime"
)

type RealtimeHandler struct {
	hub *realtime.Hub
	cfg *config.Config
}

type pushRealtimeRequest struct {
	UserID         string `json:"user_id" binding:"required"`
	Data           any    `json:"data" binding:"required"`
	WaitAck        bool   `json:"wait_ack"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func NewRealtimeHandler(hub *realtime.Hub, cfg *config.Config) *RealtimeHandler {
	return &RealtimeHandler{hub: hub, cfg: cfg}
}

func (h *RealtimeHandler) Connect(c *gin.Context) {
	transport := h.hub.Transport()
	if transport == realtime.TransportSSE {
		h.ConnectSSE(c)
		return
	}
	h.ConnectWS(c)
}

func (h *RealtimeHandler) ConnectWS(c *gin.Context) {
	if h.hub.Transport() != realtime.TransportWS {
		c.Error(errors.NewConflictError("realtime transport is configured as sse; websocket endpoint is disabled"))
		return
	}
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	if err := h.hub.HandleWebSocket(c.Writer, c.Request, userID); err != nil {
		c.Error(errors.NewInternalServerError("failed to establish websocket connection").WithDetails(err.Error()))
	}
}

func (h *RealtimeHandler) ConnectSSE(c *gin.Context) {
	if h.hub.Transport() != realtime.TransportSSE {
		c.Error(errors.NewConflictError("realtime transport is configured as ws; sse endpoint is disabled"))
		return
	}
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	if err := h.hub.HandleSSE(c.Writer, c.Request, userID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "context canceled") {
			return
		}
		c.Error(errors.NewInternalServerError("failed to establish sse connection").WithDetails(err.Error()))
	}
}

func (h *RealtimeHandler) Push(c *gin.Context) {
	var req pushRealtimeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	if req.WaitAck {
		timeout := 0 * time.Second
		if req.TimeoutSeconds > 0 {
			timeout = time.Duration(req.TimeoutSeconds) * time.Second
		}
		result := h.hub.PushMessageWaitAck(strings.TrimSpace(req.UserID), req.Data, timeout)
		if !result.OK && result.Error != "" {
			c.Error(errors.NewBadRequestError("push failed").WithDetails(result.Error))
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}

	if err := h.hub.PushMessage(strings.TrimSpace(req.UserID), req.Data); err != nil {
		c.Error(errors.NewBadRequestError("push failed").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "user_id": strings.TrimSpace(req.UserID)})
}

func (h *RealtimeHandler) Stats(c *gin.Context) {
	c.JSON(http.StatusOK, h.hub.Stats())
}
