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

func (s *dataService) ListFileByProjectIDViaAnalysisNode(ctx context.Context, projectID string) ([]*types.FileWithDatasetInfo, error) {
	return s.dataRepo.ListFileByProjectIDViaAnalysisNode(ctx, projectID)
}

func (s *dataService) ListFileByAssayID(ctx context.Context, assayID int64) ([]*types.File, error) {
	return s.dataRepo.ListFileByAssayID(ctx, assayID)
}

func (s *dataService) ListFileByAssayIDAndRole(ctx context.Context, assayID int64, roles []string) ([]*types.File, error) {
	return s.dataRepo.ListFileByAssayIDAndRole(ctx, assayID, roles)
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
	assay.SampleName = strings.TrimSpace(assay.SampleName)
	if assay.SampleName == "" {
		return apperrors.NewValidationError("sample_name is required")
	}

	return s.dataRepo.CreateAssay(ctx, assay)
}

func (s *dataService) GetAssayByID(ctx context.Context, id int64) (*types.Assay, error) {
	return s.dataRepo.GetAssayByID(ctx, id)
}

func (s *dataService) UpdateAssay(ctx context.Context, assay *types.Assay) error {
	if _, err := s.dataRepo.GetAssayByID(ctx, assay.ID); err != nil {
		return err
	}

	assay.SampleName = strings.TrimSpace(assay.SampleName)
	if assay.SampleName == "" {
		return apperrors.NewValidationError("sample_name is required")
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

func (s *dataService) CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error {
	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetAssay.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	assay, err := s.dataRepo.GetAssayByID(ctx, datasetAssay.AssayID)
	if err != nil {
		return err
	}

	// sample_name (+ role) is only unique inside a dataset, so binding is where a
	// duplicate within the same dataset is rejected (the DB index is not unique).
	if err := s.ensureAssayNameUniqueInDataset(ctx, datasetAssay.DatasetID, assay.SampleName, assay.Role, 0); err != nil {
		return err
	}

	return s.dataRepo.CreateDatasetAssay(ctx, datasetAssay)
}

// ensureAssayNameUniqueInDataset enforces that sample_name + role is unique
// inside one dataset (excluding excludeAssayID). The same name may exist in
// other datasets.
func (s *dataService) ensureAssayNameUniqueInDataset(ctx context.Context, datasetID int64, sampleName, role string, excludeAssayID int64) error {
	exists, err := s.dataRepo.ExistsAssayNameInDataset(ctx, datasetID, sampleName, role, excludeAssayID)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.NewConflictError("sample_name already exists in this dataset: " + sampleName)
	}
	return nil
}

func (s *dataService) GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error) {
	return s.dataRepo.GetDatasetAssayByID(ctx, id)
}

