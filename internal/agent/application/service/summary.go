package service

import (
	"context"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// sessionSummaryService 提供会话历史摘要的读写实现。
type sessionSummaryService struct {
	store SessionSummaryStore
}

// NewSessionSummaryService 创建会话摘要服务。
func NewSessionSummaryService(store SessionSummaryStore) SessionSummaryService {
	return &sessionSummaryService{store: store}
}

func (s *sessionSummaryService) Get(ctx context.Context, sessionID int64) (*SessionSummary, error) {
	if s == nil || s.store == nil || sessionID <= 0 {
		return nil, nil
	}
	return s.store.Get(ctx, sessionID)
}

func (s *sessionSummaryService) Upsert(ctx context.Context, summary *SessionSummary) error {
	if s == nil || s.store == nil || summary == nil {
		return nil
	}
	return s.store.Upsert(ctx, summary)
}
