package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type Sample struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SampleID string `json:"sample_id" gorm:"type:varchar(255);uniqueIndex;not null"`

	SampleName string `json:"sample_name" gorm:"type:varchar(255)"`

	SubjectID string `json:"subject_id" gorm:"type:varchar(255)"`

	GroupName string `json:"group_name" gorm:"type:varchar(255)"`

	Phenotype string `json:"phenotype" gorm:"type:varchar(255)"`

	Metadata string `json:"metadata" gorm:"type:text"`

	Description string `json:"description" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

type SampleWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	SampleID string `json:"sample_id"`

	SampleName string `json:"sample_name"`

	SubjectID string `json:"subject_id"`

	GroupName string `json:"group_name"`

	Phenotype string `json:"phenotype"`

	Metadata string `json:"metadata"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`
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

type SampleFile struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	SampleID int64 `json:"sample_id,string" gorm:"index;not null"`

	FileID int64 `json:"file_id,string" gorm:"index;not null"`

	Role string `json:"role" gorm:"type:varchar(64)"`

	Lane string `json:"lane" gorm:"type:varchar(32)"`

	Replicate string `json:"replicate" gorm:"type:varchar(32)"`

	CreatedAt time.Time `json:"created_at"`
}

func (t *SampleFile) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (SampleFile) TableName() string {
	return "go_sample_file"
}
