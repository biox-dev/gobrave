package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type DataService interface {
	CreateDataset(ctx context.Context, dataset *types.Dataset) error
	GetDatasetByID(ctx context.Context, id int64) (*types.Dataset, error)
	UpdateDataset(ctx context.Context, dataset *types.Dataset) error
	DeleteDataset(ctx context.Context, id int64) error
	ListDataset(ctx context.Context) ([]*types.Dataset, error)
	PageDatasetByProjectID(ctx context.Context, pagination *types.Pagination, query *types.QueryDataset, projectID string) (*types.PageResult, error)

	CreateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error
	GetProjectDatasetByID(ctx context.Context, id int64) (*types.ProjectDataset, error)
	UpdateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error
	DeleteProjectDataset(ctx context.Context, id int64) error
	ListProjectDataset(ctx context.Context) ([]*types.ProjectDataset, error)

	CreateFile(ctx context.Context, file *types.File) error
	GetFileByID(ctx context.Context, id int64) (*types.File, error)
	GetFileByFileID(ctx context.Context, fileID string) (*types.File, error)
	UpdateFile(ctx context.Context, file *types.File) error
	DeleteFile(ctx context.Context, id int64) error
	ListFile(ctx context.Context) ([]*types.File, error)
	PageFileByProjectID(ctx context.Context, pagination *types.Pagination, projectID string, roles []string) (*types.PageResult, error)
	ListFileByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.FileWithDatasetInfo, error)
	ListFileByProjectIDGroupByRole(ctx context.Context, projectID string) ([]*types.FileByProjectRoleGroup, error)

	CreateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error
	AddFileToDataset(ctx context.Context, req *types.AddFileToDatasetRequest) (*types.AddFileToDatasetResponse, error)
	GetDatasetFileByID(ctx context.Context, id int64) (*types.DatasetFile, error)
	UpdateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error
	DeleteDatasetFile(ctx context.Context, id int64) error
	ListDatasetFile(ctx context.Context) ([]*types.DatasetFile, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSampleByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) (*types.PageResult, error)
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)

	CreateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error
	GetSampleFileByID(ctx context.Context, id int64) (*types.SampleFile, error)
	UpdateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error
	DeleteSampleFile(ctx context.Context, id int64) error
	ListSampleFile(ctx context.Context) ([]*types.SampleFile, error)

	CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error)
	UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	DeleteDatasetSample(ctx context.Context, id int64) error
	ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error)
}

type DataRepository interface {
	CreateDataset(ctx context.Context, dataset *types.Dataset) error
	GetDatasetByID(ctx context.Context, id int64) (*types.Dataset, error)
	UpdateDataset(ctx context.Context, dataset *types.Dataset) error
	DeleteDataset(ctx context.Context, id int64) error
	ListDataset(ctx context.Context) ([]*types.Dataset, error)
	PageDatasetByProjectID(ctx context.Context, pagination *types.Pagination, query *types.QueryDataset, projectID string) ([]*types.Dataset, int64, error)

	CreateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error
	GetProjectDatasetByID(ctx context.Context, id int64) (*types.ProjectDataset, error)
	UpdateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error
	DeleteProjectDataset(ctx context.Context, id int64) error
	ListProjectDataset(ctx context.Context) ([]*types.ProjectDataset, error)

	CreateFile(ctx context.Context, file *types.File) error
	GetFileByID(ctx context.Context, id int64) (*types.File, error)
	GetFileByFileID(ctx context.Context, fileID string) (*types.File, error)
	GetFileByPath(ctx context.Context, path string) (*types.File, error)
	UpdateFile(ctx context.Context, file *types.File) error
	DeleteFile(ctx context.Context, id int64) error
	ListFile(ctx context.Context) ([]*types.File, error)
	PageFileByProjectID(ctx context.Context, pagination *types.Pagination, projectID string, roles []string) ([]*types.FileWithDatasetInfo, int64, error)
	ListFileByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.FileWithDatasetInfo, error)

	CreateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error
	ExistsDatasetFile(ctx context.Context, datasetID, fileID int64) (bool, error)
	WithTransaction(ctx context.Context, fn func(DataRepository) error) error
	GetDatasetFileByID(ctx context.Context, id int64) (*types.DatasetFile, error)
	UpdateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error
	DeleteDatasetFile(ctx context.Context, id int64) error
	ListDatasetFile(ctx context.Context) ([]*types.DatasetFile, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSampleByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.SampleWithDatasetInfo, int64, error)
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)

	CreateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error
	GetSampleFileByID(ctx context.Context, id int64) (*types.SampleFile, error)
	UpdateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error
	DeleteSampleFile(ctx context.Context, id int64) error
	ListSampleFile(ctx context.Context) ([]*types.SampleFile, error)

	CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error)
	UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	DeleteDatasetSample(ctx context.Context, id int64) error
	ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error)

	ExistsProjectByID(ctx context.Context, id string) (bool, error)
	ExistsDatasetByID(ctx context.Context, id int64) (bool, error)
	ExistsFileByID(ctx context.Context, id int64) (bool, error)
	ExistsSampleByID(ctx context.Context, id int64) (bool, error)

	DeleteDatasetWithRelations(ctx context.Context, id int64) error
	DeleteFileWithRelations(ctx context.Context, id int64) error
	DeleteSampleWithRelations(ctx context.Context, id int64) error
}
