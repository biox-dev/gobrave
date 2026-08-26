package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	copilot "github.com/github/copilot-sdk/go"
	copilotrpc "github.com/github/copilot-sdk/go/rpc"
	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/realtime"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const defaultCopilotCLIURL = "localhost:4321"

type LLMHandler struct {
	cliURL      string
	model       string
	githubToken string
	providerCfg *copilot.ProviderConfig
	hub         *realtime.Hub
	cfg         *config.Config
	projectSvc  interfaces.ProjectService
	llmSvc      interfaces.LLMService

	bridgeMu         sync.RWMutex
	bridgeSessions   map[string]*llmBridgeSession
	workflowSvc      interfaces.WorkflowService
	analsyisSvc      interfaces.AnalysisService
	nodeOrchestrator interfaces.NodeOrchestrator

	completionBroker *nodeCompletionBroker
}

// nodeCompletionBroker manages per-node completion waiters.
// Design pattern: Broker/Mediator – decouples the async node executor
// from the LLM tool caller via a channel-based signaling mechanism.
type nodeCompletionBroker struct {
	mu      sync.Mutex
	waiters map[int64][]chan nodeCompletionEvent
}

type nodeCompletionEvent struct {
	Status string
	Log    string
	Err    string
}

func newNodeCompletionBroker() *nodeCompletionBroker {
	return &nodeCompletionBroker{waiters: make(map[int64][]chan nodeCompletionEvent)}
}

// register creates a buffered channel and registers it under nodeID.
// Must be called BEFORE starting the node to avoid a completion-before-register race.
func (b *nodeCompletionBroker) register(nodeID int64) chan nodeCompletionEvent {
	ch := make(chan nodeCompletionEvent, 1)
	b.mu.Lock()
	b.waiters[nodeID] = append(b.waiters[nodeID], ch)
	b.mu.Unlock()
	return ch
}

// unregister removes a specific channel from the waiter list.
func (b *nodeCompletionBroker) unregister(nodeID int64, ch chan nodeCompletionEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.waiters[nodeID]
	for i, c := range list {
		if c == ch {
			b.waiters[nodeID] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// notify sends the event to all registered waiters for nodeID and clears them.
func (b *nodeCompletionBroker) notify(nodeID int64, event nodeCompletionEvent) {
	b.mu.Lock()
	waiters := b.waiters[nodeID]
	delete(b.waiters, nodeID)
	b.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- event:
		default:
		}
	}
}

func NewLLMHandler(hub *realtime.Hub, cfg *config.Config, projectSvc interfaces.ProjectService,
	llmSvc interfaces.LLMService,
	workflowSvc interfaces.WorkflowService,
	analsyisSvc interfaces.AnalysisService,
	nodeOrchestrator interfaces.NodeOrchestrator,
) *LLMHandler {
	cliURL := ""
	if cfg != nil && cfg.LLM != nil {
		cliURL = strings.TrimSpace(cfg.LLM.CLIURL)
	}
	if cliURL == "" {
		cliURL = defaultCopilotCLIURL
	}

	defaultModel := ""
	if cfg != nil && cfg.LLM != nil {
		defaultModel = strings.TrimSpace(cfg.LLM.Model)
	}
	githubToken := ""
	if cfg != nil && cfg.LLM != nil {
		githubToken = strings.TrimSpace(cfg.LLM.GitHubToken)
	}
	if githubToken == "" {
		githubToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	providerCfg := buildProviderConfigFromConfig(cfg, defaultModel)

	h := &LLMHandler{
		cliURL:           cliURL,
		model:            defaultModel,
		githubToken:      githubToken,
		providerCfg:      providerCfg,
		hub:              hub,
		cfg:              cfg,
		projectSvc:       projectSvc,
		llmSvc:           llmSvc,
		bridgeSessions:   make(map[string]*llmBridgeSession),
		workflowSvc:      workflowSvc,
		analsyisSvc:      analsyisSvc,
		nodeOrchestrator: nodeOrchestrator,
		completionBroker: newNodeCompletionBroker(),
	}
	h.hub.SubscribeInbound(h.onBridgeInboundMessage)
	return h
}

func buildProviderConfigFromConfig(cfg *config.Config, defaultModel string) *copilot.ProviderConfig {
	if cfg == nil || cfg.LLM == nil || cfg.LLM.Provider == nil {
		return nil
	}

	providerType := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider.Type))
	baseURL := strings.TrimSpace(cfg.LLM.Provider.BaseURL)
	apiKey := strings.TrimSpace(cfg.LLM.Provider.APIKey)
	bearerToken := strings.TrimSpace(cfg.LLM.Provider.BearerToken)

	if providerType == "" && baseURL == "" && apiKey == "" && bearerToken == "" {
		return nil
	}

	if providerType == "" {
		providerType = "openai"
	}

	providerCfg := &copilot.ProviderConfig{
		Type:        providerType,
		BaseURL:     baseURL,
		APIKey:      apiKey,
		BearerToken: bearerToken,
		ModelID:     strings.TrimSpace(defaultModel),
	}

	if providerCfg.BaseURL == "" {
		return nil
	}

	return providerCfg
}

type copilotChatRequest struct {
	Prompt     string `json:"prompt" binding:"required"`
	Model      string `json:"model"`
	AllowWrite bool   `json:"allow_write"`
}

type copilotChatResponse struct {
	Content string `json:"content"`
	Type    string `json:"type"`
	Model   string `json:"model"`
	CLIURL  string `json:"cli_url"`
}

type llmStreamEvent struct {
	name string
	data any
}

type copilotWSStartRequest struct {
	Type      string         `json:"type"`
	Prompt    string         `json:"prompt"`
	Model     string         `json:"model"`
	SessionID int64          `json:"session_id"`
	Env       map[string]any `json:"env"`
}

