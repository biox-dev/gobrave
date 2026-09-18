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

type ImageStatus string

const (
	ImageStatusPending  ImageStatus = "pending"
	ImageStatusPulling  ImageStatus = "pulling"
	ImageStatusReady    ImageStatus = "ready"
	ImageStatusFailed   ImageStatus = "failed"
	ImageStatusDeleted  ImageStatus = "deleted"
	ImageStatusDisabled ImageStatus = "disabled"
)

type ContainerImage struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Name string `json:"name" gorm:"type:varchar(255);not null;index"`
	// rocker/rstudio

	// Tag string `json:"tag" gorm:"type:varchar(128);not null;index"`
	// 4.4

	// LibraryVersion string `json:"library_version" gorm:"type:varchar(128);index"`
	// R 4.4 / Python 3.11

	// Registry string `json:"registry" gorm:"type:varchar(255);not null"`
	// docker.io

	// Namespace string `json:"namespace" gorm:"type:varchar(255)"`
	// rocker

	FullName string `json:"full_name" gorm:"type:varchar(512);not null"`
	// docker.io/rocker/rstudio:4.4

	// Digest string `json:"digest" gorm:"type:varchar(255);index"`

	Description string `json:"description" gorm:"type:text"`

	Size int64 `json:"size"`

	// Status ImageStatus `json:"status" gorm:"type:varchar(32);index;not null;default:pending"`

	PullPolicy string `json:"pull_policy" gorm:"type:varchar(32);index;not null;default:IfNotPresent"`

	// LastPullTime *time.Time `json:"last_pull_time"`

	// LastError string `json:"last_error" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ContainerImage) TableName() string {
	return "go_container_image"
}

func (t *ContainerImage) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// ContainerImageRef 描述一个镜像被哪一个可运行模板（配置 × 镜像 绑定行）引用。
type ContainerImageRef struct {
	DefinitionID int64  `json:"definition_id,string"`
	SpecID       int64  `json:"spec_id,string"`
	TemplateName string `json:"template_name"`
}

// ContainerImageUsage 是镜像的绑定引用情况。镜像目录仍可增删改，
// 但只要还被绑定行引用就不允许删除，避免模板的 image_id 变成空引用。
type ContainerImageUsage struct {
	ImageID   int64               `json:"image_id,string"`
	RefCount  int                 `json:"ref_count"`
	Templates []ContainerImageRef `json:"templates"`
}

// ContainerImageItem 是镜像目录的对外读模型：嵌入镜像本体（JSON 平铺，字段不变）+ 绑定引用信息。
type ContainerImageItem struct {
	*ContainerImage
	Usage *ContainerImageUsage `json:"usage,omitempty"`
}

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

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
// 由于每行本身就唯一确定了一个可运行配置，不需要 is_default / sort_order 之类的标记：
// 同一 SpecID 可绑定多个镜像，同一 ImageID 也可被多个配置绑定（多对多），
// 且同一 (SpecID, ImageID) 组合唯一。
// 注意：该表主键 ID 就是对外暴露的 ContainerTemplate.ID。
type ContainerTemplateDefinition struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SpecID  int64 `json:"spec_id,string" gorm:"uniqueIndex:uk_template_spec_image,priority:1;not null"`
	ImageID int64 `json:"image_id,string" gorm:"uniqueIndex:uk_template_spec_image,priority:2;not null"`

	// DisplayName 为空时，对外名称回退到 ContainerTemplateConfig.Name。
	DisplayName string `json:"display_name" gorm:"type:varchar(255)"`

	// R/Python/Conda 包目录与镜像版本强耦合（R 4.3 与 4.4 不能共用），故挂在绑定行而非配置上。
	RLibraryPath      string `json:"r_library_path" gorm:"type:varchar(512)"`
	PythonLibraryPath string `json:"python_library_path" gorm:"type:varchar(512)"`
	CondaLibraryPath  string `json:"conda_library_path" gorm:"type:varchar(512)"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

// ContainerTemplate 不是持久化对象，由 repo 层 JOIN
// go_container_template_definition + go_container_template_spec + go_container_image 组装返回。
// ID == ContainerTemplateDefinition.ID：引用方（script.container_template_id /
// app_session.container_template_id / container_instance.template_id）语义不变，
// 同时天然锁定了该模板要使用的镜像。
type ContainerTemplate struct {
	ID      int64 `json:"id,string"`
	SpecID  int64 `json:"spec_id,string"`
	ImageID int64 `json:"image_id,string"`

	Name        string          `json:"name"`
	Description string          `json:"description"`
	Image       *ContainerImage `json:"image,omitempty"`

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

	RLibraryPath      string `json:"r_library_path"`
	PythonLibraryPath string `json:"python_library_path"`
	CondaLibraryPath  string `json:"conda_library_path"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c *ContainerTemplate) GetRLibraryPath() string {
	if c.RLibraryPath == "" {
		return "R_DEFAULT_LIBS"
	}
	return c.RLibraryPath
}
func (c *ContainerTemplate) GetPythonLibraryPath() string {
	if c.PythonLibraryPath == "" {
		return "PYTHON_DEFAULT_LIBS"
	}
	return c.PythonLibraryPath
}
func (c *ContainerTemplate) GetCondaLibraryPath() string {
	if c.CondaLibraryPath == "" {
		return "CONDA_DEFAULT_LIBS"
	}
	return c.CondaLibraryPath
}

