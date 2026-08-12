package service

import (
	"context"
	stderrs "errors"
	"fmt"
	"strings"

	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type containerService struct {
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager
}

func NewContainerService(containerRepo interfaces.ContainerRepository, containerMgr *manager.ContainerManager) interfaces.ContainerService {
	return &containerService{containerRepo: containerRepo, containerMgr: containerMgr}
}

func (s *containerService) CreateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	return s.containerRepo.CreateContainerImage(ctx, item)
}

func (s *containerService) GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error) {
	return s.containerRepo.GetContainerImageByID(ctx, id)
}

func (s *containerService) UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	if _, err := s.containerRepo.GetContainerImageByID(ctx, item.ID); err != nil {
		return err
	}
	return s.containerRepo.UpdateContainerImage(ctx, item)
}

func (s *containerService) DeleteContainerImage(ctx context.Context, id int64) error {
	if _, err := s.containerRepo.GetContainerImageByID(ctx, id); err != nil {
		return err
	}
	return s.containerRepo.DeleteContainerImage(ctx, id)
}

func (s *containerService) ListContainerImage(ctx context.Context) ([]*types.ContainerImage, error) {
	return s.containerRepo.ListContainerImage(ctx)
}

func (s *containerService) PageContainerImage(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerImage(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *containerService) CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	if _, err := s.containerRepo.GetContainerImageByID(ctx, item.ImageID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return err
	}
	return s.containerRepo.CreateContainerTemplate(ctx, item)
}

func (s *containerService) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	return s.containerRepo.GetContainerTemplateByID(ctx, id)
}

func (s *containerService) UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	if _, err := s.containerRepo.GetContainerTemplateByID(ctx, item.ID); err != nil {
		return err
	}
	if _, err := s.containerRepo.GetContainerImageByID(ctx, item.ImageID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return err
	}
	return s.containerRepo.UpdateContainerTemplate(ctx, item)
}

func (s *containerService) DeleteContainerTemplate(ctx context.Context, id int64) error {
	if _, err := s.containerRepo.GetContainerTemplateByID(ctx, id); err != nil {
		return err
	}
	return s.containerRepo.DeleteContainerTemplate(ctx, id)
}

func (s *containerService) ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error) {
	return s.containerRepo.ListContainerTemplate(ctx)
}

func (s *containerService) PageContainerTemplate(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerTemplate(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *containerService) CreateAppSessionByTemplate(ctx context.Context, userID string, projectID int64, containerTemplateID int64, name string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, 0, "")
}

func (s *containerService) CreateAppSessionByTemplateForAnalysisNode(ctx context.Context, userID string, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, analysisNodeID, workspacePath)
}

func (s *containerService) createAppSessionByTemplate(ctx context.Context, userID string, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("user id is required")
	}
	if projectID == 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if containerTemplateID == 0 {
		return nil, fmt.Errorf("container template id is required")
	}

	tpl, err := s.containerRepo.GetContainerTemplateByID(ctx, containerTemplateID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("app-session-%d-%d", projectID, containerTemplateID)
		if strings.TrimSpace(tpl.Name) != "" {
			name = fmt.Sprintf("%s-%d", strings.TrimSpace(tpl.Name), projectID)
		}
	}

	session := &types.AppSession{
		UserID:              userID,
		ProjectID:           projectID,
		AnalysisNodeID:      analysisNodeID,
		ContainerTemplateID: containerTemplateID,
		Name:                name,
		AppType:             tpl.AppType,
		Status:              "PENDING_CREATION",
		WorkspacePath:       strings.TrimSpace(workspacePath),
	}
	if err := s.containerRepo.CreateAppSession(ctx, session); err != nil {
		return nil, err
	}

	_, err = s.containerMgr.CreateByTemplate(ctx, "", containerTemplateID, types.ContainerOwnerAppSession, session.ID, name)
	if err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return nil, err
	}

	return session, nil
}

func (s *containerService) StartAppSession(ctx context.Context, userID string, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Start(ctx, inst.ID); err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	session.Status = "START_PENDING"
	session.StoppedAt = nil
	return s.containerRepo.UpdateAppSession(ctx, session)
}

func (s *containerService) StopAppSession(ctx context.Context, userID string, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Stop(ctx, inst.ID); err != nil {
		if err == types.ErrContainerAlreadyStopped {
			session.Status = "STOPPED"
			return s.containerRepo.UpdateAppSession(ctx, session)
		}
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	// now := time.Now()
	// session.Status = "STOPPED"
	// session.StoppedAt = &now
	// s.containerRepo.UpdateAppSession(ctx, session)
	return nil
}

func (s *containerService) DeleteAppSession(ctx context.Context, userID string, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}

	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil {
			return err
		}
	}

	return s.containerRepo.DeleteAppSession(ctx, session.ID)
}

func (s *containerService) GetAppSessionByID(ctx context.Context, userID string, appSessionID int64) (*types.AppSession, error) {
	return s.ensureOwnedAppSession(ctx, userID, appSessionID)
}

func (s *containerService) ListAppSessionByUserID(ctx context.Context, userID string) ([]*types.AppSession, error) {
	items, err := s.containerRepo.ListAppSession(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]*types.AppSession, 0)
	for _, item := range items {
		if item.UserID == userID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (s *containerService) PageAppSessionByUserID(ctx context.Context, userID string, pagination *types.Pagination, query *types.AppSessionPageQuery) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageAppSessionByUserID(ctx, userID, pagination, query)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *containerService) ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error) {
	return s.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, ownerType, ownerIDs)
}

func (s *containerService) DeleteContainerInstancesByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) error {
	if len(ownerIDs) == 0 {
		return nil
	}
	instances, err := s.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, ownerType, ownerIDs)
	if err != nil {
		return err
	}

	var firstErr error
	for _, inst := range instances {
		if inst == nil || inst.ID == 0 {
			continue
		}
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (s *containerService) PageContainerInstance(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerInstance(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

// func (s *containerService) PageContainerEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
// 	if pagination == nil {
// 		pagination = &types.Pagination{}
// 	}

// 	items, total, err := s.containerRepo.PageContainerEvent(ctx, pagination)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return types.NewPageResult(total, pagination, items), nil
// }

func (s *containerService) PageOutboxEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageOutboxEvent(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *containerService) ensureOwnedAppSession(ctx context.Context, userID string, appSessionID int64) (*types.AppSession, error) {
	session, err := s.containerRepo.GetAppSessionByID(ctx, appSessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != userID {
		return nil, gorm.ErrRecordNotFound
	}
	return session, nil
}