type copilotWSPermissionDecision struct {
	Type      string `json:"type"`
	SessionID int64  `json:"session_id"`
	RequestID string `json:"request_id"`
	Approved  bool   `json:"approved"`
	Reason    string `json:"reason"`
}

type llmIDQuery struct {
	ID int64 `form:"id" binding:"required"`
}

type llmIDBody struct {
	ID int64 `json:"id,string" binding:"required"`
}

type llmConversationListQuery struct {
	SessionID int64 `form:"session_id" binding:"required"`
}

type llmBridgeSession struct {
	userID    string
	sessionID int64
	cancel    context.CancelFunc

	decisionMu      sync.Mutex
	decisionWaiters map[string]chan llmPermissionDecision
}

type llmPermissionDecision struct {
	approved bool
	reason   string
}

func handleLLMError(c *gin.Context, err error, internalMsg string) {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
}

func (h *LLMHandler) CreateLLMSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req types.LLMSession
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	// if strings.TrimSpace(req.ProjectID) == "" {
	// 	c.Error(errors.NewValidationError("project_id is required"))
	// 	return
	// }
	project, err := h.projectSvc.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleLLMError(c, err, "failed to get active project")
		return
	}
	req.ProjectID = project.ID

	if err := h.llmSvc.CreateLLMSession(c.Request.Context(), userID, &req); err != nil {
		handleLLMError(c, err, "failed to create llm session")
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *LLMHandler) GetLLMSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req llmIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.llmSvc.GetLLMSessionByID(c.Request.Context(), userID, req.ID)
	if err != nil {
		handleLLMError(c, err, "failed to get llm session")
		return
	}

	c.JSON(http.StatusOK, item)
}

func (h *LLMHandler) UpdateLLMSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req types.LLMSession
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}
	if req.ProjectID == 0 {
		c.Error(errors.NewValidationError("project_id is required"))
		return
	}

	if err := h.llmSvc.UpdateLLMSession(c.Request.Context(), userID, &req); err != nil {
		handleLLMError(c, err, "failed to update llm session")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "llm session updated successfully"})
}

func (h *LLMHandler) DeleteLLMSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req llmIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.llmSvc.DeleteLLMSession(c.Request.Context(), userID, req.ID); err != nil {
		handleLLMError(c, err, "failed to delete llm session")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "llm session deleted successfully"})
}

func (h *LLMHandler) ListLLMSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	items, err := h.llmSvc.ListLLMSession(c.Request.Context(), userID)
	if err != nil {
		handleLLMError(c, err, "failed to list llm sessions")
		return
	}

	c.JSON(http.StatusOK, items)
}

func (h *LLMHandler) CreateLLMConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req types.LLMConversation
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.LLMSessionID == 0 {
		c.Error(errors.NewValidationError("llm_session_id is required"))
		return
	}
	if strings.TrimSpace(req.ConversationID) == "" {
		req.ConversationID = uuid.NewString()
	}

	if err := h.llmSvc.CreateLLMConversation(c.Request.Context(), userID, &req); err != nil {
		handleLLMError(c, err, "failed to create llm conversation")
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *LLMHandler) GetLLMConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req llmIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.llmSvc.GetLLMConversationByID(c.Request.Context(), userID, req.ID)
	if err != nil {
		handleLLMError(c, err, "failed to get llm conversation")
		return
	}

	c.JSON(http.StatusOK, item)
}

func (h *LLMHandler) UpdateLLMConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req types.LLMConversation
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}
	if req.LLMSessionID == 0 {
		c.Error(errors.NewValidationError("llm_session_id is required"))
		return
	}

	if err := h.llmSvc.UpdateLLMConversation(c.Request.Context(), userID, &req); err != nil {
		handleLLMError(c, err, "failed to update llm conversation")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "llm conversation updated successfully"})
}

func (h *LLMHandler) DeleteLLMConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req llmIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.llmSvc.DeleteLLMConversation(c.Request.Context(), userID, req.ID); err != nil {
		handleLLMError(c, err, "failed to delete llm conversation")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "llm conversation deleted successfully"})
}

func (h *LLMHandler) ListLLMConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req llmConversationListQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	items, err := h.llmSvc.ListLLMConversationBySessionID(c.Request.Context(), userID, req.SessionID)
	if err != nil {
		handleLLMError(c, err, "failed to list llm conversations")
		return
	}

	c.JSON(http.StatusOK, items)
}

// CopilotChat godoc
// @Summary      通过 Copilot SDK 调用本地 Copilot CLI Server
// @Description  连接已启动的 copilot --headless 服务并发送 prompt，按 SSE 流式返回助手消息
// @Tags         LLM
// @Accept       json
// @Produce      text/event-stream
// @Param        request  body      copilotChatRequest   true  "聊天请求"
// @Success      200      {string}  string  "SSE stream"
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /llm/copilot-cli/chat [post]
// func (h *LLMHandler) CopilotChat(c *gin.Context) {
// 	if _, ok := getCurrentUserID(c); !ok {
// 		return
// 	}

// 	ctx := c.Request.Context()
// 	streamCtx, streamCancel := context.WithCancel(context.Background())
// 	defer streamCancel()
// 	go func() {
// 		<-ctx.Done()
// 		streamCancel()
// 	}()

// 	var req copilotChatRequest
// 	if err := c.ShouldBindJSON(&req); err != nil {
// 		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
// 		return
// 	}

// 	model := strings.TrimSpace(req.Model)
// 	if model == "" {
// 		model = strings.TrimSpace(h.model)
// 	}
// 	if model == "" {
// 		model = "auto"
// 	}

// 	providerCfg := h.providerCfg
// 	if providerCfg != nil && strings.TrimSpace(providerCfg.ModelID) == "" && model != "" {
// 		copied := *providerCfg
// 		copied.ModelID = model
// 		providerCfg = &copied
// 	}

// 	client := copilot.NewClient(&copilot.ClientOptions{
// 		Connection: copilot.URIConnection{URL: h.cliURL},
// 	})

