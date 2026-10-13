package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	opencodesdk "github.com/biox-dev/opencode/sdk"
)

// NewCustom creates a provider that bridges gobrave agent requests to OpenCode SDK.
//
// This integration uses direct Go calls and does not execute the opencode binary.
func NewCustom() agent.Provider {
	return customProvider{}
}

type customProvider struct{}

func (customProvider) Name() string { return agent.ProviderCustom }

func (customProvider) New(opts agent.Options) (agent.Agent, error) {
	return &customAgent{opts: opts}, nil
}

type customAgent struct {
	opts agent.Options
}

func (a *customAgent) Name() string { return agent.ProviderCustom }

func (a *customAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *customAgent) Stream(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *customAgent) run(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if rt == nil {
		rt = agent.NewStandaloneRuntime(nil)
	}

	sdkOpts, err := a.buildSDKOptions(req)
	if err != nil {
		return nil, err
	}

	// 把 opencode 的工具权限请求委托给 gobrave 的 Runtime（策略 / 持久化 / UI）。
	sdkOpts.PermissionHandler = a.permissionHandler(rt, strings.TrimSpace(req.UserID))

	sdkClient, err := opencodesdk.NewClient(ctx, sdkOpts)
	if err != nil {
		return nil, fmt.Errorf("custom provider: init opencode sdk: %w", err)
	}
	defer func() { _ = sdkClient.Close() }()

	prompt := buildOpenCodePrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("custom provider: empty prompt")
	}

	seenToolCalls := map[string]map[string]bool{}
	seenToolResults := map[string]map[string]bool{}
	seenReasoningMessages := map[string]bool{}
	seenAssistantMessages := map[string]bool{}

	result, err := sdkClient.SendMessageStream(ctx, opencodesdk.RunRequest{
		SessionID:    strings.TrimSpace(req.SessionID),
		SessionTitle: "gobrave-opencode",
		Prompt:       prompt,
	}, func(event opencodesdk.Event) {
		if event.Message != nil {
			ensureSeenMaps(seenToolCalls, seenToolResults, event.Message.ID)
			emitToolEvents(ctx, rt, event.Message, seenToolCalls, seenToolResults)
			if event.Message.Role == opencodesdk.RoleAssistant && event.Message.Finished {
				messageID := strings.TrimSpace(event.Message.ID)
				// 思考内容随快照增量累积，Finished 后才是完整内容：这里补发“完整思考块”用于时间线持久化，
				// 与只做实时渲染、不落库的 reasoning_delta 事件区分开（对齐 copilot provider 的 reasoning 块）。
				// 纯思考消息（无文本）同样需要落库，因此该判断独立于下面的 message 块逻辑。
				if messageID != "" && !seenReasoningMessages[messageID] && strings.TrimSpace(event.Message.Thinking) != "" {
					_ = rt.Emit(ctx, agent.StreamEvent{
						Type: agent.StreamEventReasoning,
						Data: toReasoningBlock(*event.Message),
					})
					seenReasoningMessages[messageID] = true
				}
				// 文本内容已通过 text delta 实时下发，这里只补发“完整消息块”作为时间线锚点。
				// 对纯工具调用消息（无文本）则不重复发 message 块，避免下游出现空文本重复记录。
				if messageID != "" && strings.TrimSpace(event.Message.Text) != "" {
					if !seenAssistantMessages[messageID] {
						_ = rt.Emit(ctx, agent.StreamEvent{
							Type: agent.StreamEventMessage,
							Data: toAssistantMessageBlock(*event.Message),
						})
						seenAssistantMessages[messageID] = true
					}
				}
			}
		}

		switch event.Type {
		case opencodesdk.EventAssistantTextDelta:
			if event.Delta != "" {
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: event.Delta})
			}
		case opencodesdk.EventAssistantThinkDelta:
			if event.Delta != "" {
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoningDelta, Content: event.Delta})
			}
		case opencodesdk.EventAgentCompleted:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone})
		case opencodesdk.EventAgentCanceled:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventError, Err: event.Error})
		case opencodesdk.EventAgentError:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventError, Err: event.Error})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("custom provider: opencode run failed: %w", err)
	}

	return &agent.Result{Content: result.Message.Text}, nil
}

