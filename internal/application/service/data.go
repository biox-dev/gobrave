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

	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
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

func (s *dataService) CreateDataset(ctx context.Context, dataset *types.Dataset) error {
	return s.dataRepo.CreateDataset(ctx, dataset)
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

func (s *dataService) CreateFile(ctx context.Context, file *types.File) error {
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

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, req.DatasetID)
	if err != nil {
		return nil, err
	}
	if !datasetExists {
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

	// If copy is requested, copy file to analysis_result dir with timestamp prefix
	if req.IsCopy {
		destDir := filepath.Join(absBaseDir, req.ProjectID, "analysis_result")
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

	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "DEFAULT"
	}

	file, err := s.dataRepo.GetFileByPath(ctx, resolvedPath)
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
				FileID:   strconv.FormatInt(utils.GenerateID(), 10),
				FileName: displayName,
				Path:     resolvedPath,
				Format:   strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), "."),
				Size:     info.Size(),
				Storage:  "LOCAL",
			}
			if err := tx.CreateFile(ctx, file); err != nil {
				return err
			}
		}

		exists, err := tx.ExistsDatasetFile(ctx, req.DatasetID, file.ID)
		if err != nil {
			return err
		}
		if exists {
			return ErrDatasetFileAlreadyAdded
		}

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

func (s *dataService) CreateSample(ctx context.Context, sample *types.Sample) error {
	return s.dataRepo.CreateSample(ctx, sample)
}

func (s *dataService) GetSampleByID(ctx context.Context, id int64) (*types.Sample, error) {
	return s.dataRepo.GetSampleByID(ctx, id)
}

func (s *dataService) UpdateSample(ctx context.Context, sample *types.Sample) error {
	_, err := s.dataRepo.GetSampleByID(ctx, sample.ID)
	if err != nil {
		return err
	}
	return s.dataRepo.UpdateSample(ctx, sample)
}

func (s *dataService) DeleteSample(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetSampleByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteSampleWithRelations(ctx, id)
}

func (s *dataService) ListSample(ctx context.Context) ([]*types.Sample, error) {
	return s.dataRepo.ListSample(ctx)
}

func (s *dataService) PageSampleByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.dataRepo.PageSampleByProjectID(ctx, pagination, projectID)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

func (s *dataService) ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error) {
	return s.dataRepo.ListSampleByProjectID(ctx, projectID)
}

func (s *dataService) CreateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error {
	sampleExists, err := s.dataRepo.ExistsSampleByID(ctx, sampleFile.SampleID)
	if err != nil {
		return err
	}
	if !sampleExists {
		return gorm.ErrRecordNotFound
	}

	fileExists, err := s.dataRepo.ExistsFileByID(ctx, sampleFile.FileID)
	if err != nil {
		return err
	}
	if !fileExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.CreateSampleFile(ctx, sampleFile)
}

func (s *dataService) GetSampleFileByID(ctx context.Context, id int64) (*types.SampleFile, error) {
	return s.dataRepo.GetSampleFileByID(ctx, id)
}

func (s *dataService) UpdateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error {
	_, err := s.dataRepo.GetSampleFileByID(ctx, sampleFile.ID)
	if err != nil {
		return err
	}

	sampleExists, err := s.dataRepo.ExistsSampleByID(ctx, sampleFile.SampleID)
	if err != nil {
		return err
	}
	if !sampleExists {
		return gorm.ErrRecordNotFound
	}

	fileExists, err := s.dataRepo.ExistsFileByID(ctx, sampleFile.FileID)
	if err != nil {
		return err
	}
	if !fileExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateSampleFile(ctx, sampleFile)
}

func (s *dataService) DeleteSampleFile(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetSampleFileByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteSampleFile(ctx, id)
}

func (s *dataService) ListSampleFile(ctx context.Context) ([]*types.SampleFile, error) {
	return s.dataRepo.ListSampleFile(ctx)
}

func (s *dataService) CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error {
	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetSample.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	sampleExists, err := s.dataRepo.ExistsSampleByID(ctx, datasetSample.SampleID)
	if err != nil {
		return err
	}
	if !sampleExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.CreateDatasetSample(ctx, datasetSample)
}

func (s *dataService) GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error) {
	return s.dataRepo.GetDatasetSampleByID(ctx, id)
}

func (s *dataService) UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error {
	_, err := s.dataRepo.GetDatasetSampleByID(ctx, datasetSample.ID)
	if err != nil {
		return err
	}

	datasetExists, err := s.dataRepo.ExistsDatasetByID(ctx, datasetSample.DatasetID)
	if err != nil {
		return err
	}
	if !datasetExists {
		return gorm.ErrRecordNotFound
	}

	sampleExists, err := s.dataRepo.ExistsSampleByID(ctx, datasetSample.SampleID)
	if err != nil {
		return err
	}
	if !sampleExists {
		return gorm.ErrRecordNotFound
	}

	return s.dataRepo.UpdateDatasetSample(ctx, datasetSample)
}

func (s *dataService) DeleteDatasetSample(ctx context.Context, id int64) error {
	_, err := s.dataRepo.GetDatasetSampleByID(ctx, id)
	if err != nil {
		return err
	}
	return s.dataRepo.DeleteDatasetSample(ctx, id)
}

func (s *dataService) ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error) {
	return s.dataRepo.ListDatasetSample(ctx)
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
