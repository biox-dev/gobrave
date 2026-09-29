package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Sample 是一次生物采样记录，隶属于某个 Dataset（Dataset -> DatasetSample ->
// Sample -> Assay -> File）。
type Sample struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SampleName 是唯一的业务编号兼展示名（列 go_sample.sample_name），例如
	// S-001 / 脾脏-6。无需全局唯一，只在同一个 dataset 下唯一（经
	// go_dataset_sample 绑定判定）；同一个名字可以出现在不同 dataset。
	SampleName string `json:"sample_name" gorm:"type:varchar(255);index;not null"`

	Tissue string `json:"tissue" gorm:"type:varchar(128)"`

	CellType string `json:"cell_type" gorm:"type:varchar(128)"`

	CollectionTime *time.Time `json:"collection_time"`

	Metadata string `json:"metadata" gorm:"type:text"`

	Description string `json:"description" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Sample) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (Sample) TableName() string {
	return "go_sample"
}

// QuerySample carries optional Sample filters for list/page APIs. Empty values
// are ignored by the repository; SampleName matches the business number
// (go_sample.sample_name).
type QuerySample struct {
	SampleName string

	Tissue string

	CellType string
}

// SampleWithDatasetInfo is the read model of a Sample joined with the dataset it
// is bound to through go_dataset_sample, used by the project-wide sample picker.
// The dataset binding lives on go_dataset_sample (dataset_id + sample_id), so
// this is the entry point of the Sample -> Assay -> File branch. Samples are
// resolved without a role filter (unlike Assay/File), so this model carries no
// Role field.
type SampleWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	// SampleName 是 Sample 的业务编号兼展示名（go_sample.sample_name）。
	SampleName string `json:"sample_name"`

	Tissue string `json:"tissue"`

	CellType string `json:"cell_type"`

	CollectionTime *time.Time `json:"collection_time"`

	Metadata string `json:"metadata"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`
}

// Assay 是一次实验/测序建库记录，隶属于某个 Sample（Dataset -> DatasetSample ->
// Sample -> Assay -> File）。其下文件（go_file.assay_id）是 assay 私有的，不跨 assay 复用。
type Assay struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SampleID int64 `json:"sample_id,string" gorm:"index;not null"`

	AssayType string `json:"assay_type" gorm:"type:varchar(64);index"`

	Platform string `json:"platform" gorm:"type:varchar(128)"`

	LibraryID string `json:"library_id" gorm:"type:varchar(255)"`

	// AssayName 是 assay 的展示名（列 go_assay.assay_name）；为空时展示层回退到
	// library_id → assay_type → 主键（见 handler.assayDisplayName）。
	AssayName string `json:"assay_name" gorm:"type:varchar(255);index"`

	// Role 是该 assay 的角色（列 go_assay.role），例如 DEFAULT / TABLE；
	// input_type=assay 的表单输入用 resolver.accept_formats 与之匹配（与
	// go_dataset_file.role 对文件的口径一致）。空表示不参与角色过滤。
	Role string `json:"role" gorm:"type:varchar(64);index"`

	Metadata string `json:"metadata" gorm:"type:text"`

	Description string `json:"description" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

// AssayWithSampleInfo is the read model of an Assay joined with its owning
// Sample. Assays carry no dataset binding of their own: an assay belongs to a
// Sample, and it is the Sample that is bound to a project's dataset through
// go_dataset_sample, so a project's assays are resolved through the project's
// samples.
type AssayWithSampleInfo struct {
	ID int64 `json:"id,string"`

	SampleID int64 `json:"sample_id,string"`

	// SampleName 是所属 Sample 的业务名（go_sample.sample_name）；需要主键时
	// 经 sample_id 再查 Sample。
	SampleName string `json:"sample_name"`

	AssayType string `json:"assay_type"`

	Platform string `json:"platform"`

	LibraryID string `json:"library_id"`

	// AssayName 是该 assay 的展示名（go_assay.assay_name）。
	AssayName string `json:"assay_name"`

	// Role 是该 assay 的角色（go_assay.role），例如 DEFAULT / TABLE；
	// input_type=assay 的表单输入用 resolver.accept_formats 与之匹配，
	// resolveFormAnalysisResult 据此把 assay 分组挂到 analysis_result[role]。
	Role string `json:"role"`

	Metadata string `json:"metadata"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
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

// DatasetSample is the join table binding a Sample into a Dataset
// (go_dataset_sample). A dataset binds directly to the Sample; its assays and
// files are resolved through that sample, so every project-scoped query joins
// go_dataset_sample -> go_sample to filter on dataset_id. The same sample can be
// bound to multiple datasets, and samples are resolved project-wide without a
// role, so this binding carries no Role field.
type DatasetSample struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	SampleID int64 `json:"sample_id,string" gorm:"index;not null"`

	CreatedAt time.Time `json:"created_at"`
}

func (t *DatasetSample) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (DatasetSample) TableName() string {
	return "go_dataset_sample"
}
