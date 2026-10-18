package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
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

	userID := req.RequestUserID()
	// 把 opencode 的工具权限请求委托给 gobrave 的 Runtime（策略 / 持久化 / UI）。
	sdkOpts.PermissionHandler = a.permissionHandler(rt, userID)
	// 把 gobrave 的工具注册为 opencode 的宿主工具，与内置工具共用同一套 tool-call 循环。
	sdkOpts.Tools = a.buildHostTools(rt, req)

	sdkClient, err := opencodesdk.NewClient(ctx, sdkOpts)
	if err != nil {
		return nil, fmt.Errorf("custom provider: init opencode sdk: %w", err)
	}
	defer func() { _ = sdkClient.Close() }()

	runReq := buildRunRequest(req)
	if strings.TrimSpace(runReq.Prompt) == "" {
		return nil, fmt.Errorf("custom provider: empty prompt")
	}

	seenToolCalls := map[string]map[string]bool{}
	seenToolResults := map[string]map[string]bool{}
	seenReasoningMessages := map[string]bool{}
	seenAssistantMessages := map[string]bool{}

	result, err := sdkClient.SendMessageStream(ctx, runReq, func(event opencodesdk.Event) {
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

// modelAPIKey 返回本次调用模型对应的 api_key（未命中自定义提供商时为空）。
func (a *customAgent) modelAPIKey(modelKey string) string {
	pc := a.opts.ResolveProvider(modelKey)
	if pc == nil {
		return ""
	}
	return strings.TrimSpace(pc.APIKey)
}

// permissionHandler 把 opencode SDK 的工具权限请求桥接到 gobrave 的 Runtime：
//
//	SDK 权限请求 → agent.Operation → rt.RequestPermission → 回译为 SDK 决策
//
// 与 copilot.go 的 permissionHandler 同构；错误一律映射为拒绝（安全默认）。
func (a *customAgent) permissionHandler(rt agent.Runtime, userID int64) opencodesdk.PermissionHandler {
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
//
// Description 走 Operation 的一等字段（而非 Metadata），便于 UI 统一展示“这个操作
// 到底要做什么”——opencode 会填入如 "Execute command: ls -la"、
// "Replace content in file /a/b.go"、"Fetch content from URL: https://..." 等摘要。
// diff / file_path 仍放在 Metadata（仅 edit / write 才有，供批准前查看改动）。
func permissionToOperation(p opencodesdk.PermissionRequest) agent.Operation {
	meta := map[string]any{"tool": p.ToolName}
	// opencode 的 edit / write 工具会把 unified diff 放进权限请求的 params，
	// 透传到 Operation.Metadata，使用户能在批准前先看 diff（而非批准后看 Tool Result）。
	if diff, filePath := diffFromParams(p.Params); diff != "" {
		meta["diff"] = diff
		if filePath != "" {
			meta["file_path"] = filePath
		}
	}

	desc := strings.TrimSpace(p.Description)
	path := strings.TrimSpace(p.Path)
	if path == "" {
		// opencode 部分工具把路径放在 params 里（Path 为空），从 diff 参数中回填。
		if _, filePath := diffFromParams(p.Params); filePath != "" {
			path = filePath
		}
	}

	switch strings.ToLower(strings.TrimSpace(p.Action)) {
	case "execute":
		return agent.Operation{
			Type:        agent.OperationExecute,
			Command:     commandFromParams(p.Params, p.Description),
			Description: desc,
			Metadata:    meta,
		}
	case "write", "create", "update", "edit":
		return agent.Operation{Type: agent.OperationWrite, Path: path, Description: desc, Metadata: meta}
	case "delete":
		return agent.Operation{Type: agent.OperationDelete, Path: path, Description: desc, Metadata: meta}
	case "read":
		return agent.Operation{Type: agent.OperationRead, Path: path, Description: desc, Metadata: meta}
	case "fetch":
		return agent.Operation{Type: agent.OperationNetwork, Description: desc, Metadata: meta}
	case "analysis_node", "analysisnode":
		return agent.Operation{Type: agent.OperationAnalysisNode, Description: desc, Metadata: meta}
	default:
		return agent.Operation{Type: agent.OperationExecute, Description: desc, Metadata: meta}
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

// buildRunRequest 把 gobrave 请求转换为 opencode 结构化请求：
//
//   - SystemPrompt → RunRequest.System：走真正的 system 消息通道，不会作为 user 内容反复追加；
//   - 历史消息     → RunRequest.History：结构化 user / assistant 消息，保留 role 结构；
//   - 最后一条非空消息 → RunRequest.Prompt：本轮 user 输入，由 opencode 追加到会话末尾。
//
// 技能指令正文已由 AgentService 在 GetSystemSpromptByProfile 中注入 req.SystemPrompt，这里原样透传。
func buildRunRequest(req agent.Request) opencodesdk.RunRequest {
	type turn struct {
		role    string
		content string
	}
	turns := make([]turn, 0, len(req.Messages))
	for _, msg := range req.Messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		turns = append(turns, turn{role: msg.Role, content: content})
	}

	prompt := ""
	if len(turns) > 0 {
		prompt = turns[len(turns)-1].content
		turns = turns[:len(turns)-1]
	}

	history := make([]opencodesdk.InputMessage, 0, len(turns))
	for _, t := range turns {
		history = append(history, opencodesdk.InputMessage{Role: toSDKRole(t.role), Content: t.content})
	}

	ret := opencodesdk.RunRequest{
		SessionTitle: "gobrave-opencode",
		Prompt:       prompt,
		System:       strings.TrimSpace(req.SystemPrompt),
		History:      history,
	}
	// SessionID 在 agent.Request 中是 int64 会话主键，SDK 侧用字符串标识会话。
	if req.SessionID > 0 {
		ret.SessionID = strconv.FormatInt(req.SessionID, 10)
	}
	return ret
}

func toSDKRole(role string) opencodesdk.Role {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return opencodesdk.RoleAssistant
	case "tool":
		return opencodesdk.RoleTool
	case "system":
		return opencodesdk.RoleSystem
	default:
		return opencodesdk.RoleUser
	}
}

// buildHostTools 把 gobrave 的工具转换为 opencode 的宿主工具。
//
// opencode 自带完整的 tool-call 循环：模型发起调用后，opencode 会在其内部循环中调用
// 对应 Handler，再把结果回填给模型——Provider 无需自己驱动循环。
//
// 技能以两种方式参与：
//   - 元信息（name / description）由 AgentService 注入系统提示词（见
//     GetSystemSpromptByProfile），让模型知道有哪些技能可用；
//   - 技能的加载通过普通工具完成：get_skill / load_skill_resource 在 builtin.All() 中注册，
//     执行时从工具 ctx 取出技能注册表（a.opts.Skills），因此这里只负责注入注册表。
//
// 注意：工具调用 / 结果的时间线事件由 opencode 记录进消息流后经 emitToolEvents 统一发出，
// 因此这里只负责执行，不重复发事件（避免与快照事件重复落库）。
func (a *customAgent) buildHostTools(rt agent.Runtime, req agent.Request) []opencodesdk.HostTool {
	out := make([]opencodesdk.HostTool, 0)

	// 工具执行上下文 = 本轮业务事实（SystemPromptVars 等，来自请求）+ 进程级服务能力
	// （AnalysisNodeRunner，来自 Options）。二者分开注入：前者可序列化、随请求来，后者不可
	// 序列化、进程内复用（见 tool.WithAnalysisNodeRunner）。
	rc := agent.ToolRuntimeContextFromRequest(req)
	runner := a.opts.AnalysisNodeRunner
	fileReader := a.opts.FileReader
	scriptCreator := a.opts.ScriptCreator
	paramsBuilder := a.opts.AnalysisNodeParamsBuilder
	nodeUpdater := a.opts.AnalysisNodeUpdater
	envUpdater := a.opts.AgentEnvUpdater
	memorySaver := a.opts.MemorySaver
	browser := a.opts.ProjectResourceBrowser
	skills := a.opts.Skills

	if a.opts.Tools != nil {
		exec := tool.NewExecutor(a.opts.Tools)
		for _, def := range a.opts.Tools.List() {
			name := def.Name
			ht := hostTool(def.Name, def.Description, def.InputSchema,
				func(ctx context.Context, call opencodesdk.HostToolCall) opencodesdk.HostToolResult {
					ctx = agent.WithToolRuntime(ctx, rc, runner, fileReader, scriptCreator, paramsBuilder, nodeUpdater, envUpdater, memorySaver)
					ctx = agent.WithProjectResourceBrowser(ctx, browser)
					ctx = agent.WithSkills(ctx, skills)
					res := exec.Execute(ctx, tool.Call{ID: call.ID, Name: name, Arguments: json.RawMessage(call.Input)})
					return opencodesdk.HostToolResult{Content: res.Content, IsError: res.IsError}
				})
			// 工具自带权限元信息（tool.Definition.Permission）时，声明为 opencode 宿主工具的
			// 授权动作：执行前会走权限门禁 → SDK PermissionHandler → Runtime.RequestPermission。
			// 未声明的工具默认免授权（与内置只读工具语义一致）。
			if def.Permission != nil {
				ht = withPermission(ht, def.Permission.Action, def.Permission.PathFrom, def.Permission.Description)
			}
			out = append(out, ht)
		}
	}

	// ask_user：宿主工具，阻塞等待用户在 UI 作答后把答案回填给模型。
	out = append(out, a.askUserHostTool(rt, req.RequestUserID()))

	return out
}

// askUserHostTool 构建 ask_user 宿主工具：解析模型入参 → Runtime.AskUser 阻塞等待作答 →
// 把答案回填给模型。无 UI 时 Runtime 返回错误，转为失败结果。
func (a *customAgent) askUserHostTool(rt agent.Runtime, userID int64) opencodesdk.HostTool {
	def := agent.AskUserToolDefinition()
	return hostTool(def.Name, def.Description, def.InputSchema,
		func(ctx context.Context, call opencodesdk.HostToolCall) opencodesdk.HostToolResult {
			var in agent.AskUserInput
			if err := json.Unmarshal([]byte(call.Input), &in); err != nil {
				return opencodesdk.HostToolResult{Content: err.Error(), IsError: true}
			}
			answer, err := rt.AskUser(ctx, userID, in)
			if err != nil {
				return opencodesdk.HostToolResult{Content: err.Error(), IsError: true}
			}
			// Metadata 承载结构化答案（opencode 会以对象形式回填给模型，
			// 避免被二次序列化成 JSON 字符串导致模型“复述”该串并继续流式输出）。
			// Content 同时给出人类可读摘要，便于模型直接理解用户选择。
			return opencodesdk.HostToolResult{
				Content:  formatAskUserAnswer(in, answer),
				Metadata: answer,
			}
		})
}

// formatAskUserAnswer 把用户作答格式化为人类可读摘要（作为 ask_user 工具结果文本）。
//
// 按问题声明顺序逐题输出「问题标题 → 用户选择 / 输入」，让模型直接读懂作答内容；
// 结构化答案同时放在 HostToolResult.Metadata 中。
func formatAskUserAnswer(in agent.AskUserInput, answer agent.AskUserAnswer) string {
	byID := make(map[string]agent.QuestionAnswer, len(answer.Answers))
	for _, ans := range answer.Answers {
		if ans.ID != "" {
			byID[ans.ID] = ans
		}
	}

	var b strings.Builder
	if title := strings.TrimSpace(in.Title); title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}
	written := 0
	for _, q := range in.Questions {
		ans, ok := byID[q.ID]
		if !ok {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(q.Title))
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	// questions 未声明但作答中出现的答案（模型自定义 ID）原样追加。
	for _, ans := range answer.Answers {
		if _, known := byID[ans.ID]; !known {
			continue
		}
		declared := false
		for _, q := range in.Questions {
			if q.ID == ans.ID {
				declared = true
				break
			}
		}
		if declared {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(ans.ID)
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	if written == 0 {
		if data, err := json.Marshal(answer); err == nil {
			return string(data)
		}
		return "用户未作有效选择"
	}
	return b.String()
}

// formatQuestionAnswer 把单个答案拼接为文本（多选用逗号分隔，自定义输入用括号标注）。
func formatQuestionAnswer(ans agent.QuestionAnswer) string {
	parts := make([]string, 0, 3)
	parts = append(parts, ans.Values...)
	if value := strings.TrimSpace(ans.Value); value != "" {
		parts = append(parts, value)
	}
	text := strings.Join(parts, ", ")
	if custom := strings.TrimSpace(ans.Custom); custom != "" {
		// 自定义输入与选项值重复（选择题选了自定义）时只保留一次。
		if text == "" || text != custom {
			if text == "" {
				text = custom
			} else {
				text += "（自定义：" + custom + "）"
			}
		}
	}
	return text
}

// hostTool 组装单个 opencode 宿主工具：拆解入参 JSON Schema 并绑定执行回调。
func hostTool(name, description string, schema map[string]any, run func(ctx context.Context, call opencodesdk.HostToolCall) opencodesdk.HostToolResult) opencodesdk.HostTool {
	params, required := splitInputSchema(schema)
	return opencodesdk.HostTool{
		Name:        name,
		Description: description,
		Parameters:  params,
		Required:    required,
		Handler: func(ctx context.Context, call opencodesdk.HostToolCall) (opencodesdk.HostToolResult, error) {
			return run(ctx, call), nil
		},
	}
}

// withPermission 声明宿主工具的授权动作（链式），配置后该工具执行前会走
// opencode 的权限门禁 → SDK PermissionHandler → Runtime.RequestPermission。
func withPermission(t opencodesdk.HostTool, action, pathFrom, description string) opencodesdk.HostTool {
	t.Permission = &opencodesdk.HostToolPermission{
		Action:      action,
		PathFrom:    pathFrom,
		Description: description,
	}
	return t
}

// splitInputSchema 把 gobrave 的完整 object JSON Schema 拆分为 opencode 宿主工具所需的
// properties 映射与 required 列表（对齐 opencode 的 tools.ToolInfo 组织结构）。
//
// 空 schema 返回非 nil 的空值，避免部分 provider 收到 null 属性 / null required。
func splitInputSchema(schema map[string]any) (map[string]any, []string) {
	params := map[string]any{}
	required := []string{}
	if schema == nil {
		return params, required
	}
	if p, ok := schema["properties"].(map[string]any); ok {
		params = p
	}
	switch r := schema["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
	}
	return params, required
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
