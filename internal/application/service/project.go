package service

import (
	"context"
	"errors"
	"time"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type projectService struct {
	projectRepo interfaces.ProjectRepository
}

func NewProjectService(projectRepo interfaces.ProjectRepository) interfaces.ProjectService {
	return &projectService{projectRepo: projectRepo}
}

func (s *projectService) ListProjectByUserID(ctx context.Context, userID string) ([]*types.Project, error) {
	return s.projectRepo.ListProjectByUserID(ctx, userID)
}
func (s *projectService) GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error) {
	return s.projectRepo.GetActiveProjectByUserID(ctx, userID)
}

func (s *projectService) GetProjectByID(ctx context.Context, id int64) (*types.Project, error) {
	return s.projectRepo.GetProjectByID(ctx, id)
}

func (s *projectService) AddUserProject(ctx context.Context, userID, projectID string) error {
	exists, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("user already has access to this project")
	}
	return s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: projectID,
		CreatedAt: time.Now(),
	})
}

func (s *projectService) ActivateUserProject(ctx context.Context, userID, projectID string) error {
	return s.projectRepo.ActivateUserProject(ctx, userID, projectID)
}

func (s *projectService) AddProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	return s.projectRepo.AddProjectReport(ctx, report)
}

func (s *projectService) UpdateProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	stored, err := s.projectRepo.GetProjectReportByID(ctx, report.ID)
	if err != nil {
		return err
	}
	if stored.ProjectID != report.ProjectID {
		return gorm.ErrRecordNotFound
	}

	return s.projectRepo.UpdateProjectReport(ctx, report)
}

func (s *projectService) DeleteProjectReport(ctx context.Context, userID string, reportID int64) error {
	report, err := s.projectRepo.GetProjectReportByID(ctx, reportID)
	if err != nil {
		return err
	}

	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	return s.projectRepo.DeleteProjectReport(ctx, report.ProjectID, reportID)
}

func (s *projectService) ListProjectReportByProjectID(ctx context.Context, userID, projectID string) ([]*types.ProjectReport, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, gorm.ErrRecordNotFound
	}

	return s.projectRepo.ListProjectReportByProjectID(ctx, projectID)
}

func (s *projectService) GetProjectReportDetailByID(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, error) {
	report, err := s.projectRepo.GetProjectReportByID(ctx, reportID)
	if err != nil {
		return nil, err
	}

	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, gorm.ErrRecordNotFound
	}

	return report, nil
}