// 	eventCh := make(chan llmStreamEvent, 256)
// 	idleCh := make(chan struct{}, 1)

// 	if err := client.Start(ctx); err != nil {
// 		c.Error(errors.NewInternalServerError("failed to connect copilot cli server").WithDetails(err.Error()))
// 		return
// 	}
// 	defer func() {
// 		if err := client.Stop(); err != nil {
// 			logger.Warnf(ctx, "[LLM] failed to stop copilot client: %v", err)
// 		}
// 	}()

// 	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
// 		Model:     model,
// 		Streaming: copilot.Bool(true),
// 		Provider:  providerCfg,
// 		OnPermissionRequest: func(request copilot.PermissionRequest, invocation copilot.PermissionInvocation) (copilotrpc.PermissionDecision, error) {
// 			requiresWriteConfirm := request.Kind() == copilotrpc.PermissionRequestKindWrite

// 			if !requiresWriteConfirm && request.Kind() == copilotrpc.PermissionRequestKindShell {
// 				switch shellReq := request.(type) {
// 				case copilotrpc.PermissionRequestShell:
// 					requiresWriteConfirm = shellReq.HasWriteFileRedirection
// 				case *copilotrpc.PermissionRequestShell:
// 					requiresWriteConfirm = shellReq.HasWriteFileRedirection
// 				}
// 			}

// 			select {
// 			case eventCh <- llmStreamEvent{name: "permission.request", data: gin.H{
// 				"kind":                   string(request.Kind()),
// 				"request":                request,
// 				"session_id":             invocation.SessionID,
// 				"requires_write_confirm": requiresWriteConfirm,
// 				"allow_write":            req.AllowWrite,
// 			}}:
// 			default:
// 			}

// 			if requiresWriteConfirm && !req.AllowWrite {
// 				feedback := "write operation requires explicit allow_write=true"
// 				forceReject := true
// 				decision := copilotrpc.PermissionDecisionDeniedInteractivelyByUser{
// 					Feedback:    &feedback,
// 					ForceReject: &forceReject,
// 				}
// 				select {
// 				case eventCh <- llmStreamEvent{name: "permission.decision", data: gin.H{
// 					"kind":       string(request.Kind()),
// 					"approved":   false,
// 					"reason":     feedback,
// 					"session_id": invocation.SessionID,
// 				}}:
// 				default:
// 				}
// 				return decision, nil
// 			}

// 			decision := copilotrpc.PermissionDecisionApproveOnce{}
// 			select {
// 			case eventCh <- llmStreamEvent{name: "permission.decision", data: gin.H{
// 				"kind":       string(request.Kind()),
// 				"approved":   true,
// 				"session_id": invocation.SessionID,
// 			}}:
// 			default:
// 			}
// 			return decision, nil
// 		},
// 	})
// 	if err != nil {
// 		c.Error(errors.NewInternalServerError("failed to create copilot session").WithDetails(err.Error()))
// 		return
// 	}
// 	defer func() {
// 		if err := session.Disconnect(); err != nil {
// 			logger.Warnf(ctx, "[LLM] failed to disconnect copilot session: %v", err)
// 		}
// 	}()

// 	c.Writer.Header().Set("Content-Type", "text/event-stream")
// 	c.Writer.Header().Set("Cache-Control", "no-cache")
// 	c.Writer.Header().Set("Connection", "keep-alive")
// 	c.Writer.Header().Set("X-Accel-Buffering", "no")
// 	c.Status(http.StatusOK)
// 	c.Writer.Flush()

// 	_ = writeSSEEvent(c, "start", gin.H{"model": model, "cli_url": h.cliURL})

// 	unsubscribe := session.On(func(event copilot.SessionEvent) {
// 		eventType := string(event.Type())
// 		if eventType != "" && event.Data != nil {
// 			select {
// 			case eventCh <- llmStreamEvent{name: eventType, data: event.Data}:
// 			default:
// 			}
// 		}

// 		if eventType == "session.idle" {
// 			select {
// 			case idleCh <- struct{}{}:
// 			default:
// 			}
// 		}
// 	})
// 	defer unsubscribe()

// 	if _, err := session.Send(streamCtx, copilot.MessageOptions{Prompt: req.Prompt}); err != nil {
// 		_ = writeSSEEvent(c, "error", gin.H{"error": "failed to send copilot prompt", "detail": err.Error()})
// 		return
// 	}

// 	var finalContent strings.Builder
// 	idleTimeout := time.NewTimer(5 * time.Minute)
// 	defer idleTimeout.Stop()
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case <-idleTimeout.C:
// 			_ = writeSSEEvent(c, "error", gin.H{"error": "waiting for session.idle timeout"})
// 			return
// 		case ev := <-eventCh:
// 			switch ev.name {
// 			case "assistant.message_delta":
// 				if payload, ok := ev.data.(*copilot.AssistantMessageDeltaData); ok {
// 					if payload.DeltaContent != "" {
// 						finalContent.WriteString(payload.DeltaContent)
// 					}
// 				}
// 			case "assistant.message":
// 				if payload, ok := ev.data.(*copilot.AssistantMessageData); ok {
// 					if payload.Content != "" && finalContent.Len() == 0 {
// 						finalContent.WriteString(payload.Content)
// 					}
// 				}
// 			}

// 			if err := writeSSEEvent(c, ev.name, ev.data); err != nil {
// 				return
// 			}
// 		case <-idleCh:
// 			if err := writeSSEEvent(c, "completed", gin.H{"content": finalContent.String(), "model": model}); err != nil {
// 				return
// 			}
// 			return
// 		}
// 	}
// }

