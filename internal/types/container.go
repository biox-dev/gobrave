package types

import (
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
type ContainerTemplateSpec struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Name        string `json:"name" gorm:"type:varchar(255);not null;index"`
	Description string `json:"description" gorm:"type:text"`

	Command string `json:"command" gorm:"type:text"`

	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`

	WorkDir string `json:"work_dir" gorm:"type:varchar(512)"`
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

	WorkDir string `json:"work_dir"`
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
		tpl.WorkDir = spec.WorkDir
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
		WorkDir:              t.WorkDir,
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

	RuntimeName string `json:"runtime_name" gorm:"type:varchar(255);index;not null"`

	// NodeID *uint64 `gorm:"index"`

	RuntimeID string `json:"runtime_id" gorm:"type:varchar(255);index"`
	// docker id / pod uid

	Name string `json:"name" gorm:"type:varchar(255);not null;index"`

	Status ContainerStatus `json:"status" gorm:"type:varchar(30);index;not null;default:pending"`

	IPAddress       string `json:"ip_address" gorm:"type:varchar(128)"`
	RuntimeNodeName string `json:"runtime_node_name" gorm:"type:varchar(255);index"`

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
	return nil
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
