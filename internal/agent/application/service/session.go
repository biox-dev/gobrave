package service

import (
	"context"
	"fmt"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// SessionService 提供会话的增删改查实现。
type sessionService struct {
	store SessionStore
}

// NewSessionService 创建会话服务。
func NewSessionService(store SessionStore) SessionService {
	return &sessionService{store: store}
}

func (s *sessionService) Create(ctx context.Context, params SessionCreateParams) (Session, error) {
	now := time.Now()
	session := &Session{
		ID:              params.ID,
		ProjectID:       params.ProjectID,
		ParentSessionID: params.ParentSessionID,
		Title:           params.Title,
		UserID:          params.UserID,
		Provider:        params.Provider,
		Model:           params.Model,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.store.Create(ctx, session); err != nil {
		return Session{}, err
	}
	return *session, nil
}

func (s *sessionService) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID int64, title string) (Session, error) {
	return s.Create(ctx, SessionCreateParams{
		ID:              toolCallID,
		ParentSessionID: parentSessionID,
		Title:           title,
	})
}

// CreateChildSession 创建 ParentSessionID 指向父会话的子会话。
func (s *sessionService) CreateChildSession(ctx context.Context, params SessionChildParams) (Session, error) {
	if params.ParentSessionID <= 0 {
		return Session{}, fmt.Errorf("%w: %d", ErrInvalidParentSession, params.ParentSessionID)
	}
	return s.Create(ctx, SessionCreateParams{
		ProjectID:       params.ProjectID,
		ParentSessionID: params.ParentSessionID,
		Title:           params.Title,
		UserID:          params.UserID,
		Provider:        params.Provider,
		Model:           params.Model,
	})
}

func (s *sessionService) CreateTitleSession(ctx context.Context, parentSessionID int64) (Session, error) {
	return s.Create(ctx, SessionCreateParams{
		ParentSessionID: parentSessionID,
		Title:           TitleSessionTitle,
	})
}

func (s *sessionService) Get(ctx context.Context, id int64) (Session, error) {
	session, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return *session, nil
}

func (s *sessionService) List(ctx context.Context, params SessionListParams) ([]Session, int64, error) {
	rows, total, err := s.store.List(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	return out, total, nil
}

func (s *sessionService) Save(ctx context.Context, session Session) (Session, error) {
	session.UpdatedAt = time.Now()
	if err := s.store.Update(ctx, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *sessionService) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}
