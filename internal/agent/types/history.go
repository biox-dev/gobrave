package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// InitialVersion 是文件第一次被记录时使用的版本号。
const InitialVersion = "initial"

// 文件历史相关错误。
var (
	// ErrHistoryNotFound 表示历史记录不存在。
	ErrHistoryNotFound = errors.New("history: file not found")
	// ErrDuplicateVersion 表示 (session_id, path, version) 发生唯一键冲突，
	// 调用方可据此重新推导版本号后重试。
	ErrDuplicateVersion = errors.New("history: duplicate version")
)

// File 是某个会话中某个文件的一个历史版本。
//
// Path 长度受 MySQL 索引键上限约束：InnoDB（DYNAMIC 行格式）单索引最多 3072 字节，
// 而 DSN 使用 utf8mb4（每个字符最多 4 字节）。Path 同时出现在
//
//	idx_history_session_path        (session_id, path)
//	uk_history_session_path_version (session_id, path, version)
//
// 两个索引里，因此 path 的字符数上限为 (3072 - 8 - 64*4) / 4 = 702。
// 原先声明为 varchar(1024)（4096 字节）会让 AutoMigrate 直接失败并报
// "Error 1071 (42000): Specified key was too long; max key length is 3072 bytes"。
// 这里取 512，为将来调整 version 长度留出余量。
type File struct {
	ID        int64     `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	SessionID int64     `json:"session_id,string" gorm:"column:session_id;type:bigint;index:idx_history_session_path,priority:1"`
	Path      string    `json:"path" gorm:"column:path;type:varchar(512);index:idx_history_session_path,priority:2;uniqueIndex:uk_history_session_path_version,priority:2"`
	Content   string    `json:"content" gorm:"column:content;type:longtext"`
	Version   string    `json:"version" gorm:"column:version;type:varchar(64);uniqueIndex:uk_history_session_path_version,priority:3"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回文件历史表的表名。
func (File) TableName() string { return "agent_history_files" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (f *File) BeforeCreate(_ *gorm.DB) error {
	if f.ID == 0 {
		f.ID = utils.GenerateID()
	}
	return nil
}
