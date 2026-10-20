// Package history 记录 Agent 会话中文件的读写版本（从 opencode internal/history 迁移）。
//
// 每次 Write / Edit / Patch 都会为「同一会话 + 同一路径」生成一个新版本，
// 因此可以回溯会话中某个文件的历史内容（编辑前的原文），也便于工具做
// “这条编辑是否真的改变了文件”之类的一致性判断。
//
// 分层与 session / message 包保持一致：
//   - 本包只包含领域模型、Service 接口与版本推导逻辑；
//   - 持久化由 Store 抽象，GORM 实现放在 internal/agent/repository；
//   - SessionID 使用 gobrave 的 int64 会话主键（不是 opencode 的字符串 ID），
//     会话删除时由 repository 级联清理历史（与 message 的级联方式一致）。
package history

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// InitialVersion 是文件第一次被记录时使用的版本号。
const InitialVersion = "initial"

// 文件历史相关错误。
var (
	// ErrNotFound 表示历史记录不存在。
	ErrNotFound = errors.New("history: file not found")
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

// Store 是文件历史的持久化抽象（实现见 internal/agent/repository）。
type Store interface {
	// Create 插入一条历史记录。
	Create(ctx context.Context, file *File) error
	// Update 更新内容 / 版本 / 更新时间。
	Update(ctx context.Context, file *File) error
	// Get 按主键读取。
	Get(ctx context.Context, id int64) (*File, error)
	// GetByPathAndSession 读取指定会话中某个路径的最新版本。
	GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*File, error)
	// ListBySession 按创建时间升序列出会话的全部历史。
	ListBySession(ctx context.Context, sessionID int64) ([]*File, error)
	// ListByPathAndSession 按创建时间降序列出某路径的全部历史（最新在前）。
	ListByPathAndSession(ctx context.Context, path string, sessionID int64) ([]*File, error)
	// ListLatestBySession 列出会话中每个路径的最新版本。
	ListLatestBySession(ctx context.Context, sessionID int64) ([]*File, error)
	// Delete 按主键删除。
	Delete(ctx context.Context, id int64) error
	// DeleteBySession 删除会话的全部历史。
	DeleteBySession(ctx context.Context, sessionID int64) error
}

// Service 暴露文件历史的读写能力。
//
// 注意：opencode 版本的 Service 内嵌了 pubsub.Suscriber[File]（TUI 订阅文件变更用）。
// gobrave 的 UI 实时通知统一走 agent.EventBus，且当前没有 history 事件的消费者，
// 因此这里不迁移 Broker，保持纯 CRUD，避免多一套无用的发布订阅链路。
type Service interface {
	// Create 记录文件的初始版本。
	Create(ctx context.Context, sessionID int64, path, content string) (*File, error)
	// CreateVersion 记录文件的新版本（版本号自动递增）。
	CreateVersion(ctx context.Context, sessionID int64, path, content string) (*File, error)
	// Get 按主键读取。
	Get(ctx context.Context, id int64) (*File, error)
	// GetByPathAndSession 读取会话中某路径的最新版本。
	GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*File, error)
	// ListBySession 列出会话的全部历史。
	ListBySession(ctx context.Context, sessionID int64) ([]*File, error)
	// ListLatestSessionFiles 列出会话中每个路径的最新版本。
	ListLatestSessionFiles(ctx context.Context, sessionID int64) ([]*File, error)
	// Update 更新一条历史记录的内容与版本。
	Update(ctx context.Context, file *File) (*File, error)
	// Delete 删除一条历史记录。
	Delete(ctx context.Context, id int64) error
	// DeleteSessionFiles 删除会话的全部历史。
	DeleteSessionFiles(ctx context.Context, sessionID int64) error
}

// NewService 基于 Store 构建文件历史服务。
func NewService(store Store) Service {
	return &service{store: store}
}

type service struct {
	store Store
}

func (s *service) Create(ctx context.Context, sessionID int64, path, content string) (*File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

func (s *service) CreateVersion(ctx context.Context, sessionID int64, path, content string) (*File, error) {
	versions, err := s.store.ListByPathAndSession(ctx, path, sessionID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return s.Create(ctx, sessionID, path, content)
	}
	return s.createWithVersion(ctx, sessionID, path, content, nextVersion(versions[0]))
}

// nextVersion 由最新版本推导下一个版本号：initial -> v1 -> v2 -> ...
// 版本号格式异常时退化为基于时间戳的版本（与 opencode 行为一致）。
func nextVersion(latest *File) string {
	if latest.Version == InitialVersion {
		return "v1"
	}
	if v, ok := parseVersion(latest.Version); ok {
		return fmt.Sprintf("v%d", v+1)
	}
	return fmt.Sprintf("v%d", latest.CreatedAt.Unix())
}

// parseVersion 解析 v<number> 形式的版本号。
func parseVersion(version string) (int, bool) {
	if len(version) < 2 || version[0] != 'v' {
		return 0, false
	}
	n, err := strconv.Atoi(version[1:])
	return n, err == nil
}

// createWithVersion 写入指定版本；遇到唯一键冲突时重试（最多 maxRetries 次）。
func (s *service) createWithVersion(ctx context.Context, sessionID int64, path, content, version string) (*File, error) {
	const maxRetries = 3

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		file := &File{
			SessionID: sessionID,
			Path:      path,
			Content:   content,
			Version:   version,
		}
		if err := s.store.Create(ctx, file); err != nil {
			lastErr = err
			if !errors.Is(err, ErrDuplicateVersion) {
				return nil, err
			}
			version = bumpVersion(version)
			continue
		}
		return file, nil
	}
	return nil, lastErr
}

// bumpVersion 在冲突后生成下一个候选版本号。
func bumpVersion(version string) string {
	if v, ok := parseVersion(version); ok {
		return fmt.Sprintf("v%d", v+1)
	}
	return fmt.Sprintf("v%d", time.Now().Unix())
}

func (s *service) Get(ctx context.Context, id int64) (*File, error) {
	return s.store.Get(ctx, id)
}

func (s *service) GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*File, error) {
	return s.store.GetByPathAndSession(ctx, path, sessionID)
}

func (s *service) ListBySession(ctx context.Context, sessionID int64) ([]*File, error) {
	return s.store.ListBySession(ctx, sessionID)
}

func (s *service) ListLatestSessionFiles(ctx context.Context, sessionID int64) ([]*File, error) {
	return s.store.ListLatestBySession(ctx, sessionID)
}

func (s *service) Update(ctx context.Context, file *File) (*File, error) {
	if err := s.store.Update(ctx, file); err != nil {
		return nil, err
	}
	return file, nil
}

func (s *service) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

func (s *service) DeleteSessionFiles(ctx context.Context, sessionID int64) error {
	return s.store.DeleteBySession(ctx, sessionID)
}
