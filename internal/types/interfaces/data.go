package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type DataService interface {
	// CreateDataset creates a dataset and binds it to the given project.
	CreateDataset(ctx context.Context, dataset *types.Dataset, projectID string) error
	// EnsureDatasetDir verifies the dataset exists and creates its workspace
	// directory (utils.GetDatasetDir) when missing, returning the path.
	EnsureDatasetDir(ctx context.Context, datasetID int64, projectID string) (string, error)
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

	CreateAssay(ctx context.Context, assay *types.Assay) error
	GetAssayByID(ctx context.Context, id int64) (*types.Assay, error)
	UpdateAssay(ctx context.Context, assay *types.Assay) error
	DeleteAssay(ctx context.Context, id int64) error
	ListAssay(ctx context.Context) ([]*types.Assay, error)
	PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) (*types.PageResult, error)
	ListAssayByProjectID(ctx context.Context, projectID string) ([]*types.AssayWithDatasetInfo, error)

	CreateAssayFile(ctx context.Context, assayFile *types.AssayFile) error
	GetAssayFileByID(ctx context.Context, id int64) (*types.AssayFile, error)
	UpdateAssayFile(ctx context.Context, assayFile *types.AssayFile) error
	DeleteAssayFile(ctx context.Context, id int64) error
	ListAssayFile(ctx context.Context) ([]*types.AssayFile, error)

	CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error)
	UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	DeleteDatasetAssay(ctx context.Context, id int64) error
	ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error)
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

	CreateAssay(ctx context.Context, assay *types.Assay) error
	GetAssayByID(ctx context.Context, id int64) (*types.Assay, error)
	UpdateAssay(ctx context.Context, assay *types.Assay) error
	DeleteAssay(ctx context.Context, id int64) error
	ListAssay(ctx context.Context) ([]*types.Assay, error)
	PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.AssayWithDatasetInfo, int64, error)
	ListAssayByProjectID(ctx context.Context, projectID string) ([]*types.AssayWithDatasetInfo, error)

	CreateAssayFile(ctx context.Context, assayFile *types.AssayFile) error
	GetAssayFileByID(ctx context.Context, id int64) (*types.AssayFile, error)
	UpdateAssayFile(ctx context.Context, assayFile *types.AssayFile) error
	DeleteAssayFile(ctx context.Context, id int64) error
	ListAssayFile(ctx context.Context) ([]*types.AssayFile, error)

	CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error)
	UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	DeleteDatasetAssay(ctx context.Context, id int64) error
	ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error)

	ExistsProjectByID(ctx context.Context, id string) (bool, error)
	ExistsDatasetByID(ctx context.Context, id int64) (bool, error)
	ExistsFileByID(ctx context.Context, id int64) (bool, error)
	ExistsAssayByID(ctx context.Context, id int64) (bool, error)

	DeleteDatasetWithRelations(ctx context.Context, id int64) error
	DeleteFileWithRelations(ctx context.Context, id int64) error
	DeleteAssayWithRelations(ctx context.Context, id int64) error
}
