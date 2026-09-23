package service

import (
	"context"
	stderrs "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type dataService struct {
	dataRepo interfaces.DataRepository
	baseDir  string
}

var ErrDatasetFileAlreadyAdded = stderrs.New("dataset file already added")

func NewDataService(cfg *config.Config, dataRepo interfaces.DataRepository) interfaces.DataService {
	baseDir := ""
	if cfg != nil && cfg.Storage != nil {
		baseDir = strings.TrimSpace(cfg.Storage.BaseDir)
	}

	return &dataService{dataRepo: dataRepo, baseDir: baseDir}
}

func (s *dataService) CreateDataset(ctx context.Context, dataset *types.Dataset, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return gorm.ErrRecordNotFound
	}

	projectExists, err := s.dataRepo.ExistsProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	if !projectExists {
		return gorm.ErrRecordNotFound
	}

	// The dataset and its project binding must be created atomically so that a
	// dataset never exists without being attached to the active project.
	err = s.dataRepo.WithTransaction(ctx, func(tx interfaces.DataRepository) error {
		if err := tx.CreateDataset(ctx, dataset); err != nil {
			return err
		}

		return tx.CreateProjectDataset(ctx, &types.ProjectDataset{
			ProjectID: projectID,
			DatasetID: dataset.ID,
		})
	})
	if err != nil {
		return err
	}

	// Ensure the dataset workspace directory exists (created lazily on first
	// use by downstream steps otherwise).
	return s.ensureDatasetDir(projectID, dataset.ID)
}

// EnsureDatasetDir validates that the dataset exists and creates the dataset
// workspace directory (utils.GetDatasetDir) when it is missing. It returns the
// dataset directory path so callers can use it directly.
func (s *dataService) EnsureDatasetDir(ctx context.Context, datasetID int64, projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if datasetID == 0 || projectID == "" {
		return "", gorm.ErrRecordNotFound
	}

	if _, err := s.dataRepo.GetDatasetByID(ctx, datasetID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return "", gorm.ErrRecordNotFound
		}
		return "", err
	}

	if strings.TrimSpace(s.baseDir) == "" {
		return "", fmt.Errorf("storage base dir is required")
	}

	if err := s.ensureDatasetDir(projectID, datasetID); err != nil {
		return "", err
	}

	return utils.GetDatasetDir(s.baseDir, projectID, datasetID), nil
}

// ensureDatasetDir creates utils.GetDatasetDir(baseDir, projectID, datasetID)
// when it is missing. A blank baseDir disables directory provisioning.
func (s *dataService) ensureDatasetDir(projectID string, datasetID int64) error {
	if strings.TrimSpace(s.baseDir) == "" {
		return nil
	}

	dir := utils.GetDatasetDir(s.baseDir, projectID, datasetID)
	info, err := os.Stat(dir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("dataset path %q already exists and is not a directory", dir)
	case !os.IsNotExist(err):
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create dataset directory %q: %w", dir, err)
	}
	return nil
}

func (s *dataService) GetDatasetByID(ctx context.Context, id int64) (*types.Dataset, error) {
	return s.dataRepo.GetDatasetByID(ctx, id)
}

func (s *dataService) UpdateDataset(ctx context.Context, dataset *types.Dataset) error {
	_, err := s.dataRepo.GetDatasetByID(ctx, dataset.ID)
	if err != nil {
		return err
	}
	return s.dataRepo.UpdateDataset(ctx, dataset)
}

func (s *dataService) DeleteDataset(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetDatasetByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteDatasetWithRelations(ctx, id)
}

func (s *dataService) ListDataset(ctx context.Context) ([]*types.Dataset, error) {
	return s.dataRepo.ListDataset(ctx)
}

func (s *dataService) PageDatasetByProjectID(ctx context.Context, pagination *types.Pagination, query *types.QueryDataset, projectID string) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageDatasetByProjectID(ctx, pagination, query, projectID)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) PageFileByProjectID(ctx context.Context, pagination *types.Pagination, projectID string, roles []string) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageFileByProjectID(ctx, pagination, projectID, roles)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) CreateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error {
	projectExists, err := s.dataRepo.ExistsProjectByID(ctx, projectDataset.ProjectID)
	if err != nil {
		return err
	}
	if !projectExists {
		return gorm.ErrRecordNotFound
	}

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, projectDataset.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.CreateProjectDataset(ctx, projectDataset)
}

