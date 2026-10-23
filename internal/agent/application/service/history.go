package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// historyService 暴露文件历史的读写实现。
type historyService struct {
	store HistoryStore
}

// NewHistoryService 基于 Store 构建文件历史服务。
func NewHistoryService(store HistoryStore) HistoryService {
	return &historyService{store: store}
}

func (s *historyService) Create(ctx context.Context, sessionID int64, path, content string) (*File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

func (s *historyService) CreateVersion(ctx context.Context, sessionID int64, path, content string) (*File, error) {
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
func (s *historyService) createWithVersion(ctx context.Context, sessionID int64, path, content, version string) (*File, error) {
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

func (s *historyService) Get(ctx context.Context, id int64) (*File, error) {
	return s.store.Get(ctx, id)
}

func (s *historyService) GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*File, error) {
	return s.store.GetByPathAndSession(ctx, path, sessionID)
}

func (s *historyService) ListBySession(ctx context.Context, sessionID int64) ([]*File, error) {
	return s.store.ListBySession(ctx, sessionID)
}

func (s *historyService) ListLatestSessionFiles(ctx context.Context, sessionID int64) ([]*File, error) {
	return s.store.ListLatestBySession(ctx, sessionID)
}

func (s *historyService) Update(ctx context.Context, file *File) (*File, error) {
	if err := s.store.Update(ctx, file); err != nil {
		return nil, err
	}
	return file, nil
}

func (s *historyService) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

func (s *historyService) DeleteSessionFiles(ctx context.Context, sessionID int64) error {
	return s.store.DeleteBySession(ctx, sessionID)
}
