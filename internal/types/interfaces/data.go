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
	// ListFileByAssayID returns the files owned by one assay; an assay without
	// files yields an empty slice (not an error).
	ListFileByAssayID(ctx context.Context, assayID int64) ([]*types.File, error)
	// ListFileByAssayIDAndRole returns the files owned by one assay, filtered on
	// the owning assay's role (go_assay.role). An empty roles slice adds no role
	// condition, so every file of the assay is returned (mirrors
	// ListAssayByProjectID).
	ListFileByAssayIDAndRole(ctx context.Context, assayID int64, roles []string) ([]*types.File, error)

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
	// ListAssayBySampleID returns the assays owned by one sample, oldest first;
	// a sample without assays yields an empty slice (not an error).
	ListAssayBySampleID(ctx context.Context, sampleID int64) ([]*types.Assay, error)
	PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) (*types.PageResult, error)
	// ListAssayByProjectID returns the project's assays, resolved through the
	// project's samples (go_project_dataset -> go_dataset_sample -> go_sample ->
	// go_assay). When roles is non-empty the assays are filtered on
	// go_assay.role; an empty roles slice adds no role condition and returns
	// every assay.
	ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithSampleInfo, error)

	CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error)
	// GetDatasetSampleBySampleID returns the single dataset binding of one
	// sample, or gorm.ErrRecordNotFound when the sample is not bound to any
	// dataset yet.
	GetDatasetSampleBySampleID(ctx context.Context, sampleID int64) (*types.DatasetSample, error)
	UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	DeleteDatasetSample(ctx context.Context, id int64) error
	ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	// DeleteSample removes a sample and cascades through its assays (Sample ->
	// Assay -> File), including the files' dataset bindings and the sample's own
	// dataset binding (go_dataset_sample).
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) (*types.PageResult, error)
	// ListSampleByProjectID returns the project's samples joined with the dataset
	// they are bound to through go_dataset_sample. Samples are resolved
	// project-wide without a role filter.
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)

	// ImportAssayTSV bulk-imports the Sample -> Assay -> File tree from a TSV
	// text scoped to one dataset, upserting each level by its natural key
	// (dataset+sample_name, sample+assay_role, assay+file_key).
	ImportAssayTSV(ctx context.Context, req *types.ImportAssayTSVRequest) (*types.ImportAssayTSVResult, error)
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
	// GetFileByPathAndAssayID resolves a path to its file row for one assay
	// (assayID 0 = files that are not owned by any assay). Paths are unique per
	// assay, not globally.
	GetFileByPathAndAssayID(ctx context.Context, path string, assayID int64) (*types.File, error)
	UpdateFile(ctx context.Context, file *types.File) error
	DeleteFile(ctx context.Context, id int64) error
	ListFile(ctx context.Context) ([]*types.File, error)
	PageFileByProjectID(ctx context.Context, pagination *types.Pagination, projectID string, roles []string) ([]*types.FileWithDatasetInfo, int64, error)
	ListFileByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.FileWithDatasetInfo, error)
	ListFileByAssayID(ctx context.Context, assayID int64) ([]*types.File, error)
	ListFileByAssayIDAndRole(ctx context.Context, assayID int64, roles []string) ([]*types.File, error)

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
	ListAssayBySampleID(ctx context.Context, sampleID int64) ([]*types.Assay, error)
	PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.AssayWithSampleInfo, int64, error)
	ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithSampleInfo, error)

	CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error)
	GetDatasetSampleBySampleID(ctx context.Context, sampleID int64) (*types.DatasetSample, error)
	UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error
	DeleteDatasetSample(ctx context.Context, id int64) error
	ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) ([]*types.Sample, int64, error)
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)

	// Import lookups: resolve each hierarchy level by its natural key so the TSV
	// importer can decide between insert and update.
	GetSampleByNameAndDatasetID(ctx context.Context, datasetID int64, sampleName string) (*types.Sample, error)
	GetAssayBySampleIDAndRole(ctx context.Context, sampleID int64, role string) (*types.Assay, error)
	GetFileByAssayIDAndFileKey(ctx context.Context, assayID int64, fileKey string) (*types.File, error)

	ExistsSampleByID(ctx context.Context, id int64) (bool, error)

	// SampleName is only unique inside a dataset, so the uniqueness check below is
	// dataset-scoped instead of global.
	//
	// ExistsSampleNameInDataset reports whether another sample with the same
	// sample_name (excluding excludeSampleID) is bound to datasetID.
	ExistsSampleNameInDataset(ctx context.Context, datasetID int64, sampleName string, excludeSampleID int64) (bool, error)

	ExistsProjectByID(ctx context.Context, id string) (bool, error)
	ExistsDatasetByID(ctx context.Context, id int64) (bool, error)
	ExistsFileByID(ctx context.Context, id int64) (bool, error)
	ExistsAssayByID(ctx context.Context, id int64) (bool, error)

	DeleteDatasetWithRelations(ctx context.Context, id int64) error
	DeleteFileWithRelations(ctx context.Context, id int64) error
	DeleteAssayWithRelations(ctx context.Context, id int64) error
	// DeleteSampleWithRelations removes a sample, every assay it owns (including
	// those assays' files and dataset bindings), and its own dataset binding
	// (go_dataset_sample).
	DeleteSampleWithRelations(ctx context.Context, id int64) error
}
