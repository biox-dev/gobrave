package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

// 技能工具的入参 / 出参定义。
//
// 技能本身仍以 SKILL.md 为唯一真实来源，但正文不再由模型「读文件」获取，而是通过
// 工具调用按需加载（渐进式披露）：
//
//   - get_skill：按名称（可选版本）加载 SKILL.md 的指令正文与必要元信息；
//   - load_skill_resource：加载技能目录内的附属资源（参考文档 / 脚本 / 模板 / Schema）；
//   - create_skill / edit_skill：创建技能，或按 old_string → new_string 局部修改技能。
//
// 前两者是只读工具，免授权（技能来自 $BaseDir/.skills，属于框架受控目录）；
// 后两者通过注入的 runtimecontext.SkillWriter 落盘并热更新注册表。
const (
	// GetSkillToolName 是「加载技能指令」工具名。
	GetSkillToolName = "get_skill"
	// LoadSkillResourceToolName 是「加载技能附属资源」工具名。
	LoadSkillResourceToolName = "load_skill_resource"
	// CreateSkillToolName 是「创建技能」工具名。
	CreateSkillToolName = "create_skill"
	// EditSkillToolName 是「编辑技能」工具名。
	EditSkillToolName = "edit_skill"
)

// GetSkillInput 是 get_skill 工具的入参。
type GetSkillInput struct {
	// Name 是技能名（即 SKILL.md frontmatter 的 name）。
	Name string `json:"name"`
	// Version 是可选的版本约束；当前只支持精确匹配（与 manifest.json /
	// frontmatter 的 version 比对），留空表示使用已发现的当前版本。
	Version string `json:"version,omitempty"`
}

// GetSkillOutput 是 get_skill 工具返回给模型的结果。
//
// 刻意不包含技能所在磁盘路径：技能目录布局固定为 <skills_root>/<name>（见 Store.SkillPath），
// 与 name 完全冗余；且技能正文/资源只能经 get_skill / load_skill_resource 加载，
// 回给模型一个绝对路径只会诱导它绕过这两个工具直接访问宿主文件系统。
// 需要展示路径的 HTTP 接口请用 AgentSkillDetail。
type GetSkillOutput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Version      string   `json:"version,omitempty"`
	Resources    []string `json:"resources,omitempty"`
	Instructions string   `json:"instructions"`
	Error        string   `json:"error,omitempty"`
}

// LoadSkillResourceInput 是 load_skill_resource 工具的入参。
type LoadSkillResourceInput struct {
	// Name 是技能名。
	Name string `json:"name"`
	// Path 是相对技能目录的资源路径，如 references/design.md。
	Path string `json:"path"`
	// Version 语义同 get_skill.Version（可选）。
	Version string `json:"version,omitempty"`
}

// LoadSkillResourceOutput 是 load_skill_resource 工具返回给模型的结果。
//
// 与 GetSkillOutput 同理，不回传绝对路径：调用方只需要 name + 相对 path 就能再次定位资源。
type LoadSkillResourceOutput struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CreateSkillInput 是 create_skill 工具的入参。
type CreateSkillInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

