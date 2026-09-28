package repository

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type dataRepository struct {
	db *gorm.DB
}

func NewDataRepository(db *gorm.DB) interfaces.DataRepository {
	return &dataRepository{db: db}
}

func (r *dataRepository) CreateDataset(ctx context.Context, dataset *types.Dataset) error {
	return r.db.WithContext(ctx).Create(dataset).Error
}

func (r *dataRepository) GetDatasetByID(ctx context.Context, id int64) (*types.Dataset, error) {
	dataset := &types.Dataset{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(dataset).Error; err != nil {
		return nil, err
	}
	return dataset, nil
}

func (r *dataRepository) UpdateDataset(ctx context.Context, dataset *types.Dataset) error {
	return r.db.WithContext(ctx).Model(&types.Dataset{}).
		Where("id = ?", dataset.ID).
		Updates(map[string]interface{}{
			"dataset_name": dataset.DatasetName,
			"description":  dataset.Description,
			"metadata":     dataset.Metadata,
		}).Error
}

func (r *dataRepository) DeleteDataset(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Dataset{}).Error
}

func (r *dataRepository) ListDataset(ctx context.Context) ([]*types.Dataset, error) {
	items := make([]*types.Dataset, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) PageDatasetByProjectID(ctx context.Context, pagination *types.Pagination, query *types.QueryDataset, projectID string) ([]*types.Dataset, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.Dataset, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Table("go_dataset AS dataset").
			Joins("JOIN go_project_dataset AS pd ON pd.dataset_id = dataset.id").
			Where("pd.project_id = ?", projectID)
	}

	applyFilters := func(db *gorm.DB) *gorm.DB {
		if query == nil {
			return db
		}

		if query.ID != nil {
			db = db.Where("dataset.id = ?", *query.ID)
		}

		if datasetName := query.GetDatasetName(); datasetName != "" {
			db = db.Where("dataset.dataset_name LIKE ?", "%"+datasetName+"%")
		}

		if description := query.GetDescription(); description != "" {
			db = db.Where("dataset.description LIKE ?", "%"+description+"%")
		}

		if metadata := query.GetMetadata(); metadata != "" {
			db = db.Where("dataset.metadata LIKE ?", "%"+metadata+"%")
		}

		return db
	}

	baseQuery := applyFilters(buildQuery())

	if err := applyFilters(buildQuery()).
		Select("COUNT(DISTINCT dataset.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	err := baseQuery.
		Select("dataset.id, dataset.dataset_name, dataset.description, dataset.metadata, dataset.created_at, dataset.updated_at").
		Distinct().
		Order("dataset.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.Dataset{}, total, nil
	}

	return items, total, nil
}

func (r *dataRepository) CreateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error {
	return r.db.WithContext(ctx).Create(projectDataset).Error
}

func (r *dataRepository) GetProjectDatasetByID(ctx context.Context, id int64) (*types.ProjectDataset, error) {
	item := &types.ProjectDataset{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateProjectDataset(ctx context.Context, projectDataset *types.ProjectDataset) error {
	return r.db.WithContext(ctx).Model(&types.ProjectDataset{}).
		Where("id = ?", projectDataset.ID).
		Updates(map[string]interface{}{
			"project_id": projectDataset.ProjectID,
			"dataset_id": projectDataset.DatasetID,
		}).Error
}

func (r *dataRepository) DeleteProjectDataset(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ProjectDataset{}).Error
}

func (r *dataRepository) ListProjectDataset(ctx context.Context) ([]*types.ProjectDataset, error) {
	items := make([]*types.ProjectDataset, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateFile(ctx context.Context, file *types.File) error {
	return r.db.WithContext(ctx).Create(file).Error
}

func (r *dataRepository) GetFileByID(ctx context.Context, id int64) (*types.File, error) {
	item := &types.File{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) GetFileByFileID(ctx context.Context, fileID string) (*types.File, error) {
	item := &types.File{}
	if err := r.db.WithContext(ctx).Where("file_id = ?", fileID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

// GetFileByPathAndAssayID resolves a path to its file row within one assay scope.
// Paths are only unique per assay (assayID 0 = files not owned by any assay), so
// the same physical path can legitimately back one row per assay.
func (r *dataRepository) GetFileByPathAndAssayID(ctx context.Context, path string, assayID int64) (*types.File, error) {
	item := &types.File{}
	if err := r.db.WithContext(ctx).
		Where("path = ? AND assay_id = ?", path, assayID).
		Order("id ASC").
		Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateFile(ctx context.Context, file *types.File) error {
	updates := make(map[string]interface{})
	if file.FileID != "" {
		updates["file_id"] = file.FileID
	}
	if file.FileName != "" {
		updates["file_name"] = file.FileName
	}
	if file.Path != "" {
		updates["path"] = file.Path
	}
	if file.Format != "" {
		updates["format"] = file.Format
	}
	// Optional columns follow the non-zero convention used by the rest of this
	// method: leave them empty to keep the stored value.
	if file.AssayID != 0 {
		updates["assay_id"] = file.AssayID
	}
	if file.FileKey != "" {
		updates["file_key"] = file.FileKey
	}
	if file.Size != 0 {
		updates["size"] = file.Size
	}
	if file.MD5 != "" {
		updates["md5"] = file.MD5
	}
	if file.Storage != "" {
		updates["storage"] = file.Storage
	}
	// Description always set (allows clearing)
	updates["description"] = file.Description

	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.File{}).
		Where("id = ?", file.ID).
		Updates(updates).Error
}

func (r *dataRepository) DeleteFile(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.File{}).Error
}

func (r *dataRepository) ListFile(ctx context.Context) ([]*types.File, error) {
	items := make([]*types.File, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListFileByAssayID returns the files owned by one assay ordered by creation
// order (oldest first), matching the order the analysis input resolver relies on
// when a role appears more than once.
func (r *dataRepository) ListFileByAssayID(ctx context.Context, assayID int64) ([]*types.File, error) {
	items := make([]*types.File, 0)
	err := r.db.WithContext(ctx).Where("assay_id = ?", assayID).Order("id ASC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListFileByAssayIDAndRole returns the files owned by one assay ordered by
// creation order (oldest first), keeping only the assays whose go_assay.role is
// in roles. An empty roles slice adds no role condition at all, so every file of
// the assay is returned — matching ListAssayByProjectID's "empty roles means no
// condition" convention. The assay's role lives on go_assay, hence the join.
func (r *dataRepository) ListFileByAssayIDAndRole(ctx context.Context, assayID int64, roles []string) ([]*types.File, error) {
	items := make([]*types.File, 0)
	query := r.db.WithContext(ctx).
		Table("go_file AS f").
		Select("f.*").
		Joins("JOIN go_assay AS a ON a.id = f.assay_id").
		Where("f.assay_id = ?", assayID)

	if len(roles) > 0 {
		query = query.Where("a.role IN ?", roles)
	}

	err := query.Order("f.id ASC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) PageFileByProjectID(ctx context.Context, pagination *types.Pagination, projectID string, roles []string) ([]*types.FileWithDatasetInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.FileWithDatasetInfo, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Table("go_project_dataset AS pd").
			Select(`
				file.id,
				file.file_id,
				file.analysis_node_id,
				file.file_name,
				file.path,
				file.format,
				file.size,
				file.md5,
				file.storage,
				file.description,
				file.created_at,
				file.updated_at,
				dataset.id AS dataset_id,
				dataset.dataset_name,
				dataset_file.role
			`).
			Joins("JOIN go_dataset AS dataset ON dataset.id = pd.dataset_id").
			Joins("JOIN go_dataset_file AS dataset_file ON dataset_file.dataset_id = dataset.id").
			Joins("JOIN go_file AS file ON file.id = dataset_file.file_id").
			Where("pd.project_id = ?", projectID)
	}

	applyFilters := func(db *gorm.DB) *gorm.DB {
		if len(roles) > 0 {
			db = db.Where("dataset_file.role IN ?", roles)
		}
		return db
	}

	baseQuery := applyFilters(buildQuery())

	if err := applyFilters(buildQuery()).
		Select("COUNT(DISTINCT dataset_file.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	err := baseQuery.
		Order("dataset_file.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.FileWithDatasetInfo{}, total, nil
	}

	return items, total, nil
}

func (r *dataRepository) ListFileByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.FileWithDatasetInfo, error) {
	items := make([]*types.FileWithDatasetInfo, 0)
	query := r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(`
			f.id,
			f.file_id,
			f.file_name,
			f.path,
			f.format,
			f.size,
			f.md5,
			f.storage,
			f.description,
			f.created_at,
			f.updated_at,
			d.id AS dataset_id,
			d.dataset_name,
			df.role
		`).
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_dataset_file AS df ON df.dataset_id = d.id").
		Joins("JOIN go_file AS f ON f.id = df.file_id").
		Where("pd.project_id = ?", projectID)

	if len(roles) > 0 {
		query = query.Where("df.role IN ?", roles)
	}

	err := query.Order("f.id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error {
	return r.db.WithContext(ctx).Create(datasetFile).Error
}

func (r *dataRepository) ExistsDatasetFile(ctx context.Context, datasetID, fileID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.DatasetFile{}).
		Where("dataset_id = ? AND file_id = ?", datasetID, fileID).
		Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) WithTransaction(ctx context.Context, fn func(interfaces.DataRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&dataRepository{db: tx})
	})
}

func (r *dataRepository) GetDatasetFileByID(ctx context.Context, id int64) (*types.DatasetFile, error) {
	item := &types.DatasetFile{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateDatasetFile(ctx context.Context, datasetFile *types.DatasetFile) error {
	updates := make(map[string]interface{})
	if datasetFile.Role != "" {
		updates["role"] = datasetFile.Role
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.DatasetFile{}).
		Where("dataset_id = ? AND file_id = ?", datasetFile.DatasetID, datasetFile.FileID).
		Updates(updates).Error
}

func (r *dataRepository) DeleteDatasetFile(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.DatasetFile{}).Error
}

func (r *dataRepository) ListDatasetFile(ctx context.Context) ([]*types.DatasetFile, error) {
	items := make([]*types.DatasetFile, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateAssay(ctx context.Context, assay *types.Assay) error {
	return r.db.WithContext(ctx).Create(assay).Error
}

func (r *dataRepository) GetAssayByID(ctx context.Context, id int64) (*types.Assay, error) {
	item := &types.Assay{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateAssay(ctx context.Context, assay *types.Assay) error {
	return r.db.WithContext(ctx).Model(&types.Assay{}).
		Where("id = ?", assay.ID).
		Updates(map[string]interface{}{
			"sample_id":  assay.SampleID,
			"assay_type": assay.AssayType,
			"platform":   assay.Platform,
			"library_id": assay.LibraryID,
			"role":       assay.Role,
			"metadata":   assay.Metadata,
		}).Error
}

func (r *dataRepository) DeleteAssay(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Assay{}).Error
}

func (r *dataRepository) ListAssay(ctx context.Context) ([]*types.Assay, error) {
	items := make([]*types.Assay, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListAssayBySampleID returns the assays owned by one sample ordered by id
// ascending, so a sample's files can be grouped per assay role deterministically.
func (r *dataRepository) ListAssayBySampleID(ctx context.Context, sampleID int64) ([]*types.Assay, error) {
	items := make([]*types.Assay, 0)
	err := r.db.WithContext(ctx).Where("sample_id = ?", sampleID).Order("id ASC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// assayWithSampleSelect is the shared projection of the assay read model: the
// assay row plus its owning sample name and subject identifiers. Assays have no
// dataset binding of their own; the project link goes through the subject.
const assayWithSampleSelect = `
	a.id,
	a.sample_id,
	a.assay_type,
	a.platform,
	a.library_id,
	a.role,
	a.metadata,
	a.created_at,
	a.updated_at,
	s.sample_name,
	sub.subject_name AS subject_name`

// assayByProjectBase builds the query resolving a project's assays through the
// project's subjects and samples: go_project_dataset -> go_dataset_subject ->
// go_subject -> go_sample -> go_assay. A dataset only binds to the top-level
// subject, and assays hang off the subject's samples.
func (r *dataRepository) assayByProjectBase(ctx context.Context, projectID string) *gorm.DB {
	return r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(assayWithSampleSelect).
		Joins("JOIN go_dataset_subject AS ds ON ds.dataset_id = pd.dataset_id").
		Joins("JOIN go_subject AS sub ON sub.id = ds.subject_id").
		Joins("JOIN go_sample AS s ON s.subject_id = sub.id").
		Joins("JOIN go_assay AS a ON a.sample_id = s.id").
		Where("pd.project_id = ?", projectID)
}

func (r *dataRepository) PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.AssayWithSampleInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.AssayWithSampleInfo, 0)
	var total int64

	if err := r.assayByProjectBase(ctx, projectID).
		Select("COUNT(DISTINCT a.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	// Grouping by the joined PKs (s.id / sub.id) keeps the extra columns valid
	// under MySQL's ONLY_FULL_GROUP_BY while collapsing the row to one per assay
	// (a sample bound to several datasets would otherwise duplicate its assays).
	err := r.assayByProjectBase(ctx, projectID).
		Group("a.id, s.id, sub.id").
		Order("a.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.AssayWithSampleInfo{}, total, nil
	}

	return items, total, nil
}

// ListAssayByProjectID returns the project's assays, resolved through the
// project's subjects and samples. When roles is non-empty the query filters on
// go_assay.role; an empty roles slice adds no role condition at all, so every
// assay of the project is returned.
func (r *dataRepository) ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithSampleInfo, error) {
	items := make([]*types.AssayWithSampleInfo, 0)
	query := r.assayByProjectBase(ctx, projectID)

	if len(roles) > 0 {
		query = query.Where("a.role IN ?", roles)
	}

	err := query.
		Group("a.id, s.id, sub.id").
		Order("a.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error {
	return r.db.WithContext(ctx).Create(datasetSubject).Error
}

func (r *dataRepository) GetDatasetSubjectByID(ctx context.Context, id int64) (*types.DatasetSubject, error) {
	item := &types.DatasetSubject{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) GetDatasetSubjectBySubjectID(ctx context.Context, subjectID int64) (*types.DatasetSubject, error) {
	item := &types.DatasetSubject{}
	if err := r.db.WithContext(ctx).Where("subject_id = ?", subjectID).Order("id ASC").Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateDatasetSubject(ctx context.Context, datasetSubject *types.DatasetSubject) error {
	return r.db.WithContext(ctx).Model(&types.DatasetSubject{}).
		Where("id = ?", datasetSubject.ID).
		Updates(map[string]interface{}{
			"dataset_id": datasetSubject.DatasetID,
			"subject_id": datasetSubject.SubjectID,
		}).Error
}

func (r *dataRepository) DeleteDatasetSubject(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.DatasetSubject{}).Error
}

func (r *dataRepository) ListDatasetSubject(ctx context.Context) ([]*types.DatasetSubject, error) {
	items := make([]*types.DatasetSubject, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) ExistsProjectByID(ctx context.Context, id string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Project{}).Where("project_id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsDatasetByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Dataset{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsFileByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.File{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsAssayByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Assay{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsSubjectByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Subject{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsSampleByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Sample{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// ExistsSubjectKeyInDataset reports whether another subject with subjectKey is
// already bound to datasetID. subject_key is only unique inside a dataset, so
// the check joins go_dataset_subject instead of looking at go_subject alone.
func (r *dataRepository) ExistsSubjectKeyInDataset(ctx context.Context, datasetID int64, subjectKey string, excludeSubjectID int64) (bool, error) {
	if datasetID == 0 || strings.TrimSpace(subjectKey) == "" {
		return false, nil
	}

	var count int64
	query := r.db.WithContext(ctx).
		Table("go_subject AS sub").
		Joins("JOIN go_dataset_subject AS ds ON ds.subject_id = sub.id").
		Where("ds.dataset_id = ? AND sub.subject_key = ?", datasetID, subjectKey)
	if excludeSubjectID != 0 {
		query = query.Where("sub.id <> ?", excludeSubjectID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ExistsSampleKeyInDatasets reports whether a sample with sampleKey exists under
// a subject bound to any of datasetIDs. sample_key is only unique inside a
// dataset, so the check joins go_dataset_subject. An empty datasetIDs slice
// means the sample is not part of any dataset yet, hence no constraint.
func (r *dataRepository) ExistsSampleKeyInDatasets(ctx context.Context, datasetIDs []int64, sampleKey string, excludeSampleID int64) (bool, error) {
	if len(datasetIDs) == 0 || strings.TrimSpace(sampleKey) == "" {
		return false, nil
	}

	var count int64
	query := r.db.WithContext(ctx).
		Table("go_sample AS s").
		Joins("JOIN go_dataset_subject AS ds ON ds.subject_id = s.subject_id").
		Where("s.sample_key = ? AND ds.dataset_id IN ?", sampleKey, datasetIDs)
	if excludeSampleID != 0 {
		query = query.Where("s.id <> ?", excludeSampleID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListDatasetIDsBySubjectID returns the datasets a subject is bound to
// (go_dataset_subject).
func (r *dataRepository) ListDatasetIDsBySubjectID(ctx context.Context, subjectID int64) ([]int64, error) {
	ids := make([]int64, 0)
	if err := r.db.WithContext(ctx).Model(&types.DatasetSubject{}).
		Where("subject_id = ?", subjectID).
		Pluck("dataset_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// CountSamplesBySubjectID counts samples owned by a subject (go_sample.subject_id).
func (r *dataRepository) CountSamplesBySubjectID(ctx context.Context, subjectID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Sample{}).Where("subject_id = ?", subjectID).Count(&count).Error
	return count, err
}

// CountAssaysBySampleID counts assays owned by a sample (go_assay.sample_id).
func (r *dataRepository) CountAssaysBySampleID(ctx context.Context, sampleID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Assay{}).Where("sample_id = ?", sampleID).Count(&count).Error
	return count, err
}

func (r *dataRepository) CreateSubject(ctx context.Context, subject *types.Subject) error {
	return r.db.WithContext(ctx).Create(subject).Error
}

func (r *dataRepository) GetSubjectByID(ctx context.Context, id int64) (*types.Subject, error) {
	item := &types.Subject{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateSubject(ctx context.Context, subject *types.Subject) error {
	return r.db.WithContext(ctx).Model(&types.Subject{}).
		Where("id = ?", subject.ID).
		Updates(map[string]interface{}{
			"subject_key":  subject.SubjectKey,
			"subject_name": subject.SubjectName,
			"species":      subject.Species,
			"strain":       subject.Strain,
			"sex":          subject.Sex,
			"age":          subject.Age,
			"metadata":     subject.Metadata,
		}).Error
}

func (r *dataRepository) DeleteSubject(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Subject{}).Error
}

func (r *dataRepository) ListSubject(ctx context.Context) ([]*types.Subject, error) {
	items := make([]*types.Subject, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) PageSubject(ctx context.Context, pagination *types.Pagination, query *types.QuerySubject) ([]*types.Subject, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.Subject, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		db := r.db.WithContext(ctx).Table("go_subject AS subject")
		if query == nil {
			return db
		}
		if v := strings.TrimSpace(query.SubjectKey); v != "" {
			db = db.Where("subject.subject_key LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.SubjectName); v != "" {
			db = db.Where("subject.subject_name LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.Species); v != "" {
			db = db.Where("subject.species LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.Strain); v != "" {
			db = db.Where("subject.strain LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.Sex); v != "" {
			db = db.Where("subject.sex = ?", v)
		}
		return db
	}

	if err := buildQuery().Select("COUNT(*)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	err := buildQuery().
		Select("subject.*").
		Order("subject.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

// ListSubjectByProjectID returns the project's subjects joined with the dataset
// they are bound to. The dataset binding lives on the top-level subject
// (go_dataset_subject), which is the entry point for the whole
// Subject -> Sample -> Assay -> File branch.
func (r *dataRepository) ListSubjectByProjectID(ctx context.Context, projectID string) ([]*types.SubjectWithDatasetInfo, error) {
	items := make([]*types.SubjectWithDatasetInfo, 0)
	err := r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(`
	sub.id,
	sub.subject_key,
	sub.subject_name,
	sub.species,
	sub.strain,
	sub.sex,
	sub.age,
	sub.metadata,
	sub.created_at,
	sub.updated_at,
	d.id AS dataset_id,
	d.dataset_name`).
		Joins("JOIN go_dataset_subject AS ds ON ds.dataset_id = pd.dataset_id").
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_subject AS sub ON sub.id = ds.subject_id").
		Where("pd.project_id = ?", projectID).
		Group("sub.id, d.id, d.dataset_name").
		Order("sub.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateSample(ctx context.Context, sample *types.Sample) error {
	return r.db.WithContext(ctx).Create(sample).Error
}

func (r *dataRepository) GetSampleByID(ctx context.Context, id int64) (*types.Sample, error) {
	item := &types.Sample{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateSample(ctx context.Context, sample *types.Sample) error {
	return r.db.WithContext(ctx).Model(&types.Sample{}).
		Where("id = ?", sample.ID).
		Updates(map[string]interface{}{
			"sample_key":      sample.SampleKey,
			"sample_name":     sample.SampleName,
			"subject_id":      sample.SubjectID,
			"tissue":          sample.Tissue,
			"cell_type":       sample.CellType,
			"collection_time": sample.CollectionTime,
			"metadata":        sample.Metadata,
			"description":     sample.Description,
		}).Error
}

func (r *dataRepository) DeleteSample(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Sample{}).Error
}

func (r *dataRepository) ListSample(ctx context.Context) ([]*types.Sample, error) {
	items := make([]*types.Sample, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// sampleWithSubjectSelect is the shared projection of the Sample+Subject read model.
const sampleWithSubjectSelect = `
	s.id,
	s.sample_key,
	s.sample_name,
	s.subject_id,
	sub.subject_name AS subject_name,
	sub.species AS species,
	s.tissue,
	s.cell_type,
	s.collection_time,
	s.metadata,
	s.description,
	s.created_at,
	s.updated_at`

func (r *dataRepository) PageSample(ctx context.Context, pagination *types.Pagination, query *types.QuerySample) ([]*types.SampleWithSubjectInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.SampleWithSubjectInfo, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		db := r.db.WithContext(ctx).
			Table("go_sample AS s").
			Joins("LEFT JOIN go_subject AS sub ON sub.id = s.subject_id")
		if query == nil {
			return db
		}
		if v := strings.TrimSpace(query.SampleKey); v != "" {
			db = db.Where("s.sample_key LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.SampleName); v != "" {
			db = db.Where("s.sample_name LIKE ?", "%"+v+"%")
		}
		if query.SubjectID != nil {
			db = db.Where("s.subject_id = ?", *query.SubjectID)
		}
		if v := strings.TrimSpace(query.Tissue); v != "" {
			db = db.Where("s.tissue LIKE ?", "%"+v+"%")
		}
		if v := strings.TrimSpace(query.CellType); v != "" {
			db = db.Where("s.cell_type LIKE ?", "%"+v+"%")
		}
		return db
	}

	if err := buildQuery().Select("COUNT(*)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	err := buildQuery().
		Select(sampleWithSubjectSelect).
		Order("s.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

// ListSampleByProjectID returns the project's samples joined with their owning
// subject and the dataset that subject is bound to. The dataset binding lives
// on the top-level subject (go_dataset_subject -> go_subject), so the samples
// are reached through their subject. Samples are resolved project-wide, so there
// is no role filter (contrast with ListAssayByProjectID/ListFileByProjectID).
func (r *dataRepository) ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error) {
	items := make([]*types.SampleWithDatasetInfo, 0)
	err := r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(`
	s.id,
	s.sample_key,
	s.sample_name,
	s.subject_id,
	sub.subject_name AS subject_name,
	sub.species AS species,
	s.tissue,
	s.cell_type,
	s.collection_time,
	s.metadata,
	s.description,
	s.created_at,
	s.updated_at,
	d.id AS dataset_id,
	d.dataset_name`).
		Joins("JOIN go_dataset_subject AS ds ON ds.dataset_id = pd.dataset_id").
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_subject AS sub ON sub.id = ds.subject_id").
		Joins("JOIN go_sample AS s ON s.subject_id = sub.id").
		Where("pd.project_id = ?", projectID).
		Group("s.id, d.id, d.dataset_name, sub.id").
		Order("s.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetSubjectByKeyAndDatasetID resolves a subject by its business key inside one
// dataset. Subjects are only "in" a dataset through go_dataset_subject, so the
// lookup joins that binding; it returns gorm.ErrRecordNotFound when the subject
// is not bound to the dataset yet (the importer then creates the subject and the
// binding).
func (r *dataRepository) GetSubjectByKeyAndDatasetID(ctx context.Context, datasetID int64, subjectKey string) (*types.Subject, error) {
	item := &types.Subject{}
	err := r.db.WithContext(ctx).
		Table("go_subject AS sub").
		Select("sub.*").
		Joins("JOIN go_dataset_subject AS ds ON ds.subject_id = sub.id").
		Where("ds.dataset_id = ? AND sub.subject_key = ?", datasetID, subjectKey).
		Order("sub.id ASC").
		Take(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
}

// GetSampleBySubjectIDAndSampleKey resolves a sample by its business key inside
// one subject (the natural key used by the TSV importer).
func (r *dataRepository) GetSampleBySubjectIDAndSampleKey(ctx context.Context, subjectID int64, sampleKey string) (*types.Sample, error) {
	item := &types.Sample{}
	err := r.db.WithContext(ctx).
		Where("subject_id = ? AND sample_key = ?", subjectID, sampleKey).
		Order("id ASC").
		Take(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
}

// GetAssayBySampleIDAndRole resolves an assay by its owning sample and role (the
// natural key used by the TSV importer).
func (r *dataRepository) GetAssayBySampleIDAndRole(ctx context.Context, sampleID int64, role string) (*types.Assay, error) {
	item := &types.Assay{}
	err := r.db.WithContext(ctx).
		Where("sample_id = ? AND role = ?", sampleID, role).
		Order("id ASC").
		Take(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
}

// GetFileByAssayIDAndFileKey resolves a file by its owning assay and file key
// (the TSV column name, e.g. FASTQ_R1).
func (r *dataRepository) GetFileByAssayIDAndFileKey(ctx context.Context, assayID int64, fileKey string) (*types.File, error) {
	item := &types.File{}
	err := r.db.WithContext(ctx).
		Where("assay_id = ? AND file_key = ?", assayID, fileKey).
		Order("id ASC").
		Take(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) DeleteDatasetWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dataset_id = ?", id).Delete(&types.ProjectDataset{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetSubject{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.Dataset{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// DeleteFileWithRelations removes a file row together with its dataset bindings.
// The assay binding lives on the file row itself, so there is no join table left
// to clean up.
func (r *dataRepository) DeleteFileWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ?", id).Delete(&types.DatasetFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.File{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// DeleteAssayWithRelations removes an assay and the files it owns. Files are
// assay-private, so they go away with their assay (along with any dataset
// bindings those files had). An assay has no dataset binding of its own.
func (r *dataRepository) DeleteAssayWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fileIDs := make([]int64, 0)
		if err := tx.Model(&types.File{}).Where("assay_id = ?", id).Pluck("id", &fileIDs).Error; err != nil {
			return err
		}
		if len(fileIDs) > 0 {
			if err := tx.Where("file_id IN ?", fileIDs).Delete(&types.DatasetFile{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", fileIDs).Delete(&types.File{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id = ?", id).Delete(&types.Assay{}).Error; err != nil {
			return err
		}
		return nil
	})
}