func (a *customAgent) buildSDKOptions(req agent.Request) (opencodesdk.Options, error) {
	workingDir := firstNonEmpty(strings.TrimSpace(req.WorkingDir), strings.TrimSpace(a.opts.WorkingDir))
	if workingDir == "" {
		return opencodesdk.Options{}, fmt.Errorf("custom provider: working dir is required")
	}

	model := firstNonEmpty(
		strings.TrimSpace(req.Model),
		strings.TrimSpace(a.opts.Extra["opencode_model"]),
	)
	if model == "" {
		return opencodesdk.Options{}, fmt.Errorf("custom provider: model is required")
	}

	apiKey := firstNonEmpty(a.modelAPIKey(model), strings.TrimSpace(a.opts.Extra["opencode_api_key"]))

	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = parseInt64(strings.TrimSpace(a.opts.Extra["opencode_max_tokens"]))
	}

	return opencodesdk.Options{
		WorkingDir:      workingDir,
		Debug:           isTrue(a.opts.Extra["opencode_debug"]),
		Provider:        strings.TrimSpace(a.opts.Extra["opencode_provider"]),
		APIKey:          apiKey,
		Model:           model,
		MaxTokens:       maxTokens,
		ReasoningEffort: strings.TrimSpace(a.opts.Extra["opencode_reasoning_effort"]),
	}, nil
}

// modelAPIKey 按模型 key 从模型提供商配置表解析 api_key。
func (a *customAgent) modelAPIKey(modelKey string) string {
	pc, ok := a.opts.Providers[strings.ToLower(strings.TrimSpace(modelKey))]
	if !ok {
		return ""
	}
	return strings.TrimSpace(pc.APIKey)
}

// permissionHandler 把 opencode SDK 的工具权限请求桥接到 gobrave 的 Runtime：
//
//	SDK 权限请求 → agent.Operation → rt.RequestPermission → 回译为 SDK 决策
//
// 与 copilot.go 的 permissionHandler 同构；错误一律映射为拒绝（安全默认）。
func (a *customAgent) permissionHandler(rt agent.Runtime, userID string) opencodesdk.PermissionHandler {
	return func(ctx context.Context, p opencodesdk.PermissionRequest) opencodesdk.PermissionDecision {
		op := permissionToOperation(p)
		decision, err := rt.RequestPermission(ctx, userID, op)
		if err != nil || decision != agent.DecisionAllow {
			return opencodesdk.PermissionDeny
		}
		return opencodesdk.PermissionAllow
	}
}

// permissionToOperation 把 opencode 的权限请求映射为框架内的 Operation 描述。
func permissionToOperation(p opencodesdk.PermissionRequest) agent.Operation {
	meta := map[string]any{"tool": p.ToolName}
	if p.Description != "" {
		meta["description"] = p.Description
	}
	// opencode 的 edit / write 工具会把 unified diff 放进权限请求的 params，
	// 透传到 Operation.Metadata，使用户能在批准前先看 diff（而非批准后看 Tool Result）。
	if diff, filePath := diffFromParams(p.Params); diff != "" {
		meta["diff"] = diff
		if filePath != "" {
			meta["file_path"] = filePath
		}
	}

	switch strings.ToLower(strings.TrimSpace(p.Action)) {
	case "execute":
		return agent.Operation{
			Type:     agent.OperationExecute,
			Command:  commandFromParams(p.Params, p.Description),
			Metadata: meta,
		}
	case "write", "create", "update", "edit":
		return agent.Operation{Type: agent.OperationWrite, Path: p.Path, Metadata: meta}
	case "delete":
		return agent.Operation{Type: agent.OperationDelete, Path: p.Path, Metadata: meta}
	case "read":
		return agent.Operation{Type: agent.OperationRead, Path: p.Path, Metadata: meta}
	case "fetch":
		return agent.Operation{Type: agent.OperationNetwork, Metadata: meta}
	default:
		return agent.Operation{Type: agent.OperationExecute, Metadata: meta}
	}
}

