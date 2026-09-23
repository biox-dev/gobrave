package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Assay 是一次实验/测序建库记录，隶属于某个 Sample（Sample -> Assay -> AssayFile）。
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

	AssayType string `json:"assay_type"`

	Platform string `json:"platform"`

	LibraryID string `json:"library_id"`

	Metadata string `json:"metadata"`

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

type AssayFile struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	AssayID int64 `json:"assay_id,string" gorm:"index;not null"`

	FileID int64 `json:"file_id,string" gorm:"index;not null"`

	Role string `json:"role" gorm:"type:varchar(64)"`

	Lane string `json:"lane" gorm:"type:varchar(32)"`

	Replicate string `json:"replicate" gorm:"type:varchar(32)"`

	CreatedAt time.Time `json:"created_at"`
}

func (t *AssayFile) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (AssayFile) TableName() string {
	return "go_assay_file"
}
