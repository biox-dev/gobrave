package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Script struct {
	// ID                  uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	ID                  int64  `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	StoreID             int64  `json:"store_id,string" gorm:"column:store_id;type:bigint"`
	ProjectID           int64  `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	ScriptID            string `json:"component_id" gorm:"column:component_id;type:varchar(255)"`
	InstallKey          string `json:"install_key" gorm:"type:varchar(255)"`
	ComponentType       string `json:"component_type" gorm:"type:varchar(255)"`
	ComponentName       string `json:"component_name" gorm:"type:varchar(255)"`
	Description         string `json:"description" gorm:"type:longtext"`
	ComponentIDs        string `json:"component_ids" gorm:"type:longtext"`
	Img                 string `json:"img" gorm:"type:varchar(255)"`
	ContainerTemplateID int64  `json:"container_template_id,string" gorm:"column:container_template_id;type:bigint"`
	ToolsContainerID    string `json:"tools_container_id" gorm:"type:text"`
	Prompt              string `json:"prompt" gorm:"type:longtext"`
	IOSchema            string `json:"io_schema" gorm:"column:io_schema;type:longtext"`
	SubContainerID      string `json:"sub_container_id" gorm:"type:varchar(255)"`
	Tags                string `json:"tags" gorm:"type:varchar(255)"`
	FileType            string `json:"file_type" gorm:"type:varchar(255)"`
	ScriptType          string `json:"script_type" gorm:"type:varchar(255)"`
	Category            string `json:"category" gorm:"type:varchar(255);default:default"`
	Content             string `json:"content" gorm:"type:text"`
	OrderIndex          int    `json:"order_index"`
	Position            string `json:"position" gorm:"type:text"`
	Edges               string `json:"edges" gorm:"type:text"`
	// URL                 string `json:"url" gorm:"column:url;type:varchar(255)"`

	// Version   string    `json:"version" gorm:"type:varchar(255)"`
	// Message   string    `json:"message" gorm:"type:longtext"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Script) TableName() string {
	return "pipeline_components"
}
func (t *Script) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type Workflow struct {
	// ID                 uint           `json:"id" gorm:"primaryKey;autoIncrement"`
	ID        int64          `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectID int64          `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	StoreID   int64          `json:"store_id,string" gorm:"column:store_id;type:bigint"`
	Name      string         `json:"name" gorm:"type:varchar(255)"`
	Img       string         `json:"img" gorm:"type:varchar(255)"`
	Tags      datatypes.JSON `json:"tags" gorm:"type:json"`
	// URL                string         `json:"url" gorm:"column:url;type:varchar(255)"`
	Category           string         `json:"category" gorm:"type:varchar(255);default:default"`
	Description        string         `json:"description" gorm:"type:longtext"`
	Prompt             string         `json:"prompt" gorm:"type:longtext"`
	DagDefinition      string         `json:"dag_definition" gorm:"column:dag_definition;type:longtext"`
	WorkflowID         string         `json:"relation_id" gorm:"column:relation_id;type:varchar(255)"`
	RelationType       string         `json:"relation_type" gorm:"type:varchar(255)"`
	InstallKey         string         `json:"install_key" gorm:"type:varchar(255)"`
	ModuleID           string         `json:"component_id" gorm:"column:component_id;type:varchar(255)"`
	ContainerID        string         `json:"container_id" gorm:"type:varchar(255)"`
	ParentComponentID  string         `json:"parent_component_id" gorm:"type:varchar(255)"`
	InputComponentIDs  datatypes.JSON `json:"input_component_ids" gorm:"type:json"`
	OutputComponentIDs datatypes.JSON `json:"output_component_ids" gorm:"type:json"`
	OrderIndex         int            `json:"order_index"`
	// Version            string         `json:"version" gorm:"type:varchar(255)"`
	// Message   string    `json:"message" gorm:"type:longtext"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Workflow) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}
func (Workflow) TableName() string {
	return "pipeline_components_relation"
}

// ScriptContainerSnapshot is a read model for analysis node visualization.
// It flattens script -> container template definition/spec -> container image fields via SQL join.
// ContainerID is the go_container_template_definition primary key (the id scripts bind to);
// the image tag is already part of ContainerImage (go_container_image.full_name), and image
// status is no longer persisted, so there is no separate tag/status column here.
type ScriptContainerSnapshot struct {
	ScriptID       string `json:"script_id" gorm:"column:script_id"`
	ContainerID    int64  `json:"container_id,string" gorm:"column:container_id"`
	ContainerName  string `json:"container_name" gorm:"column:container_name"`
	ImageID        int64  `json:"image_id,string" gorm:"column:image_id"`
	ContainerImage string `json:"container_image" gorm:"column:container_image"`
	ImageName      string `json:"image_name" gorm:"column:image_name"`
}

type WorkflowJSONExportResponse struct {
	// Version 是导出文件格式版本（取值见 internal/exportcodec，如 exportcodec.VersionV1），
	// 写入 workflow.json 顶层；安装侧（InstallWorkflow）据此路由到对应版本的 Codec。
	// 历史产物没有该字段，反序列化后为空字符串。
	Version string `json:"version"`
	// Path               string           `json:"path"`
	WorkflowID string           `json:"workflow_id"`
	Workflow   map[string]any   `json:"workflow"`
	Scripts    []map[string]any `json:"scripts"`
	// ContainerTemplateSpecs 是去重后的容器运行配置列表（按 ContainerTemplateSpec 主键去重，
	// 结构见 types.ContainerTemplateSpec）。脚本的 container_template_id 引用的是绑定行主键，
	// 绑定行通过 SpecID 引用这里的配置，因此三个列表都按主键去重，不重复出现。
	ContainerTemplateSpecs []map[string]any `json:"container_template_specs"`
	// ContainerTemplateDefinitions 是去重后的「运行配置 × 镜像」绑定行列表（按主键去重，
	// 结构见 types.ContainerTemplateDefinition）：SpecID 指向 ContainerTemplateSpecs，
	// ImageID 指向 ContainerImages，主键就是脚本 container_template_id 引用的值。
	ContainerTemplateDefinitions []map[string]any `json:"container_template_definitions"`
	// ContainerImages 是去重后的容器镜像列表（按镜像主键去重），
	// 结构见 types.ContainerImageExport，与 container_template_definitions 的 image_id 一一对应。
	ContainerImages []map[string]any `json:"container_images"`
}

type WorkflowVersion struct {
	// ID                 int64          `json:"id,string"`
	// ProjectID          int64          `json:"project_id,string"`
	// StoreID            int64          `json:"store_id,string"`
	// Name               string         `json:"name"`
	// Img                string         `json:"img"`
	// Tags               datatypes.JSON `json:"tags"`
	// URL                string         `json:"url"`
	// Category           string         `json:"category"`
	// Description        string         `json:"description"`
	// Prompt             string         `json:"prompt"`
	// DagDefinition      string         `json:"dag_definition"`
	// WorkflowID         string         `json:"relation_id"`
	// RelationType       string         `json:"relation_type"`
	// InstallKey         string         `json:"install_key"`
	// ModuleID           string         `json:"component_id"`
	// ContainerID        string         `json:"container_id"`
	// ParentComponentID  string         `json:"parent_component_id"`
	// InputComponentIDs  datatypes.JSON `json:"input_component_ids"`
	// OutputComponentIDs datatypes.JSON `json:"output_component_ids"`
	// OrderIndex         int            `json:"order_index"`
	// Version            string         `json:"version"`
	// UpdateInfo         string         `json:"update_info"`
	// CreatedAt          time.Time      `json:"created_at"`
	// UpdatedAt          time.Time      `json:"updated_at"`
	Workflow
	StorePath    string `json:"store_path"`
	WorkflowPath string `json:"workflow_path"`
	StoreURL     string `json:"store_url"`
	StoreMessage string `json:"store_message"`
	// StoreVersion string `json:"store_version"`
	// GitState 由磁盘上的 git 元数据实时推导（不落库）：
	// has_local_changes=true 表示本地有未发布改动，
	// has_store_changes=true 表示 store 有本地未同步的提交。
	GitState *utils.GitSyncState `json:"git_state,omitempty"`
}

type ScriptVersion struct {
	Script
	// StoreVersion string `json:"store_version"`
	StorePath    string `json:"store_path"`
	ScriptPath   string `json:"script_path"`
	StoreURL     string `json:"store_url"`
	StoreMessage string `json:"store_message"`
	// GitState 由磁盘上的 git 元数据实时推导（不落库）：
	// has_local_changes=true 表示本地有未发布改动，
	// has_store_changes=true 表示 store 有本地未同步的提交。
	GitState *utils.GitSyncState `json:"git_state,omitempty"`
}

type ScriptJSONExportResponse struct {
	// Version 是导出文件格式版本（取值见 internal/exportcodec，如 exportcodec.VersionV1），
	// 写入 script.json 顶层；安装侧（InstallScript）据此路由到对应版本的 Codec。
	// 历史产物没有该字段，反序列化后为空字符串。
	Version  string         `json:"version"`
	ScriptID string         `json:"script_id"`
	Script   map[string]any `json:"script"`
	// ContainerTemplateSpecs 是去重后的容器运行配置列表（按 ContainerTemplateSpec 主键去重，
	// 结构见 types.ContainerTemplateSpec）。脚本的 container_template_id 引用的是绑定行主键，
	// 绑定行通过 SpecID 引用这里的配置，因此三个列表都按主键去重，不重复出现。
	ContainerTemplateSpecs []map[string]any `json:"container_template_specs,omitempty"`
	// ContainerTemplateDefinitions 是去重后的「运行配置 × 镜像」绑定行列表（按主键去重，
	// 结构见 types.ContainerTemplateDefinition）：SpecID 指向 ContainerTemplateSpecs，
	// ImageID 指向 ContainerImages，主键就是脚本 container_template_id 引用的值。
	ContainerTemplateDefinitions []map[string]any `json:"container_template_definitions,omitempty"`
	// ContainerImages 是去重后的容器镜像列表（按镜像主键去重），
	// 结构见 types.ContainerImageExport，与 container_template_definitions 的 image_id 一一对应。
	ContainerImages []map[string]any `json:"container_images,omitempty"`
}
