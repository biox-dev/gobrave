package service

import (
	"context"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type llmService struct {
	llmRepo    interfaces.LLMRepository
	projectSvc interfaces.ProjectService
}

func NewLLMService(llmRepo interfaces.LLMRepository, projectSvc interfaces.ProjectService) interfaces.LLMService {
	return &llmService{llmRepo: llmRepo, projectSvc: projectSvc}
}

func (s *llmService) CreateLLMSession(ctx context.Context, userID string, session *types.LLMSession) error {
	return s.llmRepo.CreateLLMSession(ctx, session)
}

func (s *llmService) GetLLMSessionByID(ctx context.Context, userID string, id int64) (*types.LLMSession, error) {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, id, projectID)
}

func (s *llmService) UpdateLLMSession(ctx context.Context, userID string, session *types.LLMSession) error {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return err
	}
	current, err := s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, session.ID, projectID)
	if err != nil {
		return err
	}

	session.ProjectID = current.ProjectID
	return s.llmRepo.UpdateLLMSession(ctx, session)
}

func (s *llmService) DeleteLLMSession(ctx context.Context, userID string, id int64) error {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, id, projectID); err != nil {
		return err
	}
	return s.llmRepo.DeleteLLMSessionWithRelations(ctx, id)
}

func (s *llmService) ListLLMSession(ctx context.Context, userID string) ([]*types.LLMSession, error) {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.llmRepo.ListLLMSessionByProjectID(ctx, projectID)
}

func (s *llmService) CreateLLMConversation(ctx context.Context, userID string, conversation *types.LLMConversation) error {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, conversation.LLMSessionID, projectID); err != nil {
		return err
	}
	return s.llmRepo.CreateLLMConversation(ctx, conversation)
}

func (s *llmService) GetLLMConversationByID(ctx context.Context, userID string, id int64) (*types.LLMConversation, error) {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.llmRepo.GetLLMConversationByIDAndProjectID(ctx, id, projectID)
}

func (s *llmService) UpdateLLMConversation(ctx context.Context, userID string, conversation *types.LLMConversation) error {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return err
	}
	current, err := s.llmRepo.GetLLMConversationByIDAndProjectID(ctx, conversation.ID, projectID)
	if err != nil {
		return err
	}

	if conversation.LLMSessionID != current.LLMSessionID {
		if _, err := s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, conversation.LLMSessionID, projectID); err != nil {
			return err
		}
	}

	return s.llmRepo.UpdateLLMConversation(ctx, conversation)
}

func (s *llmService) DeleteLLMConversation(ctx context.Context, userID string, id int64) error {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.llmRepo.GetLLMConversationByIDAndProjectID(ctx, id, projectID); err != nil {
		return err
	}
	return s.llmRepo.DeleteLLMConversation(ctx, id)
}

func (s *llmService) ListLLMConversationBySessionID(ctx context.Context, userID string, llmSessionID int64) ([]*types.LLMConversation, error) {
	projectID, err := s.resolveActiveProjectID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if _, err := s.llmRepo.GetLLMSessionByIDAndProjectID(ctx, llmSessionID, projectID); err != nil {
		return nil, err
	}
	items, err := s.llmRepo.ListLLMConversationBySessionID(ctx, llmSessionID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return []*types.LLMConversation{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *llmService) resolveActiveProjectID(ctx context.Context, userID string) (int64, error) {
	project, err := s.projectSvc.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return project.ID, nil
}
