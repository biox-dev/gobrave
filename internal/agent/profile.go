package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 内置 Profile 使用固定的负数 ID，避免与雪花算法生成的正整数主键冲突。
// Profile 的查询 / 引用一律使用 ID；Name 仅用于展示。
const (
	BuiltinDefaultProfileID int64 = -1 // 内置默认 Profile
	BuiltinAnalysisCoderID  int64 = -2 // 内置「分析代码编写」Profile
	BuiltinArticleWriterID  int64 = -3 // 内置「科研文章撰写」Profile
	BuiltinSummaryProfileID int64 = -4 // 内置「摘要总结」Profile
)

// BuiltinDefaultProfileName 是内置默认 Profile 的展示名。用于无需查询数据库即可获得
// 展示名的场景（如 DefaultUserAgentConfig）。
const BuiltinDefaultProfileName = "通用助手"

// Profile 相关错误。
var (
	// ErrProfileNotFound 表示 Profile 不存在。
	ErrProfileNotFound = errors.New("agent: profile not found")
	// ErrProfileNameRequired 表示 Profile 名称缺失。
	ErrProfileNameRequired = errors.New("agent: profile name is required")
)

// ContextConfig 控制 AgentService 在调用前注入哪些背景。
//
//   - InjectMemory：检索长期记忆并拼进 SystemPrompt。
type ContextConfig struct {
	InjectMemory bool `json:"inject_memory"`
}

// Profile 描述一类任务的 Agent 配置（系统提示词 + 技能 + 上下文注入开关）。
//
// 与 Options / Request（每次调用的动态参数）不同，Profile 是「按任务类型选择」的静态
// 配置快照：HTTP 层或前端在发起调用时通过 Request.ProfileID 指定 Profile，AgentService
// 在调用前按 ID 解析并应用（合并系统提示词、过滤技能、按开关注入背景）。
//
// Name 仅用于展示；Profile 的查询 / 引用一律使用 ID。
// Profile 不再按用户隔离：所有 Profile 全局可见。
type Profile struct {
	ID int64 `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	// Name 仅作为展示名使用，不参与任何查询。
	Name         string `json:"name" gorm:"column:name;type:varchar(128)"`
	Description  string `json:"description" gorm:"column:description;type:text"`
	IsDefault    bool   `json:"is_default" gorm:"column:is_default"`
	IsBuiltin    bool   `json:"is_builtin" gorm:"column:is_builtin"`
	SystemPrompt string `json:"system_prompt" gorm:"column:system_prompt;type:text"`
	// Model 指定本 Profile 使用的模型名（对应 config.agent.providers 的 key）。
	// 为空时回退到请求级 req.Model（若仍未指定则由 Provider 兜底）。
	Model string `json:"model" gorm:"column:model;type:varchar(128)"`
	// Provider 指定本 Profile 使用的 Agent（Provider）名称（mock | claude_code | codex | copilot | custom）。
	// 为空时回退到请求级 req.Provider（若仍未指定则由 Client 的默认 Provider 兜底）。
	Provider string        `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Skills   []string      `json:"skills" gorm:"column:skills;serializer:json"`
	Context  ContextConfig `json:"context" gorm:"column:context;serializer:json"`

	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回 Profile 表的表名。
func (Profile) TableName() string { return "agent_profiles" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键（仅 ID 为 0 时）。
func (p *Profile) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = utils.GenerateID()
	}
	return nil
}