// NewContainerTemplate 把共享配置、绑定行与镜像组装成对外读模型。spec/image 允许为 nil：
// 分别表示只取绑定行字段 / 未加载镜像详情。binding 为 nil 时返回 nil。
func NewContainerTemplate(spec *ContainerTemplateSpec, binding *ContainerTemplateDefinition, image *ContainerImage) *ContainerTemplate {
	if binding == nil {
		return nil
	}

	tpl := &ContainerTemplate{
		ID:                binding.ID,
		SpecID:            binding.SpecID,
		ImageID:           binding.ImageID,
		Image:             image,
		RLibraryPath:      binding.RLibraryPath,
		PythonLibraryPath: binding.PythonLibraryPath,
		CondaLibraryPath:  binding.CondaLibraryPath,
		CreatedAt:         binding.CreatedAt,
		UpdatedAt:         binding.UpdatedAt,
	}

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

		if spec.UpdatedAt.After(tpl.UpdatedAt) {
			tpl.UpdatedAt = spec.UpdatedAt
		}
	}

	if binding.DisplayName != "" {
		tpl.Name = binding.DisplayName
	}

	return tpl
}

// ContainerImageExport 是 ContainerImage 的导出结构，包含 ID，不含时间字段。
type ContainerImageExport struct {
	ID          int64  `json:"id,string"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Size        int64  `json:"size"`
	PullPolicy  string `json:"pull_policy"`
}

func (t *ContainerImage) ToExport() *ContainerImageExport {
	if t == nil {
		return nil
	}
	return &ContainerImageExport{
		ID:          t.ID,
		Name:        t.Name,
		FullName:    t.FullName,
		Description: t.Description,
		Size:        t.Size,
		PullPolicy:  t.PullPolicy,
	}
}

// ContainerTemplateExport 是 ContainerTemplate 的导出结构，包含 ID，不含时间字段；
// ImageID 被转换为内嵌的 ContainerImageExport 对象。
type ContainerTemplateExport struct {
	ID                   int64                 `json:"id,string"`
	Name                 string                `json:"name"`
	Description          string                `json:"description"`
	Image                *ContainerImageExport `json:"image"`
	Command              string                `json:"command"`
	CPU                  float64               `json:"cpu"`
	Memory               int64                 `json:"memory"`
	WorkDir              string                `json:"work_dir"`
	Port                 int                   `json:"port"`
	AppType              string                `json:"app_type"`
	Env                  datatypes.JSON        `json:"env"`
	Mounts               datatypes.JSON        `json:"mounts"`
	Volumes              datatypes.JSON        `json:"volumes"`
	SchedulingConstraint datatypes.JSON        `json:"scheduling_constraint"`
	Labels               datatypes.JSON        `json:"labels"`
	ChangeUID            bool                  `json:"change_uid"`
	RLibraryPath         string                `json:"r_library_path"`
	PythonLibraryPath    string                `json:"python_library_path"`
	CondaLibraryPath     string                `json:"conda_library_path"`
}

func (t *ContainerTemplate) ToExport(image *ContainerImage) *ContainerTemplateExport {
	if t == nil {
		return nil
	}
	if image == nil {
		image = t.Image
	}
	export := &ContainerTemplateExport{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Command:     t.Command,
		CPU:         t.CPU,
		Memory:      t.Memory,
		WorkDir:     t.WorkDir,
		Port:        t.Port,
		AppType:     t.AppType,
		Env:         t.Env,
		Mounts:      t.Mounts,
		// Volumes:              t.Volumes,
		SchedulingConstraint: t.SchedulingConstraint,
		Labels:               t.Labels,
		ChangeUID:            t.ChangeUID,
		RLibraryPath:         t.RLibraryPath,
		PythonLibraryPath:    t.PythonLibraryPath,
		CondaLibraryPath:     t.CondaLibraryPath,
	}
	if image != nil {
		export.Image = image.ToExport()
	}
	return export
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

	UserID    string `json:"user_id" gorm:"type:varchar(36);index;not null"`
	ProjectID int64  `json:"project_id,string" gorm:"column:project_id;type:bigint"`

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