func (s *dataService) GetProjectDatasetByID(ctx context.Context, id int64) (*types.ProjectDataset, error) {
	return s.dataRepo.GetProjectDatasetByID(ctx, id)
}

func (s *dataService) UpdateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error {
	_, err := s.dataRepo.GetProjectDatasetByID(ctx, projectDataset.ID)
	if err != nil {
		return err
	}

	projectExists, err := s.dataRepo.ExistsProjectByID(ctx, projectDataset.ProjectID)
	if err != nil {
		return err
	}
	if !projectExists {
		return gorm.ErrRecordNotFound
	}

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, projectDataset.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateProjectDataset(ctx, projectDataset)
}

func (s *dataService) DeleteProjectDataset(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetProjectDatasetByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteProjectDataset(ctx, id)
}

func (s *dataService) ListProjectDataset(ctx context.Context) ([]*types.ProjectDataset, error) {
	return s.dataRepo.ListProjectDataset(ctx)
}

// ensureFileAssayExists verifies the owning assay of a file. assayID 0 means the
// file is not owned by any assay (dataset-only attachment) and is always valid.
func (s *dataService) ensureFileAssayExists(ctx context.Context, assayID int64) error {
	if assayID == 0 {
		return nil
	}
	exists, err := s.dataRepo.ExistsAssayByID(ctx, assayID)
	if err != nil {
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *dataService) CreateFile(ctx context.Context, file *types.File) error {
	if err := s.ensureFileAssayExists(ctx, file.AssayID); err != nil {
		return err
	}
	if strings.TrimSpace(file.Path) == "" {
		return apperrors.NewValidationError("path is required")
	}

	// go_file.file_id is a unique business number: generate one when the caller
	// does not supply it (same convention as AddFileToDataset).
	if strings.TrimSpace(file.FileID) == "" {
		file.FileID = strconv.FormatInt(utils.GenerateID(), 10)
	}

	// A path may only be registered once per assay scope. Two rows for the same
	// (path, assay_id) would make the assay own the same file twice and the
	// analysis input resolver would pick one of them at random.
	_, err := s.dataRepo.GetFileByPathAndAssayID(ctx, file.Path, file.AssayID)
	if err == nil {
		return apperrors.NewConflictError("file already registered for this assay: " + file.Path)
	}
	if !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	return s.dataRepo.CreateFile(ctx, file)
}

func (s *dataService) GetFileByID(ctx context.Context, id int64) (*types.File, error) {
	return s.dataRepo.GetFileByID(ctx, id)
}

func (s *dataService) GetFileByFileID(ctx context.Context, fileID string) (*types.File, error) {
	return s.dataRepo.GetFileByFileID(ctx, fileID)
}

func (s *dataService) UpdateFile(ctx context.Context, file *types.File) error {
	_, err := s.dataRepo.GetFileByID(ctx, file.ID)
	if err != nil {
		return err
	}
	if err := s.ensureFileAssayExists(ctx, file.AssayID); err != nil {
		return err
	}
	return s.dataRepo.UpdateFile(ctx, file)
}

func (s *dataService) DeleteFile(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetFileByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteFileWithRelations(ctx, id)
}

func (s *dataService) ListFile(ctx context.Context) ([]*types.File, error) {
	return s.dataRepo.ListFile(ctx)
}

func (s *dataService) ListFileByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.FileWithDatasetInfo, error) {
	return s.dataRepo.ListFileByProjectID(ctx, projectID, roles)
}

func (s *dataService) ListFileByAssayID(ctx context.Context, assayID int64) ([]*types.File, error) {
	return s.dataRepo.ListFileByAssayID(ctx, assayID)
}

func (s *dataService) ListFileByProjectIDGroupByRole(ctx context.Context, projectID string) ([]*types.FileByProjectRoleGroup, error) {
	items, err := s.dataRepo.ListFileByProjectID(ctx, projectID, nil)
	if err != nil {
		return nil, err
	}

	groups := make([]*types.FileByProjectRoleGroup, 0)
	groupIndex := make(map[string]int)
	for _, item := range items {
		idx, ok := groupIndex[item.Role]
		if !ok {
			idx = len(groups)
			groupIndex[item.Role] = idx
			groups = append(groups, &types.FileByProjectRoleGroup{
				Role:  item.Role,
				Items: make([]*types.FileWithDatasetInfo, 0),
			})
		}
		groups[idx].Items = append(groups[idx].Items, item)
	}

	return groups, nil
}

