package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/history"
	"gorm.io/gorm"
)

// NewGormHistoryStore 创建基于数据库的文件历史 Store。
func NewGormHistoryStore(db *gorm.DB) history.Store {
	return &gormHistoryStore{db: db}
}

type gormHistoryStore struct {
	db *gorm.DB
}

func (r *gormHistoryStore) Create(ctx context.Context, file *history.File) error {
	err := r.db.WithContext(ctx).Create(file).Error
	if err != nil && isUniqueViolation(err) {
		return history.ErrDuplicateVersion
	}
	return err
}

func (r *gormHistoryStore) Update(ctx context.Context, file *history.File) error {
	return r.db.WithContext(ctx).Model(file).
		Updates(map[string]any{
			"content":    file.Content,
			"version":    file.Version,
			"updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error
}

func (r *gormHistoryStore) Get(ctx context.Context, id int64) (*history.File, error) {
	var file history.File
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&file).Error; err != nil {
		return nil, wrapHistoryNotFound(err)
	}
	return &file, nil
}

func (r *gormHistoryStore) GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*history.File, error) {
	var file history.File
	err := r.db.WithContext(ctx).
		Where("path = ? AND session_id = ?", path, sessionID).
		Order("created_at DESC").
		First(&file).Error
	if err != nil {
		return nil, wrapHistoryNotFound(err)
	}
	return &file, nil
}

func (r *gormHistoryStore) ListBySession(ctx context.Context, sessionID int64) ([]*history.File, error) {
	var files []*history.File
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

func (r *gormHistoryStore) ListByPathAndSession(ctx context.Context, path string, sessionID int64) ([]*history.File, error) {
	var files []*history.File
	if err := r.db.WithContext(ctx).
		Where("path = ? AND session_id = ?", path, sessionID).
		Order("created_at DESC").
		Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

func (r *gormHistoryStore) ListLatestBySession(ctx context.Context, sessionID int64) ([]*history.File, error) {
	latest := r.db.WithContext(ctx).
		Model(&history.File{}).
		Select("path, MAX(created_at) AS max_created_at").
		Where("session_id = ?", sessionID).
		Group("path")

	var files []*history.File
	err := r.db.WithContext(ctx).
		Table("agent_history_files AS f").
		Joins("INNER JOIN (?) AS latest ON f.path = latest.path AND f.created_at = latest.max_created_at", latest).
		Where("f.session_id = ?", sessionID).
		Order("f.path").
		Find(&files).Error
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r *gormHistoryStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&history.File{}).Error
}

func (r *gormHistoryStore) DeleteBySession(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&history.File{}).Error
}

// wrapHistoryNotFound 把 GORM 的未找到错误转换为 history.ErrNotFound。
func wrapHistoryNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return history.ErrNotFound
	}
	return err
}

// isUniqueViolation 判断错误是否为唯一键冲突；history.File 的版本冲突依赖它触发重试。
//
// GORM 未开启 TranslateError 时不会返回 gorm.ErrDuplicatedKey，因此兼容主流驱动的
// 原始错误文案（SQLite / MySQL / PostgreSQL）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unique constraint failed",
		"duplicate entry",
		"duplicate key value",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
