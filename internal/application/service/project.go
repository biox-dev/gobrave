package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
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
	cfg         *config.Config
}

func NewProjectService(projectRepo interfaces.ProjectRepository, cfg *config.Config) interfaces.ProjectService {
	return &projectService{projectRepo: projectRepo, cfg: cfg}
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

	s.ensureProjectReportDefaults(report)
	if report.ID == 0 {
		report.ID = utils.GenerateID()
	}

	if report.ContentSource == types.ProjectReportContentSourceFile {
		report.Content = ""
	}

	if err := s.projectRepo.AddProjectReport(ctx, report); err != nil {
		return err
	}

	if report.ContentSource == types.ProjectReportContentSourceFile {
		return s.ensureProjectReportFile(report)
	}

	return nil
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

	s.ensureProjectReportDefaults(stored)

	// Resolve the target storage settings. Keep the stored values unless the
	// caller explicitly switches the source or filename.
	targetSource := strings.TrimSpace(report.ContentSource)
	if targetSource == "" ||
		(targetSource != types.ProjectReportContentSourceFile && targetSource != types.ProjectReportContentSourceDatabase) {
		targetSource = stored.ContentSource
	}

	targetFilename := strings.TrimSpace(report.Filename)
	if targetFilename == "" {
		targetFilename = stored.Filename
	}
	if targetFilename == "" {
		targetFilename = types.DefaultProjectReportFilename
	}

	report.ContentSource = targetSource
	report.Filename = targetFilename

	switch {
	case stored.ContentSource == types.ProjectReportContentSourceFile && targetSource == types.ProjectReportContentSourceDatabase:
		// file -> database: write the file content into the database.
		content, err := s.readProjectReportFile(stored)
		if err != nil {
			if os.IsNotExist(err) {
				content = report.Content
			} else {
				return err
			}
		}
		report.Content = content

	case stored.ContentSource == types.ProjectReportContentSourceDatabase && targetSource == types.ProjectReportContentSourceFile:
		// database -> file: write the database content into the file.
		report.Content = stored.Content
		if err := s.writeProjectReportFile(report); err != nil {
			return err
		}
		report.Content = ""

	default:
		// Same-source update.
		if targetSource == types.ProjectReportContentSourceFile {
			if err := s.writeProjectReportFile(report); err != nil {
				return err
			}
			report.Content = ""
		}
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

	if report.ContentSource == types.ProjectReportContentSourceFile {
		if err := s.deleteProjectReportFile(report); err != nil {
			return err
		}
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

	s.ensureProjectReportDefaults(report)

	if report.ContentSource == types.ProjectReportContentSourceFile {
		content, err := s.readProjectReportFile(report)
		if err != nil {
			if os.IsNotExist(err) {
				// Fall back to the database content for legacy reports whose file
				// has not been initialized yet.
				return report, nil
			}
			return nil, err
		}
		report.Content = content
	}

	return report, nil
}

func (s *projectService) GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error) {
	return s.projectRepo.GetProjectReportByID(ctx, reportID)
}

func (s *projectService) ensureProjectReportDefaults(report *types.ProjectReport) {
	if report == nil {
		return
	}
	if report.ContentSource != types.ProjectReportContentSourceDatabase &&
		report.ContentSource != types.ProjectReportContentSourceFile {
		report.ContentSource = types.ProjectReportContentSourceFile
	}
	if strings.TrimSpace(report.Filename) == "" {
		report.Filename = types.DefaultProjectReportFilename
	}
}

func (s *projectService) projectReportFilePath(report *types.ProjectReport) (string, error) {
	if s.cfg == nil || s.cfg.Storage == nil {
		return "", errors.New("storage config is missing")
	}

	baseDir := strings.TrimSpace(s.cfg.Storage.BaseDir)
	if baseDir == "" {
		return "", errors.New("storage base dir is empty")
	}

	filename := strings.TrimSpace(report.Filename)
	if filename == "" {
		filename = types.DefaultProjectReportFilename
	}
	if filepath.Base(filename) != filename {
		return "", errors.New("invalid report filename")
	}

	dir := utils.GetProjectReportDir(baseDir, report.ProjectID, strconv.FormatInt(report.ID, 10))
	return filepath.Join(dir, filename), nil
}

func (s *projectService) ensureProjectReportFile(report *types.ProjectReport) error {
	filePath, err := s.projectReportFilePath(report)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

func (s *projectService) writeProjectReportFile(report *types.ProjectReport) error {
	filePath, err := s.projectReportFilePath(report)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(filePath, []byte(report.Content), 0o644)
}

func (s *projectService) readProjectReportFile(report *types.ProjectReport) (string, error) {
	filePath, err := s.projectReportFilePath(report)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *projectService) deleteProjectReportFile(report *types.ProjectReport) error {
	filePath, err := s.projectReportFilePath(report)
	if err != nil {
		return err
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}

	// Best-effort cleanup of the now-empty per-report directory.
	_ = os.Remove(filepath.Dir(filePath))
	return nil
}