func (s *dataService) CreateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error {
	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetFile.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	fileExists, err := s.dataRepo.ExistsFileByID(ctx, datasetFile.FileID)
	if err != nil {
		return err
	}
	if !fileExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.CreateDatasetFile(ctx, datasetFile)
}

func (s *dataService) AddFileToDataset(ctx context.Context, req *types.AddFileToDatasetRequest) (*types.AddFileToDatasetResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}

	dataset, err := s.dataRepo.GetDatasetByID(ctx, req.DatasetID)
	if err != nil {
		return nil, err
	}
	if dataset == nil {
		return nil, gorm.ErrRecordNotFound
	}

	baseDir := strings.TrimSpace(s.baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("storage base dir is required")
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, fmt.Errorf("project id is required")
	}

	absBaseDir, err := utils.ResolveExternalPath(baseDir)
	if err != nil {
		return nil, err
	}

	relativePath := strings.TrimSpace(req.Path)
	if relativePath == "" {
		return nil, fmt.Errorf("path is required")
	}
	source := strings.TrimSpace(req.Source)
	var candidatePath string
	if source == "data" {
		candidatePath = filepath.Join(absBaseDir, source, req.ProjectID, strings.TrimLeft(relativePath, string(filepath.Separator)))
	} else if source == "analysis" {
		candidatePath = relativePath
	} else {
		return nil, fmt.Errorf("invalid source: %s", source)
	}

	resolvedPath, err := utils.SafePathUnderBase(absBaseDir, candidatePath)
	if err != nil {
		return nil, err
	}
	var size int64 = 0
	if !req.IsPrefix {
		info, err := os.Stat(resolvedPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, gorm.ErrRecordNotFound
			}
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("file path is a directory: %s", resolvedPath)
		}
		size = info.Size()
		// If copy is requested, copy file to analysis_result dir with timestamp prefix
		if req.IsCopy {
			destDir := utils.GetDatasetDir(absBaseDir, req.ProjectID, dataset.ID) //filepath.Join(absBaseDir, req.ProjectID, "dataset", fmt.Sprintf("%d", dataset.ID))
			if err := os.MkdirAll(destDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}

			origName := strings.TrimSpace(req.FileName)
			if origName == "" {
				origName = filepath.Base(resolvedPath)
			}
			timestamp := time.Now().Format("20060102150405")
			destName := timestamp + "_" + origName
			destPath := filepath.Join(destDir, destName)

			if err := copyFile(resolvedPath, destPath); err != nil {
				return nil, fmt.Errorf("failed to copy file: %w", err)
			}
			// Refresh stat on the new file
			info, err = os.Stat(destPath)
			if err != nil {
				return nil, err
			}
			resolvedPath = destPath
		}
	}

	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "DEFAULT"
	}

	// Files are assay-private: a path registered outside of any assay (assayID 0)
	// is a distinct row from an assay-owned one, so the lookup is assay-scoped.
	file, err := s.dataRepo.GetFileByPathAndAssayID(ctx, resolvedPath, 0)
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Use custom file name if provided, otherwise derive from path
	displayName := strings.TrimSpace(req.FileName)
	if displayName == "" {
		displayName = filepath.Base(resolvedPath)
	}

	response := &types.AddFileToDatasetResponse{}
	err = s.dataRepo.WithTransaction(ctx, func(tx interfaces.DataRepository) error {
		if file == nil {
			file = &types.File{
				FileID:         strconv.FormatInt(utils.GenerateID(), 10),
				FileName:       displayName,
				Path:           resolvedPath,
				AnalysisNodeID: req.AnalysisNodeID,
				Format:         strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), "."),
				Size:           size,
				Storage:        "LOCAL",
			}
			if err := tx.CreateFile(ctx, file); err != nil {
				return err
			}
		}

		// exists, err := tx.ExistsDatasetFile(ctx, req.DatasetID, file.ID)
		// if err != nil {
		// 	return err
		// }
		// if exists {
		// 	return ErrDatasetFileAlreadyAdded
		// }

		datasetFile := &types.DatasetFile{
			DatasetID: req.DatasetID,
			FileID:    file.ID,
			Role:      role,
		}
		if err := tx.CreateDatasetFile(ctx, datasetFile); err != nil {
			return err
		}

		response.File = file
		response.DatasetFile = datasetFile
		return nil
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (s *dataService) GetDatasetFileByID(ctx context.Context, id int64) (*types.DatasetFile, error) {
	return s.dataRepo.GetDatasetFileByID(ctx, id)
}