// commandFromParams 尽力从权限参数中抽取命令文本，失败时回退到描述。
func commandFromParams(params any, fallback string) string {
	if data, err := json.Marshal(params); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if cmd, ok := m["command"].(string); ok && cmd != "" {
				return cmd
			}
		}
	}
	return fallback
}

// diffFromParams 尽力从权限参数中抽取 file_path / diff（opencode edit / write 工具）。
//
// gobrave 无法 import opencode 的 internal tools 包做类型断言，因此与 commandFromParams
// 一样走 JSON 往返；字段名沿用 EditPermissionsParams / WritePermissionsParams 的 json tag。
func diffFromParams(params any) (diff, filePath string) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return "", ""
	}
	if d, ok := m["diff"].(string); ok {
		diff = d
	}
	if p, ok := m["file_path"].(string); ok {
		filePath = p
	}
	return diff, filePath
}

func isTrue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.EqualFold(trimmed, "1") || strings.EqualFold(trimmed, "true") || strings.EqualFold(trimmed, "yes")
}

func parseInt64(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func buildOpenCodePrompt(req agent.Request) string {
	parts := make([]string, 0, len(req.Messages)+2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		parts = append(parts, "[system]\n"+strings.TrimSpace(req.SystemPrompt))
	}
	for _, msg := range req.Messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s]\n%s", role, content))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func ensureSeenMaps(
	seenToolCalls map[string]map[string]bool,
	seenToolResults map[string]map[string]bool,
	messageID string,
) {
	if _, ok := seenToolCalls[messageID]; !ok {
		seenToolCalls[messageID] = map[string]bool{}
	}
	if _, ok := seenToolResults[messageID]; !ok {
		seenToolResults[messageID] = map[string]bool{}
	}
}

func emitToolEvents(
	ctx context.Context,
	rt agent.Runtime,
	msg *opencodesdk.MessageSnapshot,
	seenToolCalls map[string]map[string]bool,
	seenToolResults map[string]map[string]bool,
) {
	for _, call := range msg.ToolCalls {
		if !seenToolCalls[msg.ID][call.ID] {
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolCall, Data: agent.ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: call.Input,
			}})
			seenToolCalls[msg.ID][call.ID] = true
		}
	}
	for _, result := range msg.ToolResults {
		if !seenToolResults[msg.ID][result.ToolCallID] {
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolResult, Data: map[string]any{
				"tool_call_id": result.ToolCallID,
				"name":         result.Name,
				"content":      result.Content,
				"metadata":     result.Metadata,
				"is_error":     result.IsError,
			}})
			seenToolResults[msg.ID][result.ToolCallID] = true
		}
	}
}

// toReasoningBlock 将 OpenCode 的完整思考内容转换为框架内的完整思考块。
//
// 思考文本随快照增量累积，调用方须在消息 Finished 后再转换，以保证内容完整。
func toReasoningBlock(msg opencodesdk.MessageSnapshot) agent.ReasoningBlock {
	// SDK 未单独提供 reasoning id，沿用 message id 作为时间线锚点标识。
	return agent.ReasoningBlock{
		ID:      msg.ID,
		Content: msg.Thinking,
	}
}

func toAssistantMessageBlock(msg opencodesdk.MessageSnapshot) agent.MessageBlock {
	// Tool calls/results are emitted as dedicated stream events by emitToolEvents.
	// Keep message block text-only to avoid downstream duplicate tool timeline records.
	return agent.MessageBlock{
		ID:      msg.ID,
		Content: msg.Text,
	}
}