func (h *LLMHandler) onBridgeInboundMessage(msg realtime.InboundMessage) {
	if len(msg.Payload) == 0 {
		return
	}

	typ, _ := msg.Payload["type"].(string)
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return
	}

	switch typ {
	case "llm.chat.start":
		startReq := copilotWSStartRequest{
			Type:      typ,
			Prompt:    toString(msg.Payload["prompt"]),
			Model:     toString(msg.Payload["model"]),
			SessionID: toInt64(msg.Payload["session_id"]),
			Env:       toMapStringAny(msg.Payload["env"]),
		}

		sessionID := startReq.SessionID
		if sessionID == 0 {
			h.pushBridgeEvent(msg.UserID, sessionID, "error", gin.H{"error": "failed to start llm session", "detail": "session_id is required"})
			return
		}

		if err := h.startBridgeSession(context.Background(), msg.UserID, sessionID, startReq.Prompt, startReq.Model, startReq.Env); err != nil {
			h.pushBridgeEvent(msg.UserID, sessionID, "error", gin.H{"error": "failed to start llm session", "detail": err.Error()})
			return
		}
		h.pushBridgeEvent(msg.UserID, sessionID, "session.started", gin.H{"session_id": strconv.FormatInt(sessionID, 10)})
	case "llm.permission.decision":
		decision := copilotWSPermissionDecision{
			Type:      typ,
			SessionID: toInt64(msg.Payload["session_id"]),
			RequestID: toString(msg.Payload["request_id"]),
			Approved:  toBool(msg.Payload["approved"]),
			Reason:    toString(msg.Payload["reason"]),
		}
		h.acceptBridgeDecision(msg.UserID, decision)
	case "llm.chat.stop":
		h.stopBridgeSession(msg.UserID, toInt64(msg.Payload["session_id"]))
	}
}

func (h *LLMHandler) startBridgeSession(parent context.Context,
	userID string,
	sessionID int64,
	prompt, model string,
	env map[string]any) error {
	userID = strings.TrimSpace(userID)
	prompt = strings.TrimSpace(prompt)
	model = strings.TrimSpace(model)
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	if sessionID == 0 {
		return fmt.Errorf("session_id is required")
	}
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}

	key := bridgeSessionKey(userID, sessionID)
	h.bridgeMu.Lock()
	if _, exists := h.bridgeSessions[key]; exists {
		h.bridgeMu.Unlock()
		return fmt.Errorf("session already exists")
	}

	ctx, cancel := context.WithCancel(parent)
	session := &llmBridgeSession{
		userID:          userID,
		sessionID:       sessionID,
		cancel:          cancel,
		decisionWaiters: make(map[string]chan llmPermissionDecision),
	}
	h.bridgeSessions[key] = session
	h.bridgeMu.Unlock()

	if err := h.llmSvc.CreateLLMConversation(parent, userID, &types.LLMConversation{
		LLMSessionID:   sessionID,
		ConversationID: uuid.NewString(),
		Role:           "user",
		Content:        prompt,
	}); err != nil {
		h.removeBridgeSession(userID, sessionID)
		return err
	}

	go h.runBridgeSession(ctx, session, prompt, model, env)
	return nil
}