func (s *dataService) UpdateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error {
	exists, err := s.dataRepo.ExistsDatasetFile(ctx, datasetFile.DatasetID, datasetFile.FileID)
	if err != nil {
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateDatasetFile(ctx, datasetFile)
}

func (s *dataService) DeleteDatasetFile(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetDatasetFileByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteDatasetFile(ctx, id)
}

func (s *dataService) ListDatasetFile(ctx context.Context) ([]*types.DatasetFile, error) {
	return s.dataRepo.ListDatasetFile(ctx)
}

func (s *dataService) CreateAssay(ctx context.Context, assay *types.Assay) error {
	return s.dataRepo.CreateAssay(ctx, assay)
}

func (s *dataService) GetAssayByID(ctx context.Context, id int64) (*types.Assay, error) {
	return s.dataRepo.GetAssayByID(ctx, id)
}

func (s *dataService) UpdateAssay(ctx context.Context, assay *types.Assay) error {
	_, err := s.dataRepo.GetAssayByID(ctx, assay.ID)
	if err != nil {
		return err
	}
	return s.dataRepo.UpdateAssay(ctx, assay)
}

func (s *dataService) DeleteAssay(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetAssayByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteAssayWithRelations(ctx, id)
}

func (s *dataService) ListAssay(ctx context.Context) ([]*types.Assay, error) {
	return s.dataRepo.ListAssay(ctx)
}

func (s *dataService) PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageAssayByProjectID(ctx, pagination, projectID)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithDatasetInfo, error) {
	return s.dataRepo.ListAssayByProjectID(ctx, projectID, roles)
}

func (s *dataService) GetDatasetAssayByAssayID(ctx context.Context, assayID int64) (*types.DatasetAssay, error) {
	return s.dataRepo.GetDatasetAssayByAssayID(ctx, assayID)
}

func (s *dataService) CreateSubject(ctx context.Context, subject *types.Subject) error {
	subjectName := strings.TrimSpace(subject.SubjectName)
	if subjectName == "" {
		return apperrors.NewValidationError("subject_name is required")
	}
	subject.SubjectName = subjectName

	exists, err := s.dataRepo.ExistsSubjectBySubjectName(ctx, subjectName)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.NewConflictError("subject_name already exists: " + subjectName)
	}

	return s.dataRepo.CreateSubject(ctx, subject)
}

func (s *dataService) GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error) {
	return s.dataRepo.GetSubjectByID(ctx, id)
}

func (s *dataService) UpdateSubject(ctx context.Context, subject *types.Subject) error {
	current, err := s.dataRepo.GetSubjectByID(ctx, subject.ID)
	if err != nil {
		return err
	}

	subjectName := strings.TrimSpace(subject.SubjectName)
	if subjectName == "" {
		return apperrors.NewValidationError("subject_name is required")
	}
	if subjectName != current.SubjectName {
		exists, err := s.dataRepo.ExistsSubjectBySubjectName(ctx, subjectName)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError("subject_name already exists: " + subjectName)
		}
	}
	subject.SubjectName = subjectName

	return s.dataRepo.UpdateSubject(ctx, subject)
}

// DeleteSubject removes a subject only when no sample still references it; the
// hierarchy is Subject -> Sample -> Assay -> File, so the children have to be
// deleted from the leaves up.
func (s *dataService) DeleteSubject(ctx context.Context, id int64) error {
	if _, err := s.dataRepo.GetSubjectByID(ctx, id); err != nil {
		return err
	}

	sampleCount, err := s.dataRepo.CountSamplesBySubjectID(ctx, id)
	if err != nil {
		return err
	}
	if sampleCount > 0 {
		return apperrors.NewConflictError(
			fmt.Sprintf("subject still has %d sample(s); delete them first", sampleCount))
	}

	return s.dataRepo.DeleteSubject(ctx, id)
}

