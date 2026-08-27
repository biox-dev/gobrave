package types

import (
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type Dataset struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID string `json:"dataset_id" gorm:"type:varchar(255);uniqueIndex;not null"`

	DatasetName string `json:"dataset_name" gorm:"type:varchar(255)"`

	Description string `json:"description" gorm:"type:text"`

	Metadata string `json:"metadata" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (Dataset) TableName() string {
	return "go_dataset"
}

func (t *Dataset) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type ProjectDataset struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	ProjectID string `json:"project_id" gorm:"index;not null"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	CreatedAt time.Time `json:"created_at"`
}

func (ProjectDataset) TableName() string {
	return "go_project_dataset"
}

func (t *ProjectDataset) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// QueryDataset defines optional dataset filters for list/page APIs.
type QueryDataset struct {
	ID *int64 `json:"id,string,omitempty"`

	DatasetID string `json:"dataset_id,omitempty"`

	DatasetName string `json:"dataset_name,omitempty"`

	Description string `json:"description,omitempty"`

	Metadata string `json:"metadata,omitempty"`
}

func (q *QueryDataset) GetDatasetID() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.DatasetID)
}

func (q *QueryDataset) GetDatasetName() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.DatasetName)
}

func (q *QueryDataset) GetDescription() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Description)
}

func (q *QueryDataset) GetMetadata() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Metadata)
}
