package types

import (
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	PullPolicyAlways       = "Always"
	PullPolicyIfNotPresent = "IfNotPresent"
	PullPolicyNever        = "Never"
)

// ===== 持久化实体：共享的容器运行配置 =====

// ContainerTemplateSpec 只描述"怎么跑"：命令、资源、端口、环境变量、挂载、标签等，
// 不包含镜像。镜像由 ContainerTemplateDefinition 关联，因此同一套运行配置可被多个镜像复用。
//
// 这里刻意没有工作目录：容器的工作目录统一在创建时取运行时变量 PROJECT_DIR
// （DAG 节点任务型容器仍由 applyTaskRuntimeSpec 覆盖为节点工作区），
// 模板不再允许自定义 work_dir。
type ContainerTemplateSpec struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Name        string `json:"name" gorm:"type:varchar(255);not null;index"`
	Description string `json:"description" gorm:"type:text"`

	Command string `json:"command" gorm:"type:text"`

	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`

	Port    int    `json:"port" gorm:"not null;default:8787"`
	AppType string `json:"app_type" gorm:"type:varchar(32);index"`

	Env                  datatypes.JSON `json:"env" gorm:"type:json"`
	Mounts               datatypes.JSON `json:"mounts" gorm:"type:json"`
	SchedulingConstraint datatypes.JSON `json:"scheduling_constraint" gorm:"type:json"`
	Labels               datatypes.JSON `json:"labels" gorm:"type:json"`
	ChangeUID            bool           `json:"change_uid" gorm:"default:false"`

	// 指针类型是为了让导出可以显式清空（omitempty 也因此生效）；接口响应仍会带上时间字段。
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func (ContainerTemplateSpec) TableName() string {
	return "go_container_template_spec"
}

func (t *ContainerTemplateSpec) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// ===== 持久化实体：运行配置 × 镜像 绑定行 =====

// ContainerTemplateDefinition 一行即"一个可直接运行的容器模板"：共享配置 + 一个具体镜像。
// 镜像不再单独建表：完整镜像名与拉取策略直接放在绑定行上，因此一行就唯一确定了一个
// 可运行配置；同一 SpecID 可绑定多个不同镜像（(SpecID, ImageName) 组合唯一）。
// 注意：该表主键 ID 就是对外暴露的 ContainerTemplate.ID。
type ContainerTemplateDefinition struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SpecID int64 `json:"spec_id,string" gorm:"index;not null"`

	// ImageName 是完整镜像名（含 registry / namespace / tag），如 docker.io/rocker/rstudio:4.4。
	// 同一运行配置下不允许重复绑定同一镜像由服务层校验（不用唯一索引，便于旧数据迁移）。
	ImageName string `json:"image_name" gorm:"type:varchar(512);index"`

	// PullPolicy 是该绑定行的镜像拉取策略，取值 Always / IfNotPresent / Never，空值按 IfNotPresent 处理。
	// 落地方式由 runtime 决定：docker 在 create 前按策略决定是否 pull；kubernetes 直接映射为
	// container.imagePullPolicy。
	PullPolicy string `json:"pull_policy" gorm:"type:varchar(32);index;not null;default:IfNotPresent"`

	// DisplayName 为空时，对外名称回退到 ContainerTemplateConfig.Name。
	DisplayName string `json:"display_name" gorm:"type:varchar(255)"`

	// Description 是绑定行自身的描述（长文本），与共享运行配置 ContainerTemplateSpec.Description
	// 相互独立：spec 描述“怎么跑”，这里描述“这个可运行模板是做什么的 / 适用什么场景”。
	// Agent 工具 get_container_template 会把 DisplayName 与 Description 一起回传给模型。
	Description string `json:"description" gorm:"type:longtext"`

	// IsDefault 标记「默认容器模板」：只能有一行处于默认状态，由服务层在写入时互斥维护。
	// 创建脚本（create_script）未显式指定 container_template_id 时，优先取这一行。
	// 导出时会置为 false（omitempty 使其不出现在导出文件里）。
	IsDefault bool `json:"is_default,omitempty" gorm:"not null;default:false;index"`

	// 指针类型是为了让导出可以显式清空（omitempty 也因此生效）；接口响应仍会带上时间字段。
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func (ContainerTemplateDefinition) TableName() string {
	return "go_container_template_definition"
}

func (t *ContainerTemplateDefinition) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// ===== 非持久化读模型：对外容器模板 =====

// ContainerTemplate 不是持久化对象，由 repo 层读取
// go_container_template_definition + go_container_template_spec 组装返回。
// ID == ContainerTemplateDefinition.ID：引用方（script.container_template_id /
// app_session.container_template_id / container_instance.template_id）语义不变，
// 同时天然锁定了该模板要使用的镜像。
//
// 与 ContainerTemplateSpec 一样，这里没有 WorkDir：容器工作目录在创建时统一取
// 运行时变量 PROJECT_DIR。
type ContainerTemplate struct {
	ID     int64 `json:"id,string"`
	SpecID int64 `json:"spec_id,string"`

	Name        string `json:"name"`
	Description string `json:"description"`
	// ImageName / PullPolicy 原样取自绑定行 ContainerTemplateDefinition。
	ImageName  string `json:"image_name"`
	PullPolicy string `json:"pull_policy"`
	// DisplayName 原样取自绑定行 ContainerTemplateDefinition.DisplayName（不做回退）。
	// Name 才是对外展示名：DisplayName 非空时覆盖共享配置名，否则回退到 spec.Name。
	// 编辑表单必须读写 DisplayName，否则会把回退出来的配置名误固化成绑定行展示名。
	DisplayName string `json:"display_name"`
	// DefinitionDescription 原样取自绑定行 ContainerTemplateDefinition.Description（长文本）。
	// 与 Description（共享运行配置 spec 的描述）相互独立，编辑表单需读写本字段。
	DefinitionDescription string `json:"definition_description"`
	// IsDefault 原样取自绑定行 ContainerTemplateDefinition.IsDefault。
	IsDefault bool `json:"is_default"`

	Command string  `json:"command"`
	CPU     float64 `json:"cpu"`
	Memory  int64   `json:"memory"`

	Port    int    `json:"port"`
	AppType string `json:"app_type"`

	Env                  datatypes.JSON `json:"env"`
	Mounts               datatypes.JSON `json:"mounts"`
	SchedulingConstraint datatypes.JSON `json:"scheduling_constraint"`
	Labels               datatypes.JSON `json:"labels"`
	ChangeUID            bool           `json:"change_uid"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewContainerTemplate 把共享配置与绑定行组装成对外读模型。spec 允许为 nil（只取绑定行字段）。
