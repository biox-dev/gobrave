package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrUserProjectActive indicates the user-project binding is still active and cannot be deleted.
var ErrUserProjectActive = errors.New("user project is active")

type projectService struct {
	projectRepo interfaces.ProjectRepository
}

func NewProjectService(projectRepo interfaces.ProjectRepository) interfaces.ProjectService {
	return &projectService{projectRepo: projectRepo}
}

func (s *projectService) ListProjectByUserID(ctx context.Context, userID string) ([]*types.ProjectListItem, error) {
	return s.projectRepo.ListProjectByUserID(ctx, userID)
}
func (s *projectService) GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error) {
	return s.projectRepo.GetActiveProjectByUserID(ctx, userID)
}

func (s *projectService) GetActiveProjectDirByUserID(ctx context.Context, userID, baseDir string) (*types.Project, string, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, "", fmt.Errorf("storage base dir is empty")
	}

	project, err := s.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	return project, utils.GetProjectDir(baseDir, project.ProjectID), nil
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

func (s *projectService) AddUserProjectByShareCode(ctx context.Context, userID, shareCode string) error {
	shareCode = strings.TrimSpace(shareCode)
	if shareCode == "" {
		return errors.New("share code is empty")
	}

	owner, err := s.projectRepo.GetUserProjectByShareCode(ctx, shareCode)
	if err != nil {
		return err
	}
	if !owner.ShareEnabled {
		return errors.New("project sharing is disabled")
	}

	exists, err := s.projectRepo.ExistsUserProject(ctx, userID, owner.ProjectID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("user already has access to this project")
	}

	return s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: owner.ProjectID,
		CreatedAt: time.Now(),
	})
}

func (s *projectService) UpdateProjectSharing(ctx context.Context, userID, projectID string, enabled bool) (string, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	if !bound {
		return "", gorm.ErrRecordNotFound
	}

	shareCode := ""
	if enabled {
		shareCode = uuid.New().String()
	}

	if err := s.projectRepo.UpdateProjectSharing(ctx, userID, projectID, enabled, shareCode); err != nil {
		return "", err
	}

	return shareCode, nil
}

func (s *projectService) ActivateUserProject(ctx context.Context, userID, projectID string) error {
	return s.projectRepo.ActivateUserProject(ctx, userID, projectID)
}

func (s *projectService) DeleteUserProject(ctx context.Context, userID, projectID string) error {
	up, err := s.projectRepo.GetUserProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if up.IsActive {
		return ErrUserProjectActive
	}
	return s.projectRepo.DeleteUserProject(ctx, userID, projectID)
}

func (s *projectService) CreateDefaultProjectForUser(ctx context.Context, userID, username string) error {
	projectID := uuid.New().String()
	projectName := fmt.Sprintf("%s's Project", username)

	project := &types.Project{
		ID:          utils.GenerateID(),
		ProjectID:   projectID,
		ProjectName: projectName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return fmt.Errorf("failed to create default project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: projectID,
		IsActive:  true,
		CreatedAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("failed to link user to project: %w", err)
	}

	return nil
}

func (s *projectService) CreateProjectForUser(ctx context.Context, userID string, project *types.Project) (*types.Project, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}

	if strings.TrimSpace(project.ProjectName) == "" {
		return nil, errors.New("project name is empty")
	}

	if project.ProjectID == "" {
		project.ProjectID = uuid.New().String()
	}

	now := time.Now()
	project.CreatedAt = now
	project.UpdatedAt = now

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: project.ProjectID,
		CreatedAt: now,
	}); err != nil {
		return nil, fmt.Errorf("failed to link user to project: %w", err)
	}

	// if err := s.projectRepo.ActivateUserProject(ctx, userID, project.ProjectID); err != nil {
	// 	return nil, fmt.Errorf("failed to activate project: %w", err)
	// }

	return project, nil
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

func (s *projectService) PageProjectReportByProjectID(ctx context.Context, userID, projectID string, pagination *types.Pagination) ([]*types.ProjectReport, int64, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, 0, err
	}
	if !bound {
		return nil, 0, gorm.ErrRecordNotFound
	}

	return s.projectRepo.PageProjectReportByProjectID(ctx, pagination, projectID)
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

func (s *projectService) GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error) {
	return s.projectRepo.GetProjectReportByID(ctx, reportID)
}
