package service

import (
	"context"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// messageService 提供消息的增删改查实现。
type messageService struct {
	store MessageStore
}

// NewMessageService 创建消息服务。
func NewMessageService(store MessageStore) MessageService {
	return &messageService{store: store}
}

func (s *messageService) Create(ctx context.Context, params MessageCreateParams) (Message, error) {
	message := &Message{
		SessionID: params.SessionID,
		Role:      params.Role,
		Kind:      params.Kind,
		TaskID:    params.TaskID,
		Content:   params.Content,
		Data:      params.Data,
		CreatedAt: time.Now(),
	}
	if err := s.store.Create(ctx, message); err != nil {
		return Message{}, err
	}
	return *message, nil
}

func (s *messageService) Update(ctx context.Context, message Message) (Message, error) {
	if err := s.store.Update(ctx, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *messageService) Get(ctx context.Context, id int64) (Message, error) {
	message, err := s.store.Get(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return *message, nil
}

func (s *messageService) List(ctx context.Context, sessionID int64) ([]Message, error) {
	rows, err := s.store.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	return out, nil
}

func (s *messageService) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

func (s *messageService) DeleteSessionMessages(ctx context.Context, sessionID int64) error {
	return s.store.DeleteBySession(ctx, sessionID)
}