// binding 为 nil 时返回 nil。
func NewContainerTemplate(spec *ContainerTemplateSpec, binding *ContainerTemplateDefinition) *ContainerTemplate {
	if binding == nil {
		return nil
	}

	tpl := &ContainerTemplate{
		ID:         binding.ID,
		SpecID:     binding.SpecID,
		ImageName:  binding.ImageName,
		PullPolicy: binding.PullPolicy,
		IsDefault:  binding.IsDefault,
	}
	// 历史数据可能没有拉取策略，空值统一按 IfNotPresent 对外暴露。
	if tpl.PullPolicy == "" {
		tpl.PullPolicy = PullPolicyIfNotPresent
	}
	if binding.CreatedAt != nil {
		tpl.CreatedAt = *binding.CreatedAt
	}
	if binding.UpdatedAt != nil {
		tpl.UpdatedAt = *binding.UpdatedAt
	}

	tpl.DisplayName = binding.DisplayName
	tpl.DefinitionDescription = binding.Description

	if spec != nil {
		tpl.Name = spec.Name
		tpl.Description = spec.Description
		tpl.Command = spec.Command
		tpl.CPU = spec.CPU
		tpl.Memory = spec.Memory
		tpl.Port = spec.Port
		tpl.AppType = spec.AppType
		tpl.Env = spec.Env
		tpl.Mounts = spec.Mounts
		tpl.SchedulingConstraint = spec.SchedulingConstraint
		tpl.Labels = spec.Labels
		tpl.ChangeUID = spec.ChangeUID

		if spec.UpdatedAt != nil && spec.UpdatedAt.After(tpl.UpdatedAt) {
			tpl.UpdatedAt = *spec.UpdatedAt
		}
	}

	if binding.DisplayName != "" {
		tpl.Name = binding.DisplayName
	}

	return tpl
}

