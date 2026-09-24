package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Subject 是实验对象/个体，隶属于某个 Project（Subject -> Sample -> Assay -> File）。
type Subject struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SubjectKey 是输入用的英文标识（列 go_subject.subject_key），例如 mouse-001；
	// 与 Sample.SampleKey 同语义，是业务意义上的“编号”。无需全局唯一。
	// 注意：Subject 的真正主键仍然是 ID；go_sample.subject_id 是指向 ID 的
	// 外键，语义不同（一个是编号，一个是主键）。
	SubjectKey string `json:"subject_key" gorm:"type:varchar(255);index;not null"`

	// SubjectName 是人类可读的展示名（中文或带空格），例如 “小鼠 001”；
	// 不要求唯一，可为空，展示时回退到 SubjectKey。
	SubjectName string `json:"subject_name" gorm:"type:varchar(255)"`

	Species string `json:"species" gorm:"type:varchar(128)"`

	Strain string `json:"strain" gorm:"type:varchar(128)"`

	Sex string `json:"sex" gorm:"type:varchar(32)"`

	Age string `json:"age" gorm:"type:varchar(64)"`

	Metadata string `json:"metadata" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Subject) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (Subject) TableName() string {
	return "go_subject"
}

// QuerySubject carries optional Subject filters for list/page APIs. Empty
// strings are ignored by the repository.
type QuerySubject struct {
	SubjectKey string

	SubjectName string

	Species string

	Strain string

	Sex string
}

// Sample 是一次生物采样记录，隶属于某个 Subject（Subject -> Sample -> Assay -> File）。
type Sample struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SampleKey 是业务编号（列 go_sample.sample_key），例如 S-001。
	SampleKey string `json:"sample_key" gorm:"type:varchar(255);uniqueIndex;not null"`

	SampleName string `json:"sample_name" gorm:"type:varchar(255)"`

	SubjectID int64 `json:"subject_id,string" gorm:"index;not null"`

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
// are ignored by the repository; SampleKey matches the business number
// (go_sample.sample_key) while SubjectID matches the owning subject PK.
type QuerySample struct {
	SampleKey string

	SampleName string

	SubjectID *int64

	Tissue string

	CellType string
}

// SampleWithSubjectInfo is the read model of a Sample joined with its owning
// Subject, used by the paged/list APIs so the UI can show the subject name.
type SampleWithSubjectInfo struct {
	ID int64 `json:"id,string"`

	// SampleKey 是 Sample 的业务编号（go_sample.sample_key）。
	SampleKey string `json:"sample_key"`

	SampleName string `json:"sample_name"`

	// SubjectID 是所属 Subject 的主键（等于 go_sample.subject_id 外键）。
	SubjectID int64 `json:"subject_id,string"`

	// SubjectName is the subject's human-readable display name
	// (go_subject.subject_name); the machine-readable business key lives on
	// go_subject.subject_key and is intentionally NOT projected here because
	// `subject_id` on this struct is the owning subject's int64 primary key.
	SubjectName string `json:"subject_name"`

	Species string `json:"species"`

	Tissue string `json:"tissue"`

	CellType string `json:"cell_type"`

	CollectionTime *time.Time `json:"collection_time"`

	Metadata string `json:"metadata"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

// Assay 是一次实验/测序建库记录，隶属于某个 Sample（Subject -> Sample -> Assay -> File）。
// 其下文件（go_file.assay_id）是 assay 私有的，不跨 assay 复用。
type Assay struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SampleID int64 `json:"sample_id,string" gorm:"index;not null"`

	AssayType string `json:"assay_type" gorm:"type:varchar(64);index"`

	Platform string `json:"platform" gorm:"type:varchar(128)"`

	LibraryID string `json:"library_id" gorm:"type:varchar(255)"`

	Metadata string `json:"metadata" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

type AssayWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	SampleID int64 `json:"sample_id,string"`

	// SampleName 是所属 Sample 的展示名（go_sample.sample_name）。
	SampleName string `json:"sample_name"`

	// SubjectName 是所属 Subject 的人类可读展示名（go_subject.subject_name），
	// 与 SampleName 一样只承载展示名；需要主键时经 SampleKey 再查 Sample。
	// 英文标识（go_subject.subject_key）不在此投影，避免与 go_sample.subject_id
	// 这个外键同名字段混淆。
	SubjectName string `json:"subject_name"`

	AssayType string `json:"assay_type"`

	Platform string `json:"platform"`

	LibraryID string `json:"library_id"`

	Metadata string `json:"metadata"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`

	// Role is the assay's role inside its owning dataset binding
	// (go_dataset_assay.role). Analysis form inputs with input_type=assay match
	// it against their resolver.accept_formats, mirroring DatasetFile.Role.
	Role string `json:"role"`
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

type DatasetAssay struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	AssayID int64 `json:"assay_id,string" gorm:"index;not null"`

	// Role is the assay's role inside its dataset binding, e.g. DEFAULT or TABLE.
	// Analysis form inputs with input_type=assay match it against their
	// resolver.accept_formats (same convention as DatasetFile.Role).
	Role string `json:"role" gorm:"type:varchar(64)"`

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