// BuiltinProfiles 返回框架内置 Profile 的种子数据。新增内置 Profile 时在此追加。
//
// 内置 Profile 仅作为数据库初始化工具：系统启动时由 EnsureBuiltinProfiles 按 ID
// 同步到数据库（不存在则新增、已存在则保留）。所有 Profile 的读取 / 写入 / 解析均
// 走数据库（ProfileRepository），内置 Profile 与用户自定义 Profile 统一存储。
func BuiltinProfiles() []*Profile {
	now := time.Now()
	return []*Profile{
		{
			ID:          BuiltinDefaultProfileID,
			Name:        BuiltinDefaultProfileName,
			Description: "默认配置：不附加领域提示词；注入记忆。",
			IsDefault:   true,
			IsBuiltin:   true,
			// SystemPrompt 为 pongo2 模板：调用方通过 RuntimeContext.SystemPromptVars
			// 提供变量，AgentService 渲染后写入真正的系统提示词。
			SystemPrompt: "{% if workspace_directory %}workspace_directory: {{ workspace_directory }}{% endif %}{% if analysis_node_id %}\n## Analysis Node ID: {{ analysis_node_id }}\n运行当前分析节点请调用 run_analysis_node 工具。{% endif %}",
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCopilot,
			Context:      ContextConfig{InjectMemory: true},
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:          BuiltinAnalysisCoderID,
			Name:        "分析代码编写",
			Description: "用于撰写生物信息学分析代码：强调代码规范、可复现性与执行约束。",
			IsBuiltin:   true,
			SystemPrompt: `你是一名生物信息学数据分析工程师。请输出规范、可复现的分析代码，明确依赖与输入输出；如需执行，优先使用运行时提供的工具，而不是直接调用 shell / Rscript / python 等命令。
{% if workspace_directory %}workspace_directory: {{ workspace_directory }}{% endif %}{% if analysis_node_id %}
## Analysis Node ID: {{ analysis_node_id }}
运行当前分析节点请调用 run_analysis_node 工具（analysis_node_id 可留空以运行当前节点）；该工具会等待节点完成后返回执行日志。{% endif %}{% if script_file_path %}
script_file_path: {{ script_file_path }}
params_file_path: {{ params_file_path }}
io_schema_path: {{ io_schema_path }}
output_directory: {{ output_directory }}{% endif %}{% if script_workspace_directory %}
script_workspace_directory: {{ script_workspace_directory }}{% endif %}

## 创建脚本工作空间的固定流程

当用户要求「新建一个分析脚本 / 分析节点」并基于数据文件跑出结果时，必须按以下顺序推进，
并且**一次任务内走完整条流程**：从创建脚本开始，一直到 run_analysis_node 返回 success 为止。

1) create_script → 得到 script_file_path / io_schema_path / analysis_node_id。
   - 调用时一次性给出 inputs / outputs（真实的参数契约），避免创建后再补齐。
   - io_schema_path 是参数契约的唯一来源：后续 request_param 的键、params.json 的键、
     脚本内读取的参数名，全部以它为准，不要自行发明参数名。
2) 先确认契约：结合用户需求与 read_file_by_id 拿到的数据结构（列名 / 维度 / 分组字段），
   把 io_schema.json 的 inputs / outputs 改成真实契约。
   - inputs 的 name 就是 params.json 里的键，也是脚本里取参数用的名字；
   - 输出项按实际产物命名（通常 type 为 file）；
   - io_schema.json 也用于给人类用户渲染表单，因此 label / description 要写清楚含义。
3) 再写脚本主体（写入 script_file_path）：
   - 从 params.json 读取输入，读取的键名必须与 io_schema inputs 的 name 完全一致；
   - 产物写入 params.json 中的 output_dir，命名与 io_schema outputs 对应；
   - 不要硬编码路径、样本名或分组，一律通过参数传入。
4) build_analysis_node_params：按 io_schema inputs 的 name 组织 request_param（键 = name），
   把参数保存到分析节点并生成 params.json。若返回 warnings（缺必填项、存在 io_schema
   未声明的键），先修正再继续。
5) run_analysis_node 运行并查看日志；失败时必须继续迭代：读日志定位原因 → 改脚本或改参数
   → 重新 build_analysis_node_params（参数有变化时）→ 再次 run_analysis_node，
   直到返回 success=true 才算完成本次任务。

约束：
- 任务未完成的标准只有一个：run_analysis_node 返回 success=true 且产物符合 io_schema outputs。
  不得在脚本尚未跑通时结束任务，也不得把「脚本已写好」「节点已创建」当作完成。
- 若失败原因在可用信息范围内无法确定（需求歧义、缺少必要数据/参数/环境、多种方案取舍、
  create_script 报错提示需要用户配置容器模板等），调用 ask_user 向用户提问，并在拿到答复后
  继续执行流程，不要在等待用户输入时提前收尾。
- ask_user 使用要点：questions[].id 用稳定的英文短标识（如 analysis_type、group_column）；
  选项类问题用 single_choice / multiple_choice 并给出 2-5 个 option（label + value），
  必要时设置 allow_custom=true；纯信息采集用 text 并给出 placeholder；
  能一次问清的问题合并到同一次 ask_user 调用中，避免反复打断用户。
- run_analysis_node 可选传入 request_param：传了就以它为本次运行参数（并回写节点、重新生成 params.json）；不传则读取节点上已保存的参数。两种情况下都会按最新 io_schema 重新解析，并刷新 inputs_patterns / output_patterns / resolved_inputs / request_param、清空上一轮校验错误。
- 需要「先校验参数、确认 params.json 映射」时，仍优先调用 build_analysis_node_params；只是临时换一组参数跑一次时，可直接把 request_param 传给 run_analysis_node。
- 因此禁止在未调用 build_analysis_node_params（节点上无参数）且未给 run_analysis_node 传 request_param 的情况下直接运行节点。
- 修改 io_schema.json 的 inputs / outputs 后，必须重新调用 build_analysis_node_params，再运行。
- 每次运行只改必要的地方：先用日志定位失败原因，避免整段重写脚本。
- 需要完整日志时读取 run_analysis_node 返回的 log_path。`,
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			UpdatedAt: now,
		},
		{
			ID:          BuiltinArticleWriterID,
			Name:        "科研文章撰写",
			Description: "用于撰写科研报告 / 文章：按学术规范写作。",
			IsBuiltin:   true,
			SystemPrompt: `你是一名科研写作助手。请严格遵循科研报告 / 科研文章的学术规范撰写内容，语言严谨、结构完整，并确保结论与项目中的分析结果保持一致。{% if output_file %}
当前上下文是科研报告/科研文章撰写环境，请严格按照科研报告/科研文章的规范撰写内容，并将撰写的内容输出到指定的文件路径：{{ output_file }}，确保文件名为 output.md。
请不要在其他地方输出科研报告/科研文章的内容，必须严格遵守输出到指定文件的要求。{% endif %}{% if workspace_directory %}
workspace_directory: {{ workspace_directory }}{% endif %}`,
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			UpdatedAt: now,
		}, {
			ID:          BuiltinSummaryProfileID,
			Name:        "摘要总结",
			Description: "用于生成分析 / 节点的摘要：按学术规范写作。",
			IsBuiltin:   true,
			SystemPrompt: `你是一名生物信息学分析助手，请根据给定的分析输出内容，生成简洁、准确的中文摘要; 检查分析中存在的问题，并提出改进建议。{% if workspace_directory %}
workspace_directory: {{ workspace_directory }}{% endif %}{% if output_directory %}
output_directory: {{ output_directory }}{% endif %}{% if output_file %}
output_file: {{ output_file }}
Do not run analysis commands, mainly read the content of the analysis result file [{{ output_file }}] to complete the corresponding tasks.{% endif %}`,
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			UpdatedAt: now,
		},
	}
}