// ContainerTemplateBundle 是容器模板的导出 / 导入格式，也是 script.json 与 workflow.json
// 里容器资产部分的结构本身 —— 三处的字段名与语义完全一致，统一在这一处定义：
//
//	container_template_specs       []ContainerTemplateSpec
//	container_template_definitions []ContainerTemplateDefinition
//
// 两者通过主键互相引用（definition.spec_id → spec.id），而 definition.id 就是脚本
// container_template_id 引用的值；镜像名与拉取策略直接挂在绑定行上，因此：
//   - 导出不写 created_at / updated_at（主键才是引用键，时间戳只会产生无意义的 git diff）；
//   - 导入按主键 upsert，顺序固定为「运行配置 → 绑定行」
//     （实现见 exportcodec.InstallContainerAssets，script / workflow 复用同一份逻辑）。
type ContainerTemplateBundle struct {
	ContainerTemplateSpecs       []ContainerTemplateSpec       `json:"container_template_specs"`
	ContainerTemplateDefinitions []ContainerTemplateDefinition `json:"container_template_definitions"`
}

// NewContainerTemplateBundle 返回空 bundle（切片非 nil，序列化为 [] 而不是 null）。
func NewContainerTemplateBundle() *ContainerTemplateBundle {
	return &ContainerTemplateBundle{
		ContainerTemplateSpecs:       make([]ContainerTemplateSpec, 0),
		ContainerTemplateDefinitions: make([]ContainerTemplateDefinition, 0),
	}
}

// ToExport 把对外读模型（ContainerTemplate = definition + spec）还原成统一平铺格式。
// spec 为 nil 时用读模型自身的字段重建（缺完整元数据时按主键再去库里取即可）。
func (t *ContainerTemplate) ToExport(spec *ContainerTemplateSpec) *ContainerTemplateBundle {
	if t == nil {
		return nil
	}
	if spec == nil {
		spec = t.ToTemplateSpec()
	}

	bundle := NewContainerTemplateBundle()
	// 导出格式不含时间字段与 is_default（默认模板是本地状态，不随资产导出），
	// 但展示名、镜像名与拉取策略属于资产本身，必须原样带出。
	bundle.ContainerTemplateDefinitions = append(bundle.ContainerTemplateDefinitions, ContainerTemplateDefinition{
		ID:          t.ID,
		SpecID:      t.SpecID,
		ImageName:   t.ImageName,
		PullPolicy:  t.PullPolicy,
		DisplayName: t.DisplayName,
		Description: t.DefinitionDescription,
	})
	if spec != nil {
		exportedSpec := *spec
		// 导出格式不含时间字段。
		exportedSpec.CreatedAt = nil
		exportedSpec.UpdatedAt = nil
		bundle.ContainerTemplateSpecs = append(bundle.ContainerTemplateSpecs, exportedSpec)
	}
	return bundle
}