func (h *LLMHandler) runBridgeSession(ctx context.Context, session *llmBridgeSession, prompt, model string, env map[string]any) {
	defer h.removeBridgeSession(session.userID, session.sessionID)

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	go func() {
		<-ctx.Done()
		streamCancel()
	}()

	resolvedModel := strings.TrimSpace(model)
	if resolvedModel == "" {
		resolvedModel = strings.TrimSpace(h.model)
	}
	if resolvedModel == "" {
		resolvedModel = "auto"
	}

	copilotSessionID := buildCopilotSessionID(session.userID, session.sessionID)

	providerCfg := h.providerCfg
	if providerCfg != nil && strings.TrimSpace(providerCfg.ModelID) == "" && resolvedModel != "" {
		copied := *providerCfg
		copied.ModelID = resolvedModel
		providerCfg = &copied
	}

	client := copilot.NewClient(&copilot.ClientOptions{
		// Connection: copilot.URIConnection{URL: h.cliURL},
	})

	workingDir, err := h.resolveWorkingDirectory(ctx, session.userID, env)

	if err != nil {
		h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "failed to resolve working directory", "detail": err.Error()})
		return
	}
	systemMessage := buildRuntimeSystemMessage(session.userID, env, workingDir)
	// TODO
	// workingDir := "/data2/brave_analysis_workspace/data/7b3b510e-cf76-40bc-b3c9-cf2d3a81af34/analysis_node/2077962373498408960"

	eventCh := make(chan llmStreamEvent, 256)
	idleCh := make(chan struct{}, 1)

	if err := client.Start(ctx); err != nil {
		h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "failed to connect copilot cli server", "detail": err.Error()})
		return
	}
	defer func() {
		if err := client.Stop(); err != nil {
			logger.Warnf(ctx, "[LLM] failed to stop copilot client: %v", err)
		}
	}()

	permissionHandler := func(request copilot.PermissionRequest, invocation copilot.PermissionInvocation) (copilotrpc.PermissionDecision, error) {
		requiresWriteConfirm := requiresWriteApproval(request)
		requestID := uuid.NewString()
		// if request.Kind() == copilotrpc.PermissionRequestKindWrite {
		// 	// fmt.Println("xxx")
		// 	fmt.Printf("[Permission Check] invocation: %s, Kind: %v, Request: %+v\n", invocation.SessionID, request.Kind(), request)
		// }
		h.pushBridgeEvent(session.userID, session.sessionID, "permission.request", gin.H{
			"request_id":             requestID,
			"kind":                   string(request.Kind()),
			"request":                request,
			"copilot_session_id":     fmt.Sprintf("%v", invocation.SessionID),
			"requires_write_confirm": requiresWriteConfirm,
			"session_id":             strconv.FormatInt(session.sessionID, 10),
		})

		if !requiresWriteConfirm {
			decision := copilotrpc.PermissionDecisionApproveOnce{}
			h.pushBridgeEvent(session.userID, session.sessionID, "permission.decision", gin.H{
				"request_id": requestID,
				"kind":       string(request.Kind()),
				"approved":   true,
			})
			return decision, nil
		}

		waiter := session.registerDecisionWaiter(requestID)
		defer session.removeDecisionWaiter(requestID)

		select {
		case <-streamCtx.Done():
			feedback := "request canceled while waiting for ui confirmation"
			forceReject := true
			return copilotrpc.PermissionDecisionDeniedInteractivelyByUser{Feedback: &feedback, ForceReject: &forceReject}, nil
		case <-time.After(3 * time.Minute):
			feedback := "ui confirmation timeout"
			forceReject := true
			h.pushBridgeEvent(session.userID, session.sessionID, "permission.decision", gin.H{
				"request_id": requestID,
				"kind":       string(request.Kind()),
				"approved":   false,
				"reason":     feedback,
			})
			return copilotrpc.PermissionDecisionDeniedInteractivelyByUser{Feedback: &feedback, ForceReject: &forceReject}, nil
		case uiDecision := <-waiter:
			if uiDecision.approved {
				decision := copilotrpc.PermissionDecisionApproveOnce{}
				h.pushBridgeEvent(session.userID, session.sessionID, "permission.decision", gin.H{
					"request_id": requestID,
					"kind":       string(request.Kind()),
					"approved":   true,
				})
				return decision, nil
			}

			feedback := strings.TrimSpace(uiDecision.reason)
			if feedback == "" {
				feedback = "rejected by ui"
			}
			forceReject := true
			h.pushBridgeEvent(session.userID, session.sessionID, "permission.decision", gin.H{
				"request_id": requestID,
				"kind":       string(request.Kind()),
				"approved":   false,
				"reason":     feedback,
			})
			return copilotrpc.PermissionDecisionDeniedInteractivelyByUser{Feedback: &feedback, ForceReject: &forceReject}, nil
		}
	}

	copilotSession, resumed, err := h.openOrResumeCopilotSession(ctx, client, copilotSessionID, resolvedModel, workingDir, systemMessage, providerCfg, permissionHandler)
	if err != nil {
		h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "failed to initialize copilot session", "detail": err.Error()})
		return
	}
	defer func() {
		if err := copilotSession.Disconnect(); err != nil {
			logger.Warnf(ctx, "[LLM] failed to disconnect copilot session: %v", err)
		}
	}()

	h.pushBridgeEvent(session.userID, session.sessionID, "start", gin.H{
		"model":              resolvedModel,
		"cli_url":            h.cliURL,
		"copilot_session_id": copilotSessionID,
		"resumed":            resumed,
	})

	unsubscribe := copilotSession.On(func(event copilot.SessionEvent) {
		eventType := string(event.Type())
		if shouldForwardLLMEvent(eventType) && event.Data != nil {
			select {
			case eventCh <- llmStreamEvent{name: eventType, data: event.Data}:
			default:
			}
		}

		if eventType == "session.idle" {
			select {
			case idleCh <- struct{}{}:
			default:
			}
		}
	})
	defer unsubscribe()

	if _, err := copilotSession.Send(streamCtx, copilot.MessageOptions{Prompt: prompt}); err != nil {
		h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "failed to send copilot prompt", "detail": err.Error()})
		return
	}

	var finalContent strings.Builder
	idleTimeout := time.NewTimer(5 * time.Minute)
	defer idleTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-idleTimeout.C:
			h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "waiting for session.idle timeout"})
			return
		case ev := <-eventCh:
			switch ev.name {
			case "assistant.message_delta":
				if payload, ok := ev.data.(*copilot.AssistantMessageDeltaData); ok && payload.DeltaContent != "" {
					finalContent.WriteString(payload.DeltaContent)
				}
			case "assistant.message":
				if payload, ok := ev.data.(*copilot.AssistantMessageData); ok && payload.Content != "" && finalContent.Len() == 0 {
					finalContent.WriteString(payload.Content)
				}
			}
			h.pushBridgeEvent(session.userID, session.sessionID, ev.name, ev.data)
		case <-idleCh:
			if finalContent.Len() > 0 {
				if err := h.llmSvc.CreateLLMConversation(ctx, session.userID, &types.LLMConversation{
					LLMSessionID:   session.sessionID,
					ConversationID: uuid.NewString(),
					Role:           "assistant",
					Content:        finalContent.String(),
					Model:          resolvedModel,
				}); err != nil {
					h.pushBridgeEvent(session.userID, session.sessionID, "error", gin.H{"error": "failed to persist llm conversation", "detail": err.Error()})
					return
				}
			}
			h.pushBridgeEvent(session.userID, session.sessionID, "completed", gin.H{"content": finalContent.String(), "model": resolvedModel})
			return
		}
	}
}

func (h *LLMHandler) pushBridgeEvent(userID string, sessionID int64, event string, data any) {
	payload := gin.H{
		"type":        "llm.event",
		"session_id":  strconv.FormatInt(sessionID, 10),
		"event":       event,
		"data":        data,
		"require_ack": false,
	}
	if err := h.hub.PushMessage(userID, payload); err != nil {
		logger.Warnf(context.Background(), "[LLM] push bridge event failed user_id=%s session_id=%d event=%s err=%v", userID, sessionID, event, err)
	}
}

func (h *LLMHandler) acceptBridgeDecision(userID string, decision copilotWSPermissionDecision) {
	key := bridgeSessionKey(userID, decision.SessionID)
	h.bridgeMu.RLock()
	session, ok := h.bridgeSessions[key]

	h.bridgeMu.RUnlock()
	if !ok {
		logger.Errorf(context.Background(), "[LLM] accept bridge decision failed user_id=%s session_id=%d request_id=%s", userID, decision.SessionID, decision.RequestID)
		return
	}
	session.acceptDecision(strings.TrimSpace(decision.RequestID), llmPermissionDecision{approved: decision.Approved, reason: decision.Reason})
}

