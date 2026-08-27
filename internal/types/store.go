package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Store struct {
	ID int64 `json:"id,string" gorm:"primaryKey;autoIncrement"`

	// StoreID string `json:"store_id" gorm:"type:varchar(255);index"`
	// AppID       string         `json:"app_id" gorm:"type:varchar(255);index"`
	// workflow script
	StoreType   string         `json:"store_type" gorm:"type:varchar(255);index"`
	Name        string         `json:"name" gorm:"type:varchar(255)"`
	Origin      string         `json:"origin" gorm:"type:varchar(255)"`
	URL         string         `json:"url" gorm:"column:url;type:varchar(255)"`
	Status      string         `json:"status" gorm:"type:varchar(255);index"`
	Path        string         `json:"path" gorm:"type:varchar(255)"`
	PathName    string         `json:"path_name" gorm:"type:varchar(255)"`
	Category    string         `json:"category" gorm:"type:varchar(255);index"`
	Tags        datatypes.JSON `json:"tags" gorm:"type:json"`
	Img         string         `json:"img" gorm:"type:varchar(255)"`
	PublishURLs datatypes.JSON `json:"publish_urls" gorm:"column:publish_urls;type:json"`
	Log         string         `json:"log" gorm:"type:longtext"`

	Version string `json:"version" gorm:"type:varchar(255)"`
	Message string `json:"message" gorm:"type:longtext"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Store) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type StoreDTO struct {
	Store
	Installed   bool   `json:"installed"`
	InstalledID string `json:"installed_id,omitempty"`
}

func (Store) TableName() string {
	return "store"
}