// ToTemplateSpec 把读模型里平铺的运行配置字段还原成 ContainerTemplateSpec（主键取 SpecID）。
// 读模型没有配置的元数据（created_at 等），需要完整配置时请按 SpecID 单独读取。
func (t *ContainerTemplate) ToTemplateSpec() *ContainerTemplateSpec {
	if t == nil {
		return nil
	}
	return &ContainerTemplateSpec{
		ID:                   t.SpecID,
		Name:                 t.Name,
		Description:          t.Description,
		Command:              t.Command,
		CPU:                  t.CPU,
		Memory:               t.Memory,
		Port:                 t.Port,
		AppType:              t.AppType,
		Env:                  t.Env,
		Mounts:               t.Mounts,
		SchedulingConstraint: t.SchedulingConstraint,
		Labels:               t.Labels,
		ChangeUID:            t.ChangeUID,
	}
}

// AppSession 的状态机
// PENDING
//
//	↓
//
// CREATING
//
//	↓
//
// RUNNING
//
//	↓
//
// STOPPED
//
//	↓
//
// RESUMING
//
//	↓
//
// FAILED
type AppSession struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	UserID    int64 `json:"user_id,string" gorm:"type:bigint;index;not null"`
	ProjectID int64 `json:"project_id,string" gorm:"column:project_id;type:bigint"`

	// ProjectID string `json:"project_id" gorm:"type:varchar(36);index;not null"`

	AnalysisNodeID int64 `json:"analysis_node_id,string" gorm:"index"`

	// AppType string `json:"app_type" gorm:"type:varchar(32);index"`

	ContainerTemplateID int64 `json:"container_template_id,string" gorm:"index;not null"`

	Name string `json:"name" gorm:"type:varchar(255);not null"`

	Status string `json:"status" gorm:"type:varchar(32);index;not null;default:PENDING"`

	WorkspacePath string `json:"workspace_path" gorm:"type:varchar(1024)"`

	// AgentSessionID 记录创建该会话的 Agent 会话 ID（agent_sessions.id）。
	//
	// 0 表示用户从 UI 手动创建；>0 表示由 Agent 的 terminal_exec / 容器 coder bash 创建，
	// 同一 (container_template_id, agent_session_id) 唯一，保证同一 Agent 会话在同一模板上
	// 只复用一个终端会话（同一个容器与 PTY）。
	AgentSessionID int64 `json:"agent_session_id,string" gorm:"index;not null;default:0"`

	// Persistent 标记会话是否为「持久会话」。
	//
	// true  = 只能由空闲 TTL 到期或用户显式关闭（DELETE /container/terminal/session/{id}）回收；
	// false = 一次性会话：最后一个订阅者（terminal_exec / WS）离开后立即回收容器与会话。
	//
	// 用户从 UI 创建的会话一律持久；Agent 创建的一次性会话经前端打开后也会被置为 true，
	// 并且此后不会再退回 false（只增不减）。
	Persistent bool `json:"persistent" gorm:"not null;default:false"`

	// SessionKind 标记会话的「种类」，取值见 AppSessionKind*：
	//   - web      ：ContainerService.CreateAppSessionByTemplate（容器跑模板命令，前端经网关访问应用）；
	//   - terminal ：终端会话注册表 createOrAdopt（容器常驻，交互式 shell 由运行时 exec 打开）。
	//
	// 它描述会话本身，而不是「谁创建的」：创建者见 AgentSessionID，生命周期策略见 Persistent。
	// 历史行在加列之前就已存在，回填见 migrations/2026-11-06-app-session-kind；
	// 仍为空时读取方用 NormalizedSessionKind 兜底。
	SessionKind string `json:"session_kind" gorm:"column:session_kind;type:varchar(32);index;not null;default:web"`

	LastAccessAt *time.Time `json:"last_access_at"`

	StartedAt *time.Time `json:"started_at"`

	StoppedAt *time.Time `json:"stopped_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AppSessionPageQuery struct {
	AnalysisNodeID *int64 `json:"analysis_node_id,string,omitempty"`
	ProjectID      *int64 `json:"project_id,omitempty"`
}

func (AppSession) TableName() string {
	return "go_container_app_session"
}

func (t *AppSession) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type ContainerOwnerType string

const (
	ContainerOwnerDagNode    ContainerOwnerType = "dag_node"
	ContainerOwnerAppSession ContainerOwnerType = "app_session"
	ContainerOwnerService    ContainerOwnerType = "service"
	// ContainerOwnerTerminal 标识「容器模板终端会话」容器：只服务于交互式 PTY，
	// 不参与 DAG / AppSession 的容器生命周期编排。
	ContainerOwnerTerminal ContainerOwnerType = "terminal"
)

// ContainerInstanceKind 标识容器的「运行形态」：它决定 executeCreateInstance 如何组装
// 运行时 spec（启动命令、workload 类型、是否以宿主用户运行、是否暴露服务等）。
//
// 它与 ContainerOwnerType 是两个维度：OwnerType 说明「容器属于谁 / 用于回收」，
// Kind 说明「容器怎么跑」。例如终端会话容器的 owner 也是 app_session，
// 但 Kind 必须是 ContainerInstanceKindTerminal。
type ContainerInstanceKind string

const (
	// ContainerInstanceKindWeb 是 Web 应用容器：跑模板命令、暴露服务，
	// 由网关按 AppSession 转发流量（OwnerType=app_session）。
	ContainerInstanceKindWeb ContainerInstanceKind = "web"
	// ContainerInstanceKindTerminal 是终端会话容器：常驻 keep-alive 进程，
	// 真正的交互式 shell 由 docker exec / kubectl exec 打开（Sync=true 创建）。
	ContainerInstanceKindTerminal ContainerInstanceKind = "terminal"
	// ContainerInstanceKindDagNode 是 DAG 节点任务容器：统一入口 bash ./run.sh，
	// 跑完即退出（OwnerType=dag_node）。
	ContainerInstanceKindDagNode ContainerInstanceKind = "dag_node"
)

type ContainerStatus string

const (
	ContainerCreatePending   ContainerStatus = "create_pending"
	ContainerReCreatePending ContainerStatus = "recreate_pending"
	ContainerReCreating      ContainerStatus = "recreating"
	ContainerCreating        ContainerStatus = "creating"
	ContainerRunning         ContainerStatus = "running"
	ContainerPaused          ContainerStatus = "paused"
	ContainerStopPending     ContainerStatus = "stop_pending"
	ContainerStopping        ContainerStatus = "stopping"
	ContainerStartPending    ContainerStatus = "start_pending"
	ContainerStarting        ContainerStatus = "starting"
	ContainerStopped         ContainerStatus = "stopped"
	ContainerDeleted         ContainerStatus = "deleted"
	ContainerFailed          ContainerStatus = "failed"
	ContainerExited          ContainerStatus = "exited"
	ContainerDeletePending   ContainerStatus = "delete_pending"
	ContainerDeleting        ContainerStatus = "deleting"
)

type ContainerInstance struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	TemplateID int64 `json:"template_id,string" gorm:"index;not null"`

	OwnerType ContainerOwnerType `json:"owner_type" gorm:"type:varchar(30);index;not null"`

	OwnerID int64 `json:"owner_id,string" gorm:"index;not null"`

	// Kind 是容器的运行形态（取值见 ContainerInstanceKind*），决定创建时如何组装运行时
	// spec。它独立于 OwnerType：终端会话容器的 owner 也是 app_session。
	// 历史数据该列为空，读取方用 NormalizedKind 兜底（按 OwnerType 推断）。
	Kind ContainerInstanceKind `json:"kind" gorm:"column:kind;type:varchar(32);index;not null;default:web"`

	RuntimeName string `json:"runtime_name" gorm:"type:varchar(255);index;not null"`

	// NodeID *uint64 `gorm:"index"`

	RuntimeID string `json:"runtime_id" gorm:"type:varchar(255);index"`
	// docker id / pod uid

	Name string `json:"name" gorm:"type:varchar(255);not null;index"`

	Status ContainerStatus `json:"status" gorm:"type:varchar(30);index;not null;default:pending"`

	IPAddress       string `json:"ip_address" gorm:"type:varchar(128)"`
	RuntimeNodeName string `json:"runtime_node_name" gorm:"type:varchar(255);index"`

	// HostPort 是容器端口发布到宿主机后的实际端口（0 表示未发布）。
	// 只有 container.network_mode=host_port 时非 0；由运行时 Inspect 读回，
	// 供路由层用「宿主机 + HostPort」代替「容器 IP + 容器端口」。
	HostPort int `json:"host_port" gorm:"not null;default:0"`

	ExitCode *int `json:"exit_code"`

	StartedAt *time.Time `json:"started_at"`

	FinishedAt *time.Time `json:"finished_at"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (ContainerInstance) TableName() string {
	return "go_container_instance"
}