func (h *LLMHandler) stopBridgeSession(userID string, sessionID int64) {
	key := bridgeSessionKey(userID, sessionID)
	h.bridgeMu.RLock()
	session, ok := h.bridgeSessions[key]
	h.bridgeMu.RUnlock()
	if !ok {
		return
	}
	session.cancel()
}

func (h *LLMHandler) resolveWorkingDirectory(ctx context.Context, userID string, env map[string]any) (string, error) {
	if h.projectSvc == nil {
		return "", fmt.Errorf("project service is not initialized")
	}

	if env != nil {
		envType, ok := env["type"].(string)
		if !ok || strings.TrimSpace(envType) == "" {
			// return "", fmt.Errorf("env.type is required")
			return h.resolveDefaultWorkingDirectory(ctx, userID)
		}
		idStr, ok := env["id"]
		if !ok {
			return "", fmt.Errorf("env.id is required for script type")
		}
		id, err := strconv.ParseInt(fmt.Sprintf("%v", idStr), 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid env.script_id: %w", err)
		}
		switch strings.TrimSpace(envType) {
		case "script":
			scriptDir, _, err := h.workflowSvc.GetScriptFileByScriptID(ctx, id)
			if err != nil {
				return "", fmt.Errorf("failed to get script file by script id: %w", err)
			}
			// workingDir := filepath.Join(h.cfg.Storage.BaseDir, scriptDir)
			return scriptDir, nil
		case "analsyisNode":
			analsyisNode, err := h.analsyisSvc.GetAnalysisNodeByID(ctx, id)
			if err != nil {
				return "", err
			}
			return analsyisNode.WorkspaceDir, nil

		}
	}
	return h.resolveDefaultWorkingDirectory(ctx, userID)

}

func (h *LLMHandler) resolveDefaultWorkingDirectory(ctx context.Context, userID string) (string, error) {
	project, err := h.projectSvc.GetActiveProjectByUserID(ctx, strings.TrimSpace(userID))
	if err != nil {
		return "", err
	}

	if project == nil || strings.TrimSpace(project.ProjectID) == "" {
		return "", fmt.Errorf("active project is empty")
	}

	baseDir := ""
	if h.cfg != nil && h.cfg.Storage != nil {
		baseDir = strings.TrimSpace(h.cfg.Storage.BaseDir)
	}
	if baseDir == "" {
		return "", fmt.Errorf("storage.base_dir is empty")
	}

	workingDir := filepath.Join(baseDir, "data", strings.TrimSpace(project.ProjectID))
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return "", err
	}

	return workingDir, nil
}

func buildRuntimeSystemMessage(userID string, env map[string]any, workingDir string) *copilot.SystemMessageConfig {
	lines := []string{
		"You are operating inside Gobrave's LLM runtime.",
		"Follow the runtime context below when choosing tools or file locations.",
		fmt.Sprintf("current_user_id: %s", strings.TrimSpace(userID)),
		fmt.Sprintf("working_directory: %s", strings.TrimSpace(workingDir)),
	}

	if env == nil {
		lines = append(lines, "env: <nil>")
	} else {
		envType := strings.TrimSpace(toString(env["type"]))
		envID := strings.TrimSpace(fmt.Sprintf("%v", env["id"]))
		if envType == "" {
			envType = "<empty>"
		}
		if envID == "" || envID == "<nil>" {
			envID = "<empty>"
		}
		lines = append(lines,
			fmt.Sprintf("env.type: %s", envType),
			fmt.Sprintf("env.id: %s", envID),
		)
		if strings.EqualFold(envType, "analysisNode") {
			lines = append(lines,
				"When you need to run or inspect the current analysis node, use env.id as the analysis_node_id and do not guess a different ID.",
				"For executing workflow scripts in this analysis node context (for example main.R, main.py, or similar entry scripts), you must call the tool run_analysis_node with analysis_node_id=env.id.",
				"Do not execute scripts directly via shell/system calls such as Rscript, python, python3, bash, sh, or equivalent runtime commands.",
				"The runtime will execute the node in the correct container automatically through run_analysis_node.",
			)
		} else if strings.EqualFold(envType, "script") {
			lines = append(lines, "When you need to run a script-based task, resolve the script workspace from env.id through the runtime context.")
		}
	}

	return &copilot.SystemMessageConfig{Content: strings.Join(lines, "\n")}
}

func (h *LLMHandler) removeBridgeSession(userID string, sessionID int64) {
	key := bridgeSessionKey(userID, sessionID)
	h.bridgeMu.Lock()
	session, ok := h.bridgeSessions[key]
	if ok {
		delete(h.bridgeSessions, key)
	}
	h.bridgeMu.Unlock()
	if ok {
		session.closeAllDecisionWaiters()
	}
}

func bridgeSessionKey(userID string, sessionID int64) string {
	return strings.TrimSpace(userID) + "::" + strconv.FormatInt(sessionID, 10)
}

func buildCopilotSessionID(userID string, sessionID int64) string {
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		trimmedUserID = "anonymous"
	}

	b := strings.Builder{}
	b.Grow(len(trimmedUserID))
	for _, r := range trimmedUserID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	return "gobrave-" + b.String() + "-" + strconv.FormatInt(sessionID, 10)
}

// RunAnalysisNodeToolParams is the input for the run_analysis_node LLM tool.
type RunAnalysisNodeToolParams struct {
	AnalysisNodeID string `json:"analysis_node_id" jsonschema:"The analysis node ID to run, as a string to preserve precision"`
	TimeoutSeconds int    `json:"timeout_seconds" jsonschema:"Timeout seconds waiting for completion (default 600)"`
}

