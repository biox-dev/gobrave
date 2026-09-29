package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Assay 是一次实验/测序建库记录，直接隶属于某个 Dataset（Dataset -> DatasetAssay ->
// Assay -> File）。其下文件（go_file.assay_id）是 assay 私有的，不跨 assay 复用。
type Assay struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SampleName 是样本的业务编号兼展示名（列 go_assay.sample_name），例如
	// S-001 / 脾脏-6。无需全局唯一，只在同一个 dataset 下唯一（经
	// go_dataset_assay 绑定判定）；同一个名字可以出现在不同 dataset。
	SampleName string `json:"sample_name" gorm:"type:varchar(255);index;not null"`

	AssayType string `json:"assay_type" gorm:"type:varchar(64);index"`

	Platform string `json:"platform" gorm:"type:varchar(128)"`

	LibraryID string `json:"library_id" gorm:"type:varchar(255)"`

	// Role 是该 assay 的角色（列 go_assay.role），例如 DEFAULT / TABLE；
	// input_type=assay 的表单输入用 resolver.accept_formats 与之匹配（与
	// go_dataset_file.role 对文件的口径一致）。空表示不参与角色过滤。
	Role string `json:"role" gorm:"type:varchar(64);index"`

	Metadata string `json:"metadata" gorm:"type:text"`

	Description string `json:"description" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

// AssayWithDatasetInfo is the read model of an Assay joined with the dataset it
// is bound to through go_dataset_assay, used by the project-wide assay queries.
// The assay itself carries its SampleName, so no sample join is needed.
type AssayWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	SampleName string `json:"sample_name"`

	AssayType string `json:"assay_type"`

	Platform string `json:"platform"`

	LibraryID string `json:"library_id"`

	// Role 是该 assay 的角色（go_assay.role），例如 DEFAULT / TABLE；
	// input_type=assay 的表单输入用 resolver.accept_formats 与之匹配，
	// resolveFormAnalysisResult 据此把 assay 分组挂到 analysis_result[role]。
	Role string `json:"role"`

	Metadata string `json:"metadata"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`
}

func (t *Assay) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (Assay) TableName() string {
	return "go_assay"
}

// DatasetAssay is the join table binding an Assay into a Dataset
// (go_dataset_assay). A dataset binds directly to the Assay; the assay's files
// follow it (go_file.assay_id), so every project-scoped query joins
// go_dataset_assay -> go_assay to filter on dataset_id. The same assay can be
// bound to multiple datasets.
type DatasetAssay struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	AssayID int64 `json:"assay_id,string" gorm:"index;not null"`

	CreatedAt time.Time `json:"created_at"`
}

func (t *DatasetAssay) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (DatasetAssay) TableName() string {
	return "go_dataset_assay"
}