func (t *ContainerInstance) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	if t.Kind == "" {
		t.Kind = t.NormalizedKind()
	}
	return nil
}

// NormalizedKind 返回实例的运行形态：Kind 已显式设置时直接返回，否则按 OwnerType
// 推断（历史数据的兜底），最终默认 web。
func (t *ContainerInstance) NormalizedKind() ContainerInstanceKind {
	if t == nil {
		return ContainerInstanceKindWeb
	}
	if kind := NormalizeContainerInstanceKind(string(t.Kind)); kind != "" {
		return kind
	}
	switch t.OwnerType {
	case ContainerOwnerDagNode:
		return ContainerInstanceKindDagNode
	case ContainerOwnerTerminal:
		return ContainerInstanceKindTerminal
	default:
		return ContainerInstanceKindWeb
	}
}

// NormalizeContainerInstanceKind 校验并归一化容器运行形态；空值或未知取值返回 ""。
func NormalizeContainerInstanceKind(raw string) ContainerInstanceKind {
	switch ContainerInstanceKind(strings.ToLower(strings.TrimSpace(raw))) {
	case ContainerInstanceKindWeb:
		return ContainerInstanceKindWeb
	case ContainerInstanceKindTerminal:
		return ContainerInstanceKindTerminal
	case ContainerInstanceKindDagNode:
		return ContainerInstanceKindDagNode
	default:
		return ""
	}
}