// RunAnalysisNodeToolResult is the output returned to the LLM after the node finishes.
type RunAnalysisNodeToolResult struct {
	AnalysisNodeID int64  `json:"analysis_node_id"`
	Status         string `json:"status"`
	Log            string `json:"log"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
}

// buildRunAnalysisNodeTool returns a copilot Tool that:
//  1. Registers a completion waiter (Broker pattern) BEFORE starting the node.
//  2. Calls RunAnalysisNodeWithID to reset state and trigger async execution.
//  3. Launches a background Polling Observer goroutine that watches node status.
//  4. Blocks on the waiter channel until completion or timeout.
//  5. Reads the node log file and returns the result to the LLM.
func (h *LLMHandler) buildRunAnalysisNodeTool() copilot.Tool {
	return copilot.DefineTool(
		"run_analysis_node",
		"Run an analysis node by its numeric ID, wait for it to finish, then return the execution log. Use this to trigger computations and inspect their output.",
		func(params RunAnalysisNodeToolParams, inv copilot.ToolInvocation) (RunAnalysisNodeToolResult, error) {
			ctx := context.Background()
			nodeID, err := parseAnalysisNodeToolID(params.AnalysisNodeID)
			if err != nil {
				return RunAnalysisNodeToolResult{Error: fmt.Sprintf("invalid analysis_node_id: %v", err)}, nil
			}
			if nodeID <= 0 {
				return RunAnalysisNodeToolResult{Error: "analysis_node_id must be a positive integer string"}, nil
			}

			timeoutSecs := params.TimeoutSeconds
			if timeoutSecs <= 0 {
				timeoutSecs = 60000
			}
			timeout := time.Duration(timeoutSecs) * time.Second

			// Step 1: Register waiter BEFORE starting to avoid completion-before-register race.
			ch := h.completionBroker.register(nodeID)
			defer h.completionBroker.unregister(nodeID, ch)

			// Step 2: Reset node state and submit to executor.
			node, err := h.analsyisSvc.GetAnalysisNodeByID(ctx, nodeID)

			if err != nil {
				return RunAnalysisNodeToolResult{
					AnalysisNodeID: nodeID,
					Status:         "error",
					Error:          fmt.Sprintf("failed to get analysis node: %v", err),
				}, nil
			}
			if err := h.analsyisSvc.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
				"status":                   dagruntime.StatusReady,
				"server_status":            "",
				"cache_hit":                false,
				"rerun_reason":             "node rerun requested by user",
				"error_message":            "",
				"exit_code":                0,
				"pid":                      0,
				"job_id":                   "",
				"started_at":               nil,
				"finished_at":              nil,
				"output_validation_errors": types.JSONSlice{},
				"updated_at":               time.Now().UTC(),
			}); err != nil {
				return RunAnalysisNodeToolResult{
					AnalysisNodeID: nodeID,
					Status:         "error",
					Error:          fmt.Sprintf("failed to update analysis node: %v", err),
				}, nil
			}
			//

			if err := h.runAnalysisNodeByID(ctx, nodeID); err != nil {
				return RunAnalysisNodeToolResult{
					AnalysisNodeID: nodeID,
					Status:         "error",
					Error:          fmt.Sprintf("failed to start analysis node: %v", err),
				}, nil
			}

			// Step 3: Launch polling observer goroutine.
			go h.pollNodeCompletion(ctx, nodeID, node.LogPath, timeout)

			// Step 4: Wait for completion signal or timeout.
			select {
			case event := <-ch:
				return RunAnalysisNodeToolResult{
					AnalysisNodeID: nodeID,
					Status:         event.Status,
					Log:            event.Log,
					Success:        isTerminalSuccess(event.Status),
					Error:          event.Err,
				}, nil
			case <-time.After(timeout):
				return RunAnalysisNodeToolResult{
					AnalysisNodeID: nodeID,
					Status:         "timeout",
					Error:          fmt.Sprintf("timeout after %ds waiting for node to complete", timeoutSecs),
				}, nil
			}
		},
	)
}

func parseAnalysisNodeToolID(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "<nil>" {
		return 0, nil
	}
	return strconv.ParseInt(trimmed, 10, 64)
}

// runAnalysisNodeByID resets the node status and submits it for async execution.
// Mirrors the logic in AnalysisHandler.RunAnalysisNodeWithID but uses only the
// services already injected into LLMHandler.
func (h *LLMHandler) runAnalysisNodeByID(ctx context.Context, nodeID int64) error {
	if h.nodeOrchestrator == nil {
		return fmt.Errorf("node orchestrator is not configured")
	}
	return h.nodeOrchestrator.StartAsync(ctx, nodeID)
}

// pollNodeCompletion is a Polling Observer that periodically queries the DB for
// node status. When the node reaches a terminal state it reads the log and
// notifies the completionBroker, which unblocks the waiting LLM tool call.
func (h *LLMHandler) pollNodeCompletion(ctx context.Context, nodeID int64, logPath string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				return // waiter will hit its own timeout; no need to notify
			}

			node, err := h.analsyisSvc.GetAnalysisNodeByID(ctx, nodeID)
			if err != nil {
				logger.Warnf(ctx, "[LLM] pollNodeCompletion: failed to get node %d: %v", nodeID, err)
				continue
			}

			if lp := strings.TrimSpace(node.LogPath); lp != "" {
				logPath = lp
			}

			status := strings.ToLower(strings.TrimSpace(node.Status))
			if !isTerminalStatus(status) {
				continue
			}

			// Terminal state reached – read log and notify.
			log := readNodeLog(logPath)
			h.completionBroker.notify(nodeID, nodeCompletionEvent{
				Status: node.Status,
				Log:    log,
			})
			return
		}
	}
}

const maxLogBytes = 12000

func readNodeLog(logPath string) string {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Sprintf("[could not read log: %v]", err)
	}
	if len(data) > maxLogBytes {
		return "...(truncated)\n" + string(data[len(data)-maxLogBytes:])
	}
	return string(data)
}

func isTerminalStatus(status string) bool {
	switch status {
	case "done", "failed", "skipped", "cached", "stopped":
		return true
	}
	return false
}

func isTerminalSuccess(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "done" || s == "cached" || s == "skipped"
}

// Define the parameter type
type WeatherParams struct {
	City string `json:"city" jsonschema:"The city name"`
}

// Define the return type
type WeatherResult struct {
	City        string `json:"city"`
	Temperature string `json:"temperature"`
	Condition   string `json:"condition"`
}

func (h *LLMHandler) openOrResumeCopilotSession(
	ctx context.Context,
	client *copilot.Client,
	copilotSessionID string,
	model string,
	workingDir string,
	systemMessage *copilot.SystemMessageConfig,
	providerCfg *copilot.ProviderConfig,
	permissionHandler copilot.PermissionHandlerFunc,
) (*copilot.Session, bool, error) {
	getWeather := copilot.DefineTool(
		"get_weather",
		"Get the current weather for a city",
		func(params WeatherParams, inv copilot.ToolInvocation) (WeatherResult, error) {
			// In a real app, you'd call a weather API here
			conditions := []string{"sunny", "cloudy", "rainy", "partly cloudy"}
			temp := rand.Intn(30) + 50
			condition := conditions[rand.Intn(len(conditions))]
			return WeatherResult{
				City:        params.City,
				Temperature: fmt.Sprintf("%d°F", temp),
				Condition:   condition,
			}, nil
		},
	)

	runAnalysisNode := h.buildRunAnalysisNodeTool()

	resumeCfg := &copilot.ResumeSessionConfig{
		Model:               model,
		GitHubToken:         h.githubToken,
		Streaming:           copilot.Bool(true),
		SystemMessage:       systemMessage,
		Provider:            providerCfg,
		WorkingDirectory:    workingDir,
		OnPermissionRequest: permissionHandler,
		Tools:               []copilot.Tool{getWeather, runAnalysisNode},
	}

	metadata, err := client.GetSessionMetadata(ctx, copilotSessionID)
	if err != nil {
		logger.Warnf(ctx, "[LLM] session metadata check failed session_id=%s: %v", copilotSessionID, err)
	} else if metadata != nil {
		// "/home/admin/.copilot/session-state/gobrave-74a7c2e4-2546-416c-8e33-e079d0905c61-2077303418459787264"
		s, resumeErr := client.ResumeSession(ctx, copilotSessionID, resumeCfg)
		if resumeErr != nil {
			return nil, false, fmt.Errorf("failed to resume session %s: %w", copilotSessionID, resumeErr)
		}
		return s, true, nil
	}

	createCfg := &copilot.SessionConfig{
		SessionID:           copilotSessionID,
		Model:               model,
		GitHubToken:         h.githubToken,
		Streaming:           copilot.Bool(true),
		SystemMessage:       systemMessage,
		Provider:            providerCfg,
		WorkingDirectory:    workingDir,
		OnPermissionRequest: permissionHandler,
		Tools:               []copilot.Tool{getWeather, runAnalysisNode},
	}

	s, createErr := client.CreateSession(ctx, createCfg)
	if createErr != nil {
		return nil, false, fmt.Errorf("failed to create session %s: %w", copilotSessionID, createErr)
	}

	return s, false, nil
}

func (s *llmBridgeSession) registerDecisionWaiter(requestID string) chan llmPermissionDecision {
	s.decisionMu.Lock()
	defer s.decisionMu.Unlock()
	if ch, ok := s.decisionWaiters[requestID]; ok {
		return ch
	}
	ch := make(chan llmPermissionDecision, 1)
	s.decisionWaiters[requestID] = ch
	return ch
}

func (s *llmBridgeSession) removeDecisionWaiter(requestID string) {
	s.decisionMu.Lock()
	defer s.decisionMu.Unlock()
	ch, ok := s.decisionWaiters[requestID]
	if !ok {
		return
	}
	delete(s.decisionWaiters, requestID)
	close(ch)
}

func (s *llmBridgeSession) acceptDecision(requestID string, decision llmPermissionDecision) {
	s.decisionMu.Lock()
	ch, ok := s.decisionWaiters[requestID]
	if ok {
		delete(s.decisionWaiters, requestID)
	}
	s.decisionMu.Unlock()
	if !ok {
		logger.Warnf(context.Background(), "[LLM] accept decision failed: no waiter found for request_id=%s", requestID)
		return
	}
	select {
	case ch <- decision:
	default:
	}
	close(ch)
}

func (s *llmBridgeSession) closeAllDecisionWaiters() {
	s.decisionMu.Lock()
	defer s.decisionMu.Unlock()
	for requestID, ch := range s.decisionWaiters {
		delete(s.decisionWaiters, requestID)
		close(ch)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func toBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
func toMapStringAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
func toInt64(v any) int64 {
	switch value := v.(type) {
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case string:
		parsed := strings.TrimSpace(value)
		if parsed == "" {
			return 0
		}
		id, err := strconv.ParseInt(parsed, 10, 64)
		if err != nil {
			return 0
		}
		return id
	default:
		return 0
	}
}

func requiresWriteApproval(request copilot.PermissionRequest) bool {
	requiresWriteConfirm := request.Kind() == copilotrpc.PermissionRequestKindWrite
	if !requiresWriteConfirm && request.Kind() == copilotrpc.PermissionRequestKindShell {
		switch shellReq := request.(type) {
		case copilotrpc.PermissionRequestShell:
			requiresWriteConfirm = shellReq.HasWriteFileRedirection
		case *copilotrpc.PermissionRequestShell:
			requiresWriteConfirm = shellReq.HasWriteFileRedirection
		}
	}
	return requiresWriteConfirm
}

func shouldForwardLLMEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "assistant.message_delta":
		return true
	case "assistant.reasoning_delta":
		return true
	case "permission.request":
		return true
	default:
		return false
	}
}

func writeSSEEvent(c *gin.Context, event string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, jsonData); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
