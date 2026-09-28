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
	// project's subjects and samples (go_project_dataset -> go_dataset_subject ->
	// go_subject -> go_sample -> go_assay). When roles is non-empty the assays
	// are filtered on go_assay.role; an empty roles slice adds no role condition
	// and returns every assay.
	ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithSampleInfo, error)

	CreateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error
	GetDatasetSubjectByID(ctx context.Context, id int64) (*types.DatasetSubject, error)
	// GetDatasetSubjectBySubjectID returns the single dataset binding of one
	// subject, or gorm.ErrRecordNotFound when the subject is not bound to any
	// dataset yet.
	GetDatasetSubjectBySubjectID(ctx context.Context, subjectID int64) (*types.DatasetSubject, error)
	UpdateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error
	DeleteDatasetSubject(ctx context.Context, id int64) error
	ListDatasetSubject(ctx context.Context) ([]*types.DatasetSubject, error)

	CreateSubject(ctx context.Context, subject *types.Subject) error
	GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error)
	UpdateSubject(ctx context.Context, subject *types.Subject) error
	// DeleteSubject removes a subject. It refuses (409) while the subject still
	// owns samples, so callers must delete the samples first.
	DeleteSubject(ctx context.Context, id int64) error
	ListSubject(ctx context.Context) ([]*types.Subject, error)
	PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) (*types.PageResult, error)
	// ListSubjectByProjectID returns the project's subjects joined with the
	// dataset they are bound to. Subjects are resolved project-wide without a
	// role filter.
	ListSubjectByProjectID(ctx context.Context, projectID string) ([]*types.SubjectWithDatasetInfo, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	// DeleteSample removes a sample. It refuses (409) while the sample still
	// owns assays, so callers must delete the assays first.
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) (*types.PageResult, error)
	// ListSampleByProjectID returns the project's samples joined with their
	// owning subject and the dataset that subject is bound to. Samples are
	// resolved project-wide without a role filter.
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)
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

	CreateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error
	GetDatasetSubjectByID(ctx context.Context, id int64) (*types.DatasetSubject, error)
	GetDatasetSubjectBySubjectID(ctx context.Context, subjectID int64) (*types.DatasetSubject, error)
	UpdateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error
	DeleteDatasetSubject(ctx context.Context, id int64) error
	ListDatasetSubject(ctx context.Context) ([]*types.DatasetSubject, error)

	CreateSubject(ctx context.Context, subject *types.Subject) error
	GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error)
	UpdateSubject(ctx context.Context, subject *types.Subject) error
	DeleteSubject(ctx context.Context, id int64) error
	ListSubject(ctx context.Context) ([]*types.Subject, error)
	PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) ([]*types.Subject, int64, error)
	ListSubjectByProjectID(ctx context.Context, projectID string) ([]*types.SubjectWithDatasetInfo, error)

	CreateSample(ctx context.Context, sample *types.Sample) error
	GetSampleByID(ctx context.Context, id int64) (*types.Sample, error)
	UpdateSample(ctx context.Context, sample *types.Sample) error
	DeleteSample(ctx context.Context, id int64) error
	ListSample(ctx context.Context) ([]*types.Sample, error)
	PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) ([]*types.SampleWithSubjectInfo, int64, error)
	ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error)

	// CountSamplesBySubjectID counts the samples owned by one subject; used to
	// guard subject deletion.
	CountSamplesBySubjectID(ctx context.Context, subjectID int64) (int64, error)

	// CountAssaysBySampleID counts the assays owned by one sample; used to guard
	// sample deletion.
	CountAssaysBySampleID(ctx context.Context, sampleID int64) (int64, error)

	ExistsSubjectByID(ctx context.Context, id int64) (bool, error)

	// ExistsSampleBySampleKey reports whether the business number
	// (go_sample.sample_key) is already taken.
	ExistsSampleBySampleKey(ctx context.Context, sampleKey string) (bool, error)

	ExistsSampleByID(ctx context.Context, id int64) (bool, error)

	ExistsProjectByID(ctx context.Context, id string) (bool, error)
	ExistsDatasetByID(ctx context.Context, id int64) (bool, error)
	ExistsFileByID(ctx context.Context, id int64) (bool, error)
	ExistsAssayByID(ctx context.Context, id int64) (bool, error)

	DeleteDatasetWithRelations(ctx context.Context, id int64) error
	DeleteFileWithRelations(ctx context.Context, id int64) error
	DeleteAssayWithRelations(ctx context.Context, id int64) error
}