// EditSkillInput 是 edit_skill 工具的入参。
type EditSkillInput struct {
	Name      string `json:"name"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// SkillWriteOutput 是 create_skill / edit_skill 工具返回给模型的结果。
type SkillWriteOutput struct {
	Name      string `json:"name"`
	SkillPath string `json:"skill_path,omitempty"`
	SkillFile string `json:"skill_file,omitempty"`
	// Notice 是写入成功后需要转告用户的提醒（如内置技能会被下次启动覆盖）。
	Notice string `json:"notice,omitempty"`
	Error  string `json:"error,omitempty"`
}

// GetSkill 返回「按名称加载技能指令」工具。
func GetSkill(resolver runtimecontext.SkillResolver) tool.Tool {
	return tool.NewFunc(
		GetSkillToolName,
		"Load a skill's full instructions (the body of its SKILL.md) by name. When the user's request matches an available skill's description, call this first and follow the returned instructions. Returns instructions plus the skill description, version and the list of bundled resources (read them with load_skill_resource).",
		tool.Schema("加载技能指令的入参", map[string]any{
			"name":    tool.StringProperty("技能名称，必须与系统提示词 available_skills 中的 name 完全一致。"),
			"version": tool.StringProperty("可选：技能版本（精确匹配）。留空表示使用当前已安装版本。"),
		}, "name"),
		func(ctx context.Context, in GetSkillInput) (GetSkillOutput, error) {
			return ResolveSkillOutput(ctx, resolver, in.Name, in.Version)
		},
	)
}

// ResolveSkillOutput 是「按名称获取技能内容」的单一实现（get_skill 工具与 HTTP 接口共用）。
//
// 它解析技能并用 ctx 中注入的 RuntimeContext 变量渲染指令正文，保证工具执行期与
// 前端 / 调试接口看到的技能内容一致。
//
// 返回 error 仅表示调用方环境缺失（如未注入 SkillResolver）；技能不存在、版本不匹配、
// 渲染失败等业务错误通过 GetSkillOutput.Error 返回（与工具反馈给模型的口径一致）。
func ResolveSkillOutput(ctx context.Context, resolver runtimecontext.SkillResolver, name, version string) (GetSkillOutput, error) {
	if resolver == nil {
		return GetSkillOutput{}, fmt.Errorf("%s: skill registry is not available in this runtime", GetSkillToolName)
	}

	s, err := resolveSkill(resolver, name, version)
	if err != nil {
		return GetSkillOutput{Name: strings.TrimSpace(name), Error: err.Error()}, nil
	}

	info := s.SkillInfo()

	// SKILL.md 正文同样是 pongo2 模板：用本轮 RuntimeContext 的业务变量渲染，
	// 使技能能按当前场景（如 workspace_directory / analysis_node_id）生成差异化指令。
	// frontmatter 的 disable-render: true 会关闭该渲染，按原文返回。
	instructions, err := renderSkillTemplate(ctx, info.Instructions, info.DisableRender)
	if err != nil {
		return GetSkillOutput{Name: info.Name, Error: err.Error()}, nil
	}

	return GetSkillOutput{
		Name:         info.Name,
		Description:  info.Description,
		Version:      s.SkillVersion(),
		Resources:    s.SkillResources(),
		Instructions: instructions,
	}, nil
}

// AgentSkillDetail 是 HTTP 接口（GET /agent/skill/resolve）返回给前端的技能详情。
//
// 它在 GetSkillOutput 之上补充了仅对「人」有意义的磁盘路径：前端详情弹窗用它
// 展示技能所在目录。工具（get_skill）不返回该字段，避免模型拿到路径后绕过
// get_skill / load_skill_resource 去直接读文件。
type AgentSkillDetail struct {
	GetSkillOutput
	// SkillPath 是技能目录的绝对路径（<skills_root>/<name>）。
	SkillPath string `json:"skill_path,omitempty"`
}

// LoadSkillResource 返回「加载技能附属资源」工具。
//
// 用于读取参考文档、脚本源码、模板、Schema 等附属文件，而不是再次读取完整的技能
// 指令（那应该用 get_skill）。只允许访问技能目录内部的文件。
func LoadSkillResource(resolver runtimecontext.SkillResolver) tool.Tool {
	return tool.NewFunc(
		LoadSkillResourceToolName,
		"Load a bundled resource of a skill: reference documents, script sources, templates, JSON schemas, etc. Use it instead of re-reading the full skill instructions. `path` must be relative to the skill directory (e.g. references/design.md, scripts/run_analysis.R, templates/params.schema.json).",
		tool.Schema("加载技能附属资源的入参", map[string]any{
			"name":    tool.StringProperty("技能名称，必须与系统提示词 available_skills 中的 name 完全一致。"),
			"path":    tool.StringProperty("相对技能目录的资源路径，如 references/design.md。"),
			"version": tool.StringProperty("可选：技能版本（精确匹配）。留空表示使用当前已安装版本。"),
		}, "name", "path"),
		func(ctx context.Context, in LoadSkillResourceInput) (LoadSkillResourceOutput, error) {
			if resolver == nil {
				return LoadSkillResourceOutput{}, fmt.Errorf("%s: skill registry is not available in this runtime", LoadSkillResourceToolName)
			}

			out := LoadSkillResourceOutput{
				Name: strings.TrimSpace(in.Name),
				Path: normalizeResourcePath(in.Path),
			}

			s, err := resolveSkill(resolver, in.Name, in.Version)
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}

			if strings.EqualFold(out.Path, SkillFileName) {
				out.Error = "SKILL.md is the skill instruction itself: use get_skill instead of load_skill_resource"
				return out, nil
			}

			content, err := s.ReadSkillResource(out.Path)
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}

			// 附属资源（参考文档 / 脚本 / 模板等）按同一套 pongo2 变量渲染，
			// 与 get_skill 的指令正文保持一致；技能关闭渲染时则原样返回。
			content, err = renderSkillTemplate(ctx, content, s.SkillInfo().DisableRender)
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}

			out.Name = s.SkillInfo().Name
			out.Content = content
			return out, nil
		},
	)
}

// CreateSkill 返回「创建技能」工具。
//
// 与 coder 的 write 工具定位一致：一次写入完整技能（frontmatter 由 Skill.Marshal 生成，
// 入参只提供 name / description / instructions），适合从零新建；只改局部内容请用 edit_skill。
func CreateSkill(writer runtimecontext.SkillWriter) tool.Tool {
	return tool.NewFunc(
		CreateSkillToolName,
		"Create a new agent skill: a SKILL.md file (YAML name/description frontmatter + markdown instructions body) stored under the skills directory and registered immediately. Use it when the user asks to create / save a reusable skill or workflow. The name must be unique and use only letters, digits and hyphens (no leading/trailing/consecutive hyphens). To change an existing skill, use edit_skill instead.",
		tool.Schema("创建技能的入参", map[string]any{
			"name":         tool.StringProperty("技能名：字母数字与连字符，不能与已有技能重名（同时作为技能目录名）。"),
			"description":  tool.StringProperty("技能描述：会注入系统提示词，模型据此判断何时加载该技能。"),
			"instructions": tool.StringProperty("SKILL.md 的 markdown 正文（不含 YAML frontmatter）。"),
		}, "name", "description", "instructions"),
		func(ctx context.Context, in CreateSkillInput) (SkillWriteOutput, error) {
			if writer == nil {
				return SkillWriteOutput{}, fmt.Errorf("%s: skill store is not available in this runtime", CreateSkillToolName)
			}
			// 先算计划：校验入参并生成待写入的 SKILL.md（不落盘），供授权前展示 diff。
			plan, err := writer.PlanCreate(runtimecontext.SkillCreateInput{
				Name:         in.Name,
				Description:  in.Description,
				Instructions: in.Instructions,
			})
			if err != nil {
				return SkillWriteOutput{Name: strings.TrimSpace(in.Name), Error: err.Error()}, nil
			}
			// 写技能会落盘并改变后续提示词，与 coder 的 write / edit 一致：先请求写授权（附 diff）。
			if err := tool.RequirePermission(ctx, tool.PermissionRequest{
				ToolName:    CreateSkillToolName,
				Action:      "write",
				Description: fmt.Sprintf("Create skill %s", plan.Name),
				Params:      skillDiffPreview(plan),
			}); err != nil {
				return SkillWriteOutput{}, err
			}
			info, err := writer.Apply(plan)
			if err != nil {
				return SkillWriteOutput{Name: strings.TrimSpace(in.Name), Error: err.Error()}, nil
			}
			return newSkillWriteOutput(info, plan.Notice), nil
		},
	)
}

// EditSkill 返回「编辑技能」工具。
//
// 语义与 coder 的 edit 工具一致：按技能名定位 SKILL.md，用 old_string → new_string
// 做一次唯一文本替换。调用前应先用 get_skill 读取技能全文，确保 old_string 精确匹配。
func EditSkill(writer runtimecontext.SkillWriter) tool.Tool {
	return tool.NewFunc(
		EditSkillToolName,
		"Edit an existing agent skill by replacing text in its SKILL.md. Read the skill first with get_skill, then pass a name plus an old_string that appears exactly once in the file (include enough surrounding context) and the new_string to replace it with. The edited SKILL.md is re-validated before being saved and the registry is refreshed immediately.",
		tool.Schema("编辑技能的入参", map[string]any{
			"name":       tool.StringProperty("技能名，必须与 available_skills 中的 name 完全一致。"),
			"old_string": tool.StringProperty("要替换的原文，必须在 SKILL.md 中唯一出现（含空白与换行需完全一致）。"),
			"new_string": tool.StringProperty("替换后的文本。"),
		}, "name", "old_string", "new_string"),
		func(ctx context.Context, in EditSkillInput) (SkillWriteOutput, error) {
			if writer == nil {
				return SkillWriteOutput{}, fmt.Errorf("%s: skill store is not available in this runtime", EditSkillToolName)
			}
			// 先算计划：读取当前 SKILL.md 并得到替换后的内容（不落盘），供授权前展示 diff。
			plan, err := writer.PlanEdit(in.Name, in.OldString, in.NewString)
			if err != nil {
				return SkillWriteOutput{Name: strings.TrimSpace(in.Name), Error: err.Error()}, nil
			}
			// 写技能会落盘并改变后续提示词，与 coder 的 write / edit 一致：先请求写授权（附 diff）。
			if err := tool.RequirePermission(ctx, tool.PermissionRequest{
				ToolName:    EditSkillToolName,
				Action:      "write",
				Description: fmt.Sprintf("Edit skill %s", plan.Name),
				Params:      skillDiffPreview(plan),
			}); err != nil {
				return SkillWriteOutput{}, err
			}
			info, err := writer.Apply(plan)
			if err != nil {
				return SkillWriteOutput{Name: strings.TrimSpace(in.Name), Error: err.Error()}, nil
			}
			return newSkillWriteOutput(info, plan.Notice), nil
		},
	)
}

// newSkillWriteOutput 把技能视图映射为工具结果。
//
// notice 为写入计划携带的提醒（可为空），会原样返回给模型，由模型转告用户。
func newSkillWriteOutput(info runtimecontext.SkillInfo, notice string) SkillWriteOutput {
	return SkillWriteOutput{
		Name:      info.Name,
		SkillPath: info.SkillPath,
		SkillFile: info.SkillFile,
		Notice:    notice,
	}
}

// skillDiffPreview 把写入计划转为授权预览：file_path / diff 与 coder edit / write 的
// 授权参数同键（tool.PermissionPreview），上层会放进 Operation.Metadata 供前端在
// 用户批准前渲染 unified diff。
func skillDiffPreview(plan runtimecontext.SkillWritePlan) tool.PermissionPreview {
	text, _, _ := diff.GenerateDiff(plan.Before, plan.After, plan.FilePath)
	return tool.PermissionPreview{FilePath: plan.FilePath, Diff: text}
}

// resolveSkill 按名称（与可选版本）解析技能。
func resolveSkill(resolver runtimecontext.SkillResolver, name, version string) (runtimecontext.SkillResource, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	s, ok := resolver.Get(name)
	if !ok {
		names := resolver.Names()
		sort.Strings(names)
		if len(names) == 0 {
			return nil, fmt.Errorf("skill %q not found: no skills are available", name)
		}
		return nil, fmt.Errorf("skill %q not found; available skills: %s", name, strings.Join(names, ", "))
	}

	version = strings.TrimSpace(version)
	if version == "" {
		return s, nil
	}
	// 当前布局为每个技能一个目录，磁盘上只保留一份版本；因此 version 只能做精确校验。
	if got := s.SkillVersion(); got != version {
		return nil, fmt.Errorf("skill %q version %q is not available (current version: %q)", name, version, got)
	}
	return s, nil
}

// renderSkillTemplate 用本轮业务变量渲染技能文本（SKILL.md 正文 / 附属资源）中的 pongo2 模板。
//
// 变量取自 runtimecontext.RuntimeContextFrom(ctx).SystemPromptVars —— 与 Agent Profile 系统提示词
// 使用同一套变量与同一份渲染实现（tool.RenderTemplate / agent.RenderSystemPromptTemplate）。
// 未注入 RuntimeContext 时沿用空变量，未定义的占位符按 pongo2 默认行为渲染为空串，
// 因此无业务上下文的场景不受影响。
//
// disabled 为 true（技能 frontmatter 的 disable-render: true）时跳过渲染，原样返回文本。
func renderSkillTemplate(ctx context.Context, text string, disabled bool) (string, error) {
	if disabled {
		return text, nil
	}
	return tool.RenderRuntimeTemplate(ctx, text)
}

// normalizeResourcePath 清理资源路径（统一分隔符、去掉前导 ./ 与 /）。
func normalizeResourcePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	return strings.TrimPrefix(p, "/")
}