func (s *dataService) ListSubject(ctx context.Context) ([]*types.Subject, error) {
	return s.dataRepo.ListSubject(ctx)
}

func (s *dataService) PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageSubject(ctx, pagination, query)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) CreateSample(ctx context.Context, sample *types.Sample) error {
	sampleID := strings.TrimSpace(sample.SampleID)
	if sampleID == "" {
		return apperrors.NewValidationError("sample_id is required")
	}
	sample.SampleID = sampleID

	if sample.SubjectID == 0 {
		return apperrors.NewValidationError("subject_id is required")
	}
	subjectExists, err := s.dataRepo.ExistsSubjectByID(ctx, sample.SubjectID)
	if err != nil {
		return err
	}
	if !subjectExists {
		return gorm.ErrRecordNotFound
	}

	exists, err := s.dataRepo.ExistsSampleBySampleID(ctx, sampleID)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.NewConflictError("sample_id already exists: " + sampleID)
	}

	return s.dataRepo.CreateSample(ctx, sample)
}

func (s *dataService) GetSampleByID(ctx context.Context, id int64) (*types.Sample, error) {
	return s.dataRepo.GetSampleByID(ctx, id)
}

func (s *dataService) UpdateSample(ctx context.Context, sample *types.Sample) error {
	current, err := s.dataRepo.GetSampleByID(ctx, sample.ID)
	if err != nil {
		return err
	}

	sampleID := strings.TrimSpace(sample.SampleID)
	if sampleID == "" {
		return apperrors.NewValidationError("sample_id is required")
	}
	if sampleID != current.SampleID {
		exists, err := s.dataRepo.ExistsSampleBySampleID(ctx, sampleID)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError("sample_id already exists: " + sampleID)
		}
	}
	sample.SampleID = sampleID

	if sample.SubjectID == 0 {
		sample.SubjectID = current.SubjectID
	}
	subjectExists, err := s.dataRepo.ExistsSubjectByID(ctx, sample.SubjectID)
	if err != nil {
		return err
	}
	if !subjectExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateSample(ctx, sample)
}

// DeleteSample removes a sample only when no assay still references it; assays
// own files and dataset bindings and must be deleted first.
func (s *dataService) DeleteSample(ctx context.Context, id int64) error {
	if _, err := s.dataRepo.GetSampleByID(ctx, id); err != nil {
		return err
	}

	assayCount, err := s.dataRepo.CountAssaysBySampleID(ctx, id)
	if err != nil {
		return err
	}
	if assayCount > 0 {
		return apperrors.NewConflictError(
			fmt.Sprintf("sample still has %d assay(s); delete them first", assayCount))
	}

	return s.dataRepo.DeleteSample(ctx, id)
}

func (s *dataService) ListSample(ctx context.Context) ([]*types.Sample, error) {
	return s.dataRepo.ListSample(ctx)
}

func (s *dataService) PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageSample(ctx, pagination, query)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error {
	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetAssay.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	assayExists, err := s.dataRepo.ExistsAssayByID(ctx, datasetAssay.AssayID)
	if err != nil {
		return err
	}
	if !assayExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.CreateDatasetAssay(ctx, datasetAssay)
}

func (s *dataService) GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error) {
	return s.dataRepo.GetDatasetAssayByID(ctx, id)
}

func (s *dataService) UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error {
	_, err := s.dataRepo.GetDatasetAssayByID(ctx, datasetAssay.ID)
	if err != nil {
		return err
	}

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetAssay.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	assayExists, err := s.dataRepo.ExistsAssayByID(ctx, datasetAssay.AssayID)
	if err != nil {
		return err
	}
	if !assayExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateDatasetAssay(ctx, datasetAssay)
}

func (s *dataService) DeleteDatasetAssay(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetDatasetAssayByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteDatasetAssay(ctx, id)
}

func (s *dataService) ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error) {
	return s.dataRepo.ListDatasetAssay(ctx)
}

// copyFile copies src to dst, preserving permissions but not timestamps.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}
