package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

type storeService struct {
	storeRepo      interfaces.StoreRepository
	projectService interfaces.ProjectService
}

func NewStoreService(storeRepo interfaces.StoreRepository, projectService interfaces.ProjectService) interfaces.StoreService {
	return &storeService{storeRepo: storeRepo, projectService: projectService}
}

func (s *storeService) CreateStore(ctx context.Context, item *types.Store) error {
	return s.storeRepo.CreateStore(ctx, item)
}

func (s *storeService) GetStoreByID(ctx context.Context, id int64) (*types.Store, error) {
	return s.storeRepo.GetStoreByID(ctx, id)
}

func (s *storeService) GetStoreByStoreID(ctx context.Context, storeID string) (*types.Store, error) {
	return s.storeRepo.GetStoreByStoreID(ctx, storeID)
}

func (s *storeService) GetStoreByURL(ctx context.Context, rawURL string) (*types.Store, error) {
	return s.storeRepo.GetStoreByURL(ctx, rawURL)
}

func (s *storeService) UpdateStore(ctx context.Context, item *types.Store) error {
	if _, err := s.storeRepo.GetStoreByID(ctx, item.ID); err != nil {
		return err
	}
	return s.storeRepo.UpdateStore(ctx, item)
}

func (s *storeService) DeleteStore(ctx context.Context, id int64) error {
	if _, err := s.storeRepo.GetStoreByID(ctx, id); err != nil {
		return err
	}
	return s.storeRepo.DeleteStore(ctx, id)
}

func (s *storeService) ListStore(ctx context.Context) ([]*types.Store, error) {
	return s.storeRepo.ListStore(ctx)
}

func (s *storeService) PageStore(ctx context.Context, userID string, pagination *types.Pagination, query *types.StorePageQuery) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("user id is required")
	}
	if query == nil {
		return nil, fmt.Errorf("query is required")
	}

	project, err := s.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	items, total, err := s.storeRepo.PageStore(ctx, pagination, query)
	if err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return types.NewPageResult(total, pagination, []*types.StoreDTO{}), nil
	}

	storeIDs := make([]int64, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		storeIDs = append(storeIDs, item.ID)
	}

	dtos := make([]*types.StoreDTO, 0, len(items))
	storeType := query.GetStoreType()

	switch storeType {
	case "workflow":
		installMap, err := s.storeRepo.ListInstalledWorkflowMap(ctx, project.ID, storeIDs)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item == nil {
				continue
			}
			dto := &types.StoreDTO{Store: *item}
			if workflowID, ok := installMap[item.ID]; ok {
				dto.Installed = true
				dto.InstalledID = strconv.FormatUint(uint64(workflowID), 10)
			}
			dtos = append(dtos, dto)
		}
	case "script":
		installMap, err := s.storeRepo.ListInstalledScriptMap(ctx, project.ID, storeIDs)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item == nil {
				continue
			}
			dto := &types.StoreDTO{Store: *item}
			if scriptID, ok := installMap[item.ID]; ok {
				dto.Installed = true
				dto.InstalledID = strconv.FormatInt(scriptID, 10)
			}
			dtos = append(dtos, dto)
		}
	default:
		for _, item := range items {
			if item == nil {
				continue
			}
			dtos = append(dtos, &types.StoreDTO{Store: *item})
		}
	}

	return types.NewPageResult(total, pagination, dtos), nil
}
