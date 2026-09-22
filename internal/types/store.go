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
	StoreType string `json:"store_type" gorm:"type:varchar(255);index"`
	Name      string `json:"name" gorm:"type:varchar(255)"`
	Origin    string `json:"origin" gorm:"type:varchar(255)"`
	// URL 字段已删除：store 不再保存「远程仓库地址」。
	// 发布到远程（PublishStoreRemote）只把地址写进 store 裸仓库的 git remote 配置，
	// 下载来源地址同样可以从 clone 生成的 origin remote 读回，因此地址不再落库，
	// 避免数据库与仓库真实配置两份状态漂移。
	// 需要展示时读 Remotes（由磁盘实时推导）。
	// Status    string `json:"status" gorm:"type:varchar(255);index"`
	// PathName 是 store 目录在 storage.base_dir/store 下的相对标识，只在首次创建 store 时
	// 由 utils.GenerateStorePathName 生成随机串并固定下来：既不随 workflow_id / script_id 变化，
	// 也不来自下载源地址（历史数据可能仍是 workflow/script ID 或 "<owner>/<repo>"，原样沿用）。
	// 绝对路径一律不落库，需要时用 utils.GetWorkflowOrScriptStoreDir(baseDir, PathName) 解析。
	PathName string `json:"path_name" gorm:"type:varchar(255)"`
	// StorePath 是 PathName 按当前 storage.base_dir 解析出的绝对目录，只在响应里返回。
	// 不落库（gorm:"-"），既避免 base_dir 迁移后库内路径失效，也保证与 PathName 单一来源一致。
	StorePath string `json:"store_path,omitempty" gorm:"-"`
	// Remotes 是 store 裸仓库上配置的远程仓库列表（github / gitee / origin ...），
	// 与 StorePath 一样只在响应里返回、不落库（gorm:"-"）：发布到远程时写入的是
	// store 仓库的 git remote 配置，读取侧统一通过 utils.ReadGitRemotes 实时推导。
	Remotes  []utils.GitRemote `json:"remotes,omitempty" gorm:"-"`
	Category string            `json:"category" gorm:"type:varchar(255);index"`
	Tags     datatypes.JSON    `json:"tags" gorm:"type:json"`
	Img      string            `json:"img" gorm:"type:varchar(255)"`
	// PublishURLs datatypes.JSON `json:"publish_urls" gorm:"column:publish_urls;type:json"`
	Log string `json:"log" gorm:"type:longtext"`

	// Version string `json:"version" gorm:"type:varchar(255)"`
	// Message string `json:"message" gorm:"type:longtext"`

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
