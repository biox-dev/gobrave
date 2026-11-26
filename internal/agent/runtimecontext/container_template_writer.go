package runtimecontext

import "context"

// 本文件定义「容器模板写能力」端口，供 builtin 的
// create/update_container_template_spec 与 create/update_container_template_definition 使用。
//
// 与 ContainerTemplateBrowser（只读）分开：写操作会改库，属于需要授权的能力。
// 两层实体各有一个窄接口，工具只依赖自己用到的那一个：
//
//   - ContainerTemplateSpecWriter   —— 共享运行配置（「怎么跑」：命令 / 资源 / 端口 / ENV / 挂载）；
//   - ContainerTemplateDefinitionWriter —— 运行配置 × 镜像 的绑定行（「跑什么镜像」）。
//
// 由 container 的 projectResourceBrowser 实现，并在构造 builtin.Deps 时注入。

// ContainerTemplateSpecWriter 抽象「创建 / 更新共享运行配置」的写能力。
type ContainerTemplateSpecWriter interface {
	// CreateContainerTemplateSpec 新建一条共享运行配置（go_container_template_spec），
	// 返回带新主键的记录。
	CreateContainerTemplateSpec(ctx context.Context, in ContainerTemplateSpecCreateInput) (ContainerTemplateSpecRecord, error)

	// UpdateContainerTemplateSpec 按主键更新共享运行配置；in 中为 nil 的字段保持原值，
	// 返回更新后的记录。
	UpdateContainerTemplateSpec(ctx context.Context, in ContainerTemplateSpecUpdateInput) (ContainerTemplateSpecRecord, error)
}

// ContainerTemplateDefinitionWriter 抽象「创建 / 更新绑定行」的写能力。
//
// 绑定行主键即对外 ContainerTemplate.ID，也是 script.container_template_id /
// app_session.container_template_id 引用的值。
type ContainerTemplateDefinitionWriter interface {
	// CreateContainerTemplateDefinition 在已存在的运行配置下新建一条绑定行。
	// SpecID 必须指向已存在的 go_container_template_spec。
	CreateContainerTemplateDefinition(ctx context.Context, in ContainerTemplateDefinitionCreateInput) (ContainerTemplateDefinitionRecord, error)

	// UpdateContainerTemplateDefinition 按主键更新绑定行；in 中为 nil 的字段保持原值，
	// 返回更新后的记录。
	UpdateContainerTemplateDefinition(ctx context.Context, in ContainerTemplateDefinitionUpdateInput) (ContainerTemplateDefinitionRecord, error)
}

// ContainerTemplateSpecCreateInput 是「创建共享运行配置」的入参。
//
// Env / Mounts / SchedulingConstraint / Labels 使用 map / slice 承载 JSON 结构，
// 由实现序列化进 datatypes.JSON 列（与前端表单提交的 JSON 形状一致）：
//   - Env：{"KEY": "value", ...}；
//   - Mounts：[{source,target,mode,type,owner}, ...]；
//   - SchedulingConstraint：{constraints:[{type,key,operator,values}]}；
//   - Labels：{"k": "v", ...}。
type ContainerTemplateSpecCreateInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Command     string  `json:"command,omitempty"`
	CPU         float64 `json:"cpu,omitempty"`
	Memory      int64   `json:"memory,omitempty"`
	Port        int     `json:"port,omitempty"`
	AppType     string  `json:"app_type,omitempty"`

	Env                  map[string]any   `json:"env,omitempty"`
	Mounts               []map[string]any `json:"mounts,omitempty"`
	SchedulingConstraint map[string]any   `json:"scheduling_constraint,omitempty"`
	Labels               map[string]any   `json:"labels,omitempty"`
	ChangeUID            bool             `json:"change_uid,omitempty"`
}

// ContainerTemplateSpecUpdateInput 是「更新共享运行配置」的入参。
//
// 指针字段为 nil 表示不修改该字段；非 nil 时按值覆盖。
type ContainerTemplateSpecUpdateInput struct {
	// SpecID 是 go_container_template_spec 主键，必须为正数。
	SpecID int64 `json:"spec_id,string"`

	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	Command     *string  `json:"command,omitempty"`
	CPU         *float64 `json:"cpu,omitempty"`
	Memory      *int64   `json:"memory,omitempty"`
	Port        *int     `json:"port,omitempty"`
	AppType     *string  `json:"app_type,omitempty"`

	Env                  map[string]any   `json:"env,omitempty"`
	Mounts               []map[string]any `json:"mounts,omitempty"`
	SchedulingConstraint map[string]any   `json:"scheduling_constraint,omitempty"`
	Labels               map[string]any   `json:"labels,omitempty"`
	ChangeUID            *bool            `json:"change_uid,omitempty"`
}

// ContainerTemplateDefinitionCreateInput 是「创建绑定行」的入参。
type ContainerTemplateDefinitionCreateInput struct {
	// SpecID 是共享运行配置主键，必须指向已存在的 go_container_template_spec。
	SpecID int64 `json:"spec_id,string"`
	// ImageName 是完整镜像名（含 registry / tag），必填。
	ImageName string `json:"image_name"`
	// PullPolicy 取 Always / IfNotPresent / Never，空值按 IfNotPresent 处理。
	PullPolicy string `json:"pull_policy,omitempty"`
	// Name 是该绑定行（可运行模板）的展示名。
	Name string `json:"name,omitempty"`
	// Description 是该绑定行自身的长描述。
	Description string `json:"description,omitempty"`
}

// ContainerTemplateDefinitionUpdateInput 是「更新绑定行」的入参。
//
// 指针字段为 nil 表示不修改该字段；非 nil 时按值覆盖。
type ContainerTemplateDefinitionUpdateInput struct {
	// DefinitionID 是绑定行主键（对外 ContainerTemplate.ID），必须为正数。
	DefinitionID int64 `json:"definition_id,string"`

	SpecID     *int64  `json:"spec_id,omitempty"`
	ImageName  *string `json:"image_name,omitempty"`
	PullPolicy *string `json:"pull_policy,omitempty"`
	Name       *string `json:"name,omitempty"`
	// Description 非 nil 时按值覆盖（允许显式置为空串以清空描述）。
	Description *string `json:"description,omitempty"`
}

// ContainerTemplateSpecRecord 是共享运行配置写入后回传给模型的记录。
type ContainerTemplateSpecRecord struct {
	SpecID      int64   `json:"spec_id,string"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Command     string  `json:"command,omitempty"`
	CPU         float64 `json:"cpu,omitempty"`
	Memory      int64   `json:"memory,omitempty"`
	Port        int     `json:"port,omitempty"`
	AppType     string  `json:"app_type,omitempty"`

	Env                  map[string]any   `json:"env,omitempty"`
	Mounts               []map[string]any `json:"mounts,omitempty"`
	SchedulingConstraint map[string]any   `json:"scheduling_constraint,omitempty"`
	Labels               map[string]any   `json:"labels,omitempty"`
	ChangeUID            bool             `json:"change_uid"`
}

// ContainerTemplateDefinitionRecord 是绑定行写入后回传给模型的记录。
type ContainerTemplateDefinitionRecord struct {
	DefinitionID int64  `json:"definition_id,string"`
	SpecID       int64  `json:"spec_id,string"`
	ImageName    string `json:"image_name"`
	PullPolicy   string `json:"pull_policy,omitempty"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
}