type ContainerEvent struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	ContainerInstanceID int64 `json:"container_instance_id,string" gorm:"index;not null"`

	Event string `json:"event" gorm:"type:varchar(64);index;not null"`

	Message string `json:"message" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
}

func (ContainerEvent) TableName() string {
	return "go_container_event"
}

func (t *ContainerEvent) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type GatewayRoute struct {
	RouteKey string `json:"route_key" gorm:"primaryKey;type:varchar(255)"`

	ContainerInstanceID int64 `json:"container_instance_id,string" gorm:"index"`

	PathPrefix   string `json:"path_prefix" gorm:"type:varchar(512);not null;uniqueIndex"`
	IsTrimPrefix *bool  `json:"is_trim_prefix" gorm:"not null;default:true"`
	BackendHost  string `json:"backend_host" gorm:"type:varchar(255);not null"`
	BackendPort  int    `json:"backend_port" gorm:"not null"`

	Metadata datatypes.JSON `json:"metadata" gorm:"type:json"`

	// RuntimeName string `json:"runtime_name" gorm:"type:varchar(255);index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (GatewayRoute) TableName() string {
	return "go_gateway_route"
}

type ContainerSpec struct {
	Image                string
	Entrypoint           []string
	Command              []string
	Env                  map[string]string
	Volumes              []ContainerVolume
	SupplementalGroups   []int64
	SchedulingConstraint *ContainerSchedulingSelector
	User                 string
	Labels               map[string]string

	// HomeDir 非空时作为容器的 HOME（容器内绝对路径），由 runtime 落到环境变量
	// HOME 上（docker: -e HOME=...，k8s: env HOME=...）。
	//
	// 以非 root 用户运行时（User 非空）镜像里的 HOME 往往不可写（例如 /root），
	// 需要同时配合 Tmpfs 把 HOME 挂成可写的内存盘。
	HomeDir string
	// Tmpfs 是以内存盘（tmpfs）挂载的容器内路径，落地方式由 runtime 决定：
	// docker 映射 HostConfig.Tmpfs，kubernetes 映射 EmptyDir{Medium: Memory}。
	//
	// 典型用法是给非 root 用户挂一个可写的 HOME：
	//
	//	docker run --user "$UID:$GID" -e HOME=/home/app \
	//	  --tmpfs /home/app:rw,uid=$UID,gid=$GID,mode=700
	Tmpfs []ContainerTmpfs

	// PullPolicy 来自容器模板绑定行（ContainerTemplateDefinition.PullPolicy），
	// 取值 Always / IfNotPresent / Never，空值按 IfNotPresent 处理。落地方式由 runtime 决定：
	// docker 没有 imagePullPolicy 概念，在 create 前按策略决定是否 pull；
	// kubernetes 直接映射为 container.imagePullPolicy。
	PullPolicy string

	CPU    float64
	Memory int64

	WorkDir          string
	RuntimeName      string
	RuntimeNamespace string
	WorkloadKind     string
	ExposedPort      int
	ExposeService    bool

	// PublishPort 为 true 时把 ExposedPort 以随机宿主机端口发布（-p 0.0.0.0:0:<port>），
	// 供 gobrave 之外的进程（如 Windows 宿主浏览器）访问。
	//
	// docker 与 k8s 语义不同：docker 由 docker runtime 落地为 PortBindings；
	// k8s 的 ExposeService 已经通过 Service/Ingress 暴露，不读取本字段。
	PublishPort bool
}

