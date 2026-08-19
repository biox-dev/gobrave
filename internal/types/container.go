package types

import (
	"time"

	"github.com/gobravedev/gobrave/internal/utils"
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

	Tag string `json:"tag" gorm:"type:varchar(128);not null;index"`
	// 4.4

	// LibraryVersion string `json:"library_version" gorm:"type:varchar(128);index"`
	// R 4.4 / Python 3.11

	Registry string `json:"registry" gorm:"type:varchar(255);not null"`
	// docker.io

	Namespace string `json:"namespace" gorm:"type:varchar(255)"`
	// rocker

	FullName string `json:"full_name" gorm:"type:varchar(512);uniqueIndex;not null"`
	// docker.io/rocker/rstudio:4.4

	Digest string `json:"digest" gorm:"type:varchar(255);index"`

	Description string `json:"description" gorm:"type:text"`

	Size int64 `json:"size"`

	Status ImageStatus `json:"status" gorm:"type:varchar(32);index;not null;default:pending"`

	PullPolicy string `json:"pull_policy" gorm:"type:varchar(32);index;not null;default:IfNotPresent"`

	LastPullTime *time.Time `json:"last_pull_time"`

	LastError string `json:"last_error" gorm:"type:text"`

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

type ContainerTemplateType string

const (
	ContainerTemplateWorkflow ContainerTemplateType = "workflow"
	ContainerTemplateApp      ContainerTemplateType = "app"
	ContainerTemplateService  ContainerTemplateType = "service"
)

type ContainerTemplate struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Name        string `json:"name" gorm:"type:varchar(255);not null;index"`
	Description string `json:"description" gorm:"type:text"`

	Type ContainerTemplateType `json:"type" gorm:"type:varchar(20);index;not null"`

	// Image string // rocker/rstudio:4.4
	ImageID int64 `json:"image_id,string" gorm:"index;not null"`

	Command string `json:"command" gorm:"type:text"`

	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`

	WorkDir string `json:"work_dir" gorm:"type:varchar(512)"`
	Port    int    `json:"port" gorm:"not null;default:8787"`
	AppType string `json:"app_type" gorm:"type:varchar(32);index"`

	Env                  datatypes.JSON `json:"env" gorm:"type:json"`
	Mounts               datatypes.JSON `json:"mounts" gorm:"type:json"`
	Volumes              datatypes.JSON `json:"volumes" gorm:"type:json"`
	SchedulingConstraint datatypes.JSON `json:"scheduling_constraint" gorm:"type:json"`
	Labels               datatypes.JSON `json:"labels" gorm:"type:json"`
	ChangeUID            bool           `json:"change_uid" gorm:"default:false"`

	RLibraryPath      string    `json:"r_library_path" gorm:"type:varchar(512)"`
	PythonLibraryPath string    `json:"python_library_path" gorm:"type:varchar(512)"`
	CondaLibraryPath  string    `json:"conda_library_path" gorm:"type:varchar(512)"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
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

func (ContainerTemplate) TableName() string {
	return "go_container_template"
}

func (t *ContainerTemplate) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
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

	AppType string `json:"app_type" gorm:"type:varchar(32);index"`

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

	PathPrefix string `json:"path_prefix" gorm:"type:varchar(512);not null;uniqueIndex"`

	BackendHost string `json:"backend_host" gorm:"type:varchar(255);not null"`
	BackendPort int    `json:"backend_port" gorm:"not null"`

	Metadata datatypes.JSON `json:"metadata" gorm:"type:json"`

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