func (s *dataService) GetDatasetAssayByAssayID(ctx context.Context, assayID int64) (*types.DatasetAssay, error) {
	return s.dataRepo.GetDatasetAssayByAssayID(ctx, assayID)
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

	assay, err := s.dataRepo.GetAssayByID(ctx, datasetAssay.AssayID)
	if err != nil {
		return err
	}

	if err := s.ensureAssayNameUniqueInDataset(ctx, datasetAssay.DatasetID, assay.SampleName, assay.Role, assay.ID); err != nil {
		return err
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

// importAssayColumns maps a TSV column name to the Assay field it fills. Note
// that sample_name + assay_role is the assay's natural key inside a dataset.
// assay_desc is optional; omitting the column leaves Assay.Description as-is.
var importAssayColumns = map[string]func(*types.Assay, string){
	"sample_name": func(a *types.Assay, v string) { a.SampleName = v },
	"assay_type":  func(a *types.Assay, v string) { a.AssayType = v },
	"assay_role":  func(a *types.Assay, v string) { a.Role = v },
	"platform":    func(a *types.Assay, v string) { a.Platform = v },
	"library_id":  func(a *types.Assay, v string) { a.LibraryID = v },
	"metadata":    func(a *types.Assay, v string) { a.Metadata = v },
	"assay_desc":  func(a *types.Assay, v string) { a.Description = v },
}

// importIgnoredColumns lists TSV columns that used to configure the removed
// Sample entity. They are neither Assay fields nor files, so they are skipped
// instead of silently becoming file columns.
var importIgnoredColumns = map[string]struct{}{
	"sample_desc": {},
	"tissue":      {},
	"cell_type":   {},
}

// ImportAssayTSV imports a TSV table into one dataset, upserting the whole
// Assay -> File tree per row:
//
//	dataset + sample_name + assay_role -> Assay (create+bind, or update)
//	assay_id + <file column>           -> File  (create, or update; FileKey = column)
//
// The header names the columns; columns not mapped to an Assay field are File
// columns, so new file keys need no code change. The run is transactional: any
// invalid row rolls the whole import back.
func (s *dataService) ImportAssayTSV(ctx context.Context, req *types.ImportAssayTSVRequest) (*types.ImportAssayTSVResult, error) {
	if req == nil {
		return nil, apperrors.NewValidationError("request is required")
	}
	if req.DatasetID == 0 {
		return nil, apperrors.NewValidationError("dataset_id is required")
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, apperrors.NewValidationError("content is required")
	}

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, req.DatasetID)
	if err != nil {
		return nil, err
	}
	if !datasetExists {
		return nil, gorm.ErrRecordNotFound
	}

	header, rows := parseTSV(req.Content)
	if len(header) == 0 {
		return nil, apperrors.NewValidationError("tsv header is empty")
	}

	colIndex := make(map[string]int, len(header))
	for i, name := range header {
		if name = strings.TrimSpace(name); name != "" {
			colIndex[name] = i
		}
	}
	for _, required := range []string{"sample_name"} {
		if _, ok := colIndex[required]; !ok {
			return nil, apperrors.NewValidationError("tsv is missing required column: " + required)
		}
	}

	// Every column that is not an Assay field is a File column
	// whose key is the column name (kept in header order for stable results).
	fileColumns := make([]string, 0, len(header))
	for _, name := range header {
		name = strings.TrimSpace(name)
		if name == "" || isImportEntityColumn(name) {
			continue
		}
		fileColumns = append(fileColumns, name)
	}

	// Organize the dataset workspace so imported files have a home even though
	// the TSV paths themselves are referenced in place (not copied).
	if projectID := strings.TrimSpace(req.ProjectID); projectID != "" && strings.TrimSpace(s.baseDir) != "" {
		if absBaseDir, err := utils.ResolveExternalPath(s.baseDir); err == nil && absBaseDir != "" {
			if err := os.MkdirAll(utils.GetDatasetDir(absBaseDir, projectID, req.DatasetID), 0755); err != nil {
				return nil, fmt.Errorf("failed to create dataset directory: %w", err)
			}
		}
	}

	result := &types.ImportAssayTSVResult{}
	err = s.dataRepo.WithTransaction(ctx, func(tx interfaces.DataRepository) error {
		for i, fields := range rows {
			row := importRowMap(colIndex, fields)
			sampleName := strings.TrimSpace(row["sample_name"])
			if sampleName == "" {
				return apperrors.NewValidationError(
					fmt.Sprintf("tsv row %d: sample_name is required", i+2))
			}

			assay, err := upsertImportAssay(ctx, tx, req.DatasetID, sampleName, strings.TrimSpace(row["assay_role"]), row, result)
			if err != nil {
				return err
			}
			for _, fileKey := range fileColumns {
				if err := upsertImportFile(ctx, tx, assay.ID, fileKey, strings.TrimSpace(row[fileKey]), result); err != nil {
					return err
				}
			}
			result.Rows++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// isImportEntityColumn reports whether a TSV column is consumed by the Assay
// setter map (and is therefore not a File column). Legacy Sample columns are
// treated the same way so they never turn into files.
func isImportEntityColumn(name string) bool {
	if _, ok := importAssayColumns[name]; ok {
		return true
	}
	if _, ok := importIgnoredColumns[name]; ok {
		return true
	}
	return false
}

// parseTSV splits TSV text into its header and data rows. CRLF is normalised to
// LF and blank lines are ignored; the separator stays a tab so values containing
// spaces survive.
func parseTSV(content string) (header []string, rows [][]string) {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if header == nil {
			header = fields
			continue
		}
		rows = append(rows, fields)
	}
	return header, rows
}

// importRowMap projects one data row onto the header column names, padding
// missing trailing cells with empty strings.
func importRowMap(colIndex map[string]int, fields []string) map[string]string {
	row := make(map[string]string, len(colIndex))
	for name, idx := range colIndex {
		if idx < len(fields) {
			row[name] = fields[idx]
		} else {
			row[name] = ""
		}
	}
	return row
}

// applyImportColumns applies every column present in row onto the entity via the
// setter map, so importing another field is just adding it to that map.
func applyImportColumns[T any](entity *T, row map[string]string, setters map[string]func(*T, string)) {
	for column, set := range setters {
		if value, ok := row[column]; ok {
			set(entity, strings.TrimSpace(value))
		}
	}
}

// upsertImportAssay resolves an assay by dataset + sample_name + role, creating
// it (plus its go_dataset_assay binding) when missing and updating it otherwise.
func upsertImportAssay(ctx context.Context, repo interfaces.DataRepository, datasetID int64, sampleName, role string, row map[string]string, result *types.ImportAssayTSVResult) (*types.Assay, error) {
	assay, err := repo.GetAssayByNameAndDatasetID(ctx, datasetID, sampleName, role)
	switch {
	case err == nil:
		applyImportColumns(assay, row, importAssayColumns)
		assay.SampleName = sampleName
		assay.Role = role
		if err := repo.UpdateAssay(ctx, assay); err != nil {
			return nil, err
		}
		result.AssaysUpdated++
		return assay, nil
	case stderrs.Is(err, gorm.ErrRecordNotFound):
		// sample_name + role is unique inside a dataset, so another assay in the
		// same dataset cannot already hold it.
		exists, err := repo.ExistsAssayNameInDataset(ctx, datasetID, sampleName, role, 0)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, apperrors.NewConflictError(
				fmt.Sprintf("sample_name already exists in dataset %d: %s", datasetID, sampleName))
		}

		assay = &types.Assay{}
		applyImportColumns(assay, row, importAssayColumns)
		assay.SampleName = sampleName
		assay.Role = role
		if err := repo.CreateAssay(ctx, assay); err != nil {
			return nil, err
		}
		if err := repo.CreateDatasetAssay(ctx, &types.DatasetAssay{
			DatasetID: datasetID,
			AssayID:   assay.ID,
		}); err != nil {
			return nil, err
		}
		result.AssaysCreated++
		return assay, nil
	default:
		return nil, err
	}
}

// upsertImportFile resolves a file by assay + fileKey (the TSV column name),
// creating it when missing and re-pointing it when the path changed. The stored
// path is the value from the TSV (referenced in place, not copied); file_name and
// format are derived from it so the file table stays self-describing.
func upsertImportFile(ctx context.Context, repo interfaces.DataRepository, assayID int64, fileKey, path string, result *types.ImportAssayTSVResult) error {
	if path == "" {
		return nil
	}

	file, err := repo.GetFileByAssayIDAndFileKey(ctx, assayID, fileKey)
	switch {
	case err == nil:
		file.Path = path
		file.FileName = filepath.Base(path)
		file.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		if err := repo.UpdateFile(ctx, file); err != nil {
			return err
		}
		result.FilesUpdated++
		return nil
	case stderrs.Is(err, gorm.ErrRecordNotFound):
		file = &types.File{
			FileID:   strconv.FormatInt(utils.GenerateID(), 10),
			FileName: filepath.Base(path),
			Path:     path,
			Format:   strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
			AssayID:  assayID,
			FileKey:  fileKey,
			Storage:  "LOCAL",
		}
		if err := repo.CreateFile(ctx, file); err != nil {
			return err
		}
		result.FilesCreated++
		return nil
	default:
		return err
	}
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
