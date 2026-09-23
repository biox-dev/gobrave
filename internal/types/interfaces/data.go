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
	// ListAssayByProjectID returns the project's assays joined with their dataset
	// binding. When roles is non-empty it filters by go_dataset_assay.role and
	// fills AssayWithDatasetInfo.Role from that binding.
	ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithDatasetInfo, error)

	CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error)
	// GetDatasetAssayByAssayID returns the single dataset binding of one assay,
	// or gorm.ErrRecordNotFound when the assay is not bound to any dataset yet.
	GetDatasetAssayByAssayID(ctx context.Context, assayID int64) (*types.DatasetAssay, error)
	UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	DeleteDatasetAssay(ctx context.Context, id int64) error
	ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error)

	CreateSubject(ctx context.Context, subject *types.Subject) error
	GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error)
	UpdateSubject(ctx context.Context, subject *types.Subject) error
	// DeleteSubject removes a subject. It refuses (409) while the subject still
	// owns samples, so callers must delete the samples first.
	DeleteSubject(ctx context.Context, id int64) error
	ListSubject(ctx context.Context) ([]*types.Subject, error)
	PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) (*types.PageResult, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	// DeleteSample removes a sample. It refuses (409) while the sample still
	// owns assays, so callers must delete the assays first.
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) (*types.PageResult, error)
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
	ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithDatasetInfo, error)

	CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error)
	GetDatasetAssayByAssayID(ctx context.Context, assayID int64) (*types.DatasetAssay, error)
	UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error
	DeleteDatasetAssay(ctx context.Context, id int64) error
	ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error)

	CreateSubject(ctx context.Context, subject *types.Subject) error
	GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error)
	UpdateSubject(ctx context.Context, subject *types.Subject) error
	DeleteSubject(ctx context.Context, id int64) error
	ListSubject(ctx context.Context) ([]*types.Subject, error)
	PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) ([]*types.Subject, int64, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) ([]*types.SampleWithSubjectInfo, int64, error)

	// CountSamplesBySubjectID counts the samples owned by one subject; used to
	// guard subject deletion.
	CountSamplesBySubjectID(ctx context.Context, subjectID int64) (int64, error)

	// CountAssaysBySampleID counts the assays owned by one sample; used to guard
	// sample deletion.
	CountAssaysBySampleID(ctx context.Context, sampleID int64) (int64, error)

	ExistsSubjectByID(ctx context.Context, id int64) (bool, error)

	// ExistsSubjectBySubjectName reports whether the subject business name
	// (go_subject.subject_name) is already taken.
	ExistsSubjectBySubjectName(ctx context.Context, subjectName string) (bool, error)

	// ExistsSampleBySampleID reports whether the business number
	// (go_sample.sample_id) is already taken.
	ExistsSampleBySampleID(ctx context.Context, sampleID string) (bool, error)

	ExistsProjectByID(ctx context.Context, id string) (bool, error)
	ExistsDatasetByID(ctx context.Context, id int64) (bool, error)
	ExistsFileByID(ctx context.Context, id int64) (bool, error)
	ExistsAssayByID(ctx context.Context, id int64) (bool, error)

	DeleteDatasetWithRelations(ctx context.Context, id int64) error
	DeleteFileWithRelations(ctx context.Context, id int64) error
	DeleteAssayWithRelations(ctx context.Context, id int64) error
}
