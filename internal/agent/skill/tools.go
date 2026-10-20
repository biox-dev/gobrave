package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// 技能工具的入参 / 出参定义。
//
// 技能本身仍以 SKILL.md 为唯一真实来源，但正文不再由模型「读文件」获取，而是通过
// 工具调用按需加载（渐进式披露）：
//
//   - get_skill：按名称（可选版本）加载 SKILL.md 的指令正文与必要元信息；
//   - load_skill_resource：加载技能目录内的附属资源（参考文档 / 脚本 / 模板 / Schema）。
//
// 两者都是只读工具，免授权（技能来自 $BaseDir/.skills，属于框架受控目录）。
const (
	// GetSkillToolName 是「加载技能指令」工具名。
	GetSkillToolName = "get_skill"
	// LoadSkillResourceToolName 是「加载技能附属资源」工具名。
	LoadSkillResourceToolName = "load_skill_resource"
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
type GetSkillOutput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Version      string   `json:"version,omitempty"`
	SkillPath    string   `json:"skill_path,omitempty"`
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
type LoadSkillResourceOutput struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	AbsPath string `json:"abs_path,omitempty"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GetSkill 返回「按名称加载技能指令」工具。
func GetSkill() tool.Tool {
	return tool.NewFunc(
		GetSkillToolName,
		"Load a skill's full instructions (the body of its SKILL.md) by name. When the user's request matches an available skill's description, call this first and follow the returned instructions. Returns instructions plus the skill description, version and the list of bundled resources (read them with load_skill_resource).",
		tool.Schema("加载技能指令的入参", map[string]any{
			"name":    tool.StringProperty("技能名称，必须与系统提示词 available_skills 中的 name 完全一致。"),
			"version": tool.StringProperty("可选：技能版本（精确匹配）。留空表示使用当前已安装版本。"),
		}, "name"),
		func(ctx context.Context, in GetSkillInput) (GetSkillOutput, error) {
			return ResolveSkillOutput(ctx, tool.SkillResolverFrom(ctx), in.Name, in.Version)
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
func ResolveSkillOutput(ctx context.Context, resolver tool.SkillResolver, name, version string) (GetSkillOutput, error) {
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
		SkillPath:    info.SkillPath,
		Resources:    s.SkillResources(),
		Instructions: instructions,
	}, nil
}

// LoadSkillResource 返回「加载技能附属资源」工具。
//
// 用于读取参考文档、脚本源码、模板、Schema 等附属文件，而不是再次读取完整的技能
// 指令（那应该用 get_skill）。只允许访问技能目录内部的文件。
func LoadSkillResource() tool.Tool {
	return tool.NewFunc(
		LoadSkillResourceToolName,
		"Load a bundled resource of a skill: reference documents, script sources, templates, JSON schemas, etc. Use it instead of re-reading the full skill instructions. `path` must be relative to the skill directory (e.g. references/design.md, scripts/run_analysis.R, templates/params.schema.json).",
		tool.Schema("加载技能附属资源的入参", map[string]any{
			"name":    tool.StringProperty("技能名称，必须与系统提示词 available_skills 中的 name 完全一致。"),
			"path":    tool.StringProperty("相对技能目录的资源路径，如 references/design.md。"),
			"version": tool.StringProperty("可选：技能版本（精确匹配）。留空表示使用当前已安装版本。"),
		}, "name", "path"),
		func(ctx context.Context, in LoadSkillResourceInput) (LoadSkillResourceOutput, error) {
			resolver := tool.SkillResolverFrom(ctx)
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

			content, abs, err := s.ReadSkillResource(out.Path)
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
			out.AbsPath = abs
			out.Content = content
			return out, nil
		},
	)
}

// resolveSkill 按名称（与可选版本）解析技能。
func resolveSkill(resolver tool.SkillResolver, name, version string) (tool.SkillResource, error) {
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
// 变量取自 tool.RuntimeContextFrom(ctx).SystemPromptVars —— 与 Agent Profile 系统提示词
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