// IsBuiltinProfileID 判断给定 ID 是否为内置 Profile。
// 内置 Profile 使用固定的负数 ID，用户自定义 Profile 使用雪花算法生成的正整数 ID。
func IsBuiltinProfileID(id int64) bool {
	return id < 0
}

// EnsureBuiltinProfiles 把内置 Profile 同步到仓库（数据库初始化工具）。
//
// 系统启动时调用：按 ID 判断，记录不存在则新增、已存在则跳过，从而允许管理端
// 对内置 Profile 的修改在重启后得以保留。repo 为空时无操作。
func EnsureBuiltinProfiles(ctx context.Context, repo ProfileRepository) error {
	if repo == nil {
		return nil
	}
	for _, p := range BuiltinProfiles() {
		_, err := repo.Get(ctx, p.ID)
		switch {
		case err == nil:
			// 已存在则跳过：内置 Profile 允许被修改，启动时不再覆盖。
			continue
		case errors.Is(err, ErrProfileNotFound):
			if err := repo.Create(ctx, p); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// ProfileManager 负责 Profile 的解析、列表与增删改查编排。
//
// 内置 Profile 与用户自定义 Profile 均持久化在数据库（ProfileRepository）中，本管理器
// 只做统一视图的编排：所有解析 / 读取均按 ID 进行（Name 仅用于展示）。内置 Profile 与
// 用户自定义 Profile 统一存储。
type ProfileManager struct {
	repo ProfileRepository
}

// NewProfileManager 创建 Profile 管理器。
func NewProfileManager(repo ProfileRepository) *ProfileManager {
	return &ProfileManager{repo: repo}
}

// Resolve 按 ID 解析 Profile。
//
//   - id 为 0 时回退到内置默认 Profile；
//   - id 不存在时返回 ErrProfileNotFound。
func (m *ProfileManager) Resolve(ctx context.Context, id int64) (*Profile, error) {
	if id == 0 {
		id = BuiltinDefaultProfileID
	}
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// List 返回全部 Profile（内置 + 自定义，按名称升序）。
func (m *ProfileManager) List(ctx context.Context) ([]*Profile, error) {
	out, err := m.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get 按 ID 返回 Profile（全局可见）。
func (m *ProfileManager) Get(ctx context.Context, id int64) (*Profile, error) {
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// Save 创建或更新 Profile；IsDefault 为 true 时清除其他 Profile 的默认标记。
//
// 内置 Profile（IsBuiltin=true、负数 ID）也允许更新：由管理端修改后持久化，
// 启动时 EnsureBuiltinProfiles 不再覆盖其改动。
func (m *ProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	// Name 仅用于展示，不做名称规整，只校验非空。
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return ErrProfileNameRequired
	}

	now := time.Now()
	if p.ID == 0 {
		p.ID = utils.GenerateID()
		p.IsBuiltin = false
		p.CreatedAt = now
		p.UpdatedAt = now
		if p.IsDefault {
			if err := m.repo.ClearDefault(ctx, p.ID); err != nil {
				return err
			}
		}
		return m.repo.Create(ctx, p)
	}

	p.UpdatedAt = now
	if p.IsDefault {
		if err := m.repo.ClearDefault(ctx, p.ID); err != nil {
			return err
		}
	}
	return m.repo.Update(ctx, p)
}

// Delete 删除自定义 Profile；内置 Profile 不可删除。
func (m *ProfileManager) Delete(ctx context.Context, id int64) error {
	// 内置 Profile（负数 ID）不可删除。
	if IsBuiltinProfileID(id) {
		return ErrProfileNotFound
	}
	return m.repo.Delete(ctx, id)
}
