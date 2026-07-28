package types

import (
	"time"

	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Script struct {
	// ID                  uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	ID                  int64     `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	StoreID             int64     `json:"store_id,string" gorm:"column:store_id;type:bigint"`
	ProjectID           int64     `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	ScriptID            string    `json:"component_id" gorm:"column:component_id;type:varchar(255)"`
	InstallKey          string    `json:"install_key" gorm:"type:varchar(255)"`
	ComponentType       string    `json:"component_type" gorm:"type:varchar(255)"`
	ComponentName       string    `json:"component_name" gorm:"type:varchar(255)"`
	Description         string    `json:"description" gorm:"type:longtext"`
	ComponentIDs        string    `json:"component_ids" gorm:"type:longtext"`
	Img                 string    `json:"img" gorm:"type:varchar(255)"`
	ContainerTemplateID int64     `json:"container_template_id,string" gorm:"column:container_template_id;type:bigint"`
	ToolsContainerID    string    `json:"tools_container_id" gorm:"type:text"`
	Prompt              string    `json:"prompt" gorm:"type:longtext"`
	IOSchema            string    `json:"io_schema" gorm:"column:io_schema;type:longtext"`
	SubContainerID      string    `json:"sub_container_id" gorm:"type:varchar(255)"`
	Tags                string    `json:"tags" gorm:"type:varchar(255)"`
	FileType            string    `json:"file_type" gorm:"type:varchar(255)"`
	ScriptType          string    `json:"script_type" gorm:"type:varchar(255)"`
	Category            string    `json:"category" gorm:"type:varchar(255);default:default"`
	Content             string    `json:"content" gorm:"type:text"`
	OrderIndex          int       `json:"order_index"`
	Position            string    `json:"position" gorm:"type:text"`
	Edges               string    `json:"edges" gorm:"type:text"`
	Version             string    `json:"version" gorm:"type:varchar(255)"`
	Message             string    `json:"message" gorm:"type:longtext"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
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
	ID                 int64          `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectID          int64          `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	StoreID            int64          `json:"store_id,string" gorm:"column:store_id;type:bigint"`
	Name               string         `json:"name" gorm:"type:varchar(255)"`
	Img                string         `json:"img" gorm:"type:varchar(255)"`
	Tags               datatypes.JSON `json:"tags" gorm:"type:json"`
	URL                string         `json:"url" gorm:"column:url;type:varchar(255)"`
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
	Version            string         `json:"version" gorm:"type:varchar(255)"`
	Message            string         `json:"message" gorm:"type:longtext"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
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
// It flattens script -> container template -> container image fields via SQL join.
type ScriptContainerSnapshot struct {
	ScriptID       string `json:"script_id" gorm:"column:script_id"`
	ContainerID    int64  `json:"container_id,string" gorm:"column:container_id"`
	ContainerName  string `json:"container_name" gorm:"column:container_name"`
	ImageID        int64  `json:"image_id,string" gorm:"column:image_id"`
	ContainerImage string `json:"container_image" gorm:"column:container_image"`
	ImageName      string `json:"image_name" gorm:"column:image_name"`
	ImageTag       string `json:"image_tag" gorm:"column:image_tag"`
	ImageStatus    string `json:"image_status" gorm:"column:image_status"`
}

type WorkflowJSONExportResponse struct {
	// Path               string           `json:"path"`
	WorkflowID         string           `json:"workflow_id"`
	Workflow           map[string]any   `json:"workflow"`
	Scripts            []map[string]any `json:"scripts"`
	ContainerTemplates []map[string]any `json:"container_templates"`
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
	StoreVersion string `json:"store_version"`
}

type ScriptVersion struct {
	Script
	StoreVersion string `json:"store_version"`
}