type ContainerSchedulingSelector struct {
	Constraints []ContainerSchedulingConstraint `json:"constraints"`
}

type ContainerSchedulingConstraint struct {
	Type     string   `json:"type"`
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type ContainerVolume struct {
	Source string
	Target string
	Mode   string
	// Type 可选：值为 "file" 时源路径按文件创建，否则按目录创建。
	Type  string
	Owner string
}

// ContainerTmpfs 描述一个内存盘挂载（对应 docker --tmpfs / HostConfig.Tmpfs）。
type ContainerTmpfs struct {
	// Target 是容器内的挂载路径（绝对路径）。
	Target string
	// Mode 是挂载权限，默认 1777，可写为 700 等。
	Mode string
	// UID/GID 是挂载点（tmpfs 根目录）的属主；为空时使用运行时默认（root）。
	//
	// 以非 root 用户运行并挂 HOME 时必须设置，否则容器用户无法写入 HOME。
	UID string
	GID string
}

type OutboxEvent struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Type string `json:"type" gorm:"type:varchar(128);index;not null"`

	Payload datatypes.JSON `json:"payload" gorm:"type:json;not null"`

	Status string `json:"status" gorm:"type:varchar(32);index;not null;default:pending"` // pending / sent

	SentAt *time.Time `json:"sent_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (OutboxEvent) TableName() string {
	return "go_outbox_event"
}

func (t *OutboxEvent) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	if t.Status == "" {
		t.Status = "pending"
	}
	return nil
}

// type WorkflowRun struct {
// 	ID uint64 `gorm:"primaryKey"`

// 	WorkflowID uint64
// 	ProjectID  uint64
// 	UserID     uint64

// 	Status string

// 	WorkDir string

// 	StartedAt  *time.Time
// 	FinishedAt *time.Time

// 	CreatedAt time.Time
// 	UpdatedAt time.Time
// }

// type DagNode struct {
// 	ID uint64 `gorm:"primaryKey"`

// 	WorkflowRunID uint64 `gorm:"index"`

// 	Name string

// 	StepKey string

// 	ContainerTemplateID uint64

// 	Command string

// 	WorkDir string

// 	Status string

// 	RetryCount int

// 	StartedAt  *time.Time
// 	FinishedAt *time.Time

// 	CreatedAt time.Time
// 	UpdatedAt time.Time
// }
