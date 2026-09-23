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
	if file.Role != "" {
		updates["role"] = file.Role
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

// assayWithDatasetSelect is the shared projection of the assay read model: the
// assay row plus its owning dataset, sample name and subject identifiers.
const assayWithDatasetSelect = `
	a.id,
	a.sample_id,
	a.assay_type,
	a.platform,
	a.library_id,
	a.metadata,
	a.created_at,
	a.updated_at,
	s.sample_name,
	sub.subject_name AS subject_name,
	d.id AS dataset_id,
	d.dataset_name`

func (r *dataRepository) PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.AssayWithDatasetInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.AssayWithDatasetInfo, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Table("go_project_dataset AS pd").
			Select(assayWithDatasetSelect).
			Joins("JOIN go_dataset_assay AS da ON da.dataset_id = pd.dataset_id").
			Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
			Joins("JOIN go_assay AS a ON a.id = da.assay_id").
			Joins("LEFT JOIN go_sample AS s ON s.id = a.sample_id").
			Joins("LEFT JOIN go_subject AS sub ON sub.id = s.subject_id").
			Where("pd.project_id = ?", projectID)
	}

	if err := buildQuery().
		Select("COUNT(DISTINCT da.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	// Grouping by the joined PKs (s.id / sub.id) keeps the extra columns valid
	// under MySQL's ONLY_FULL_GROUP_BY without changing the row cardinality.
	err := buildQuery().
		Group("a.id, d.id, d.dataset_name, s.id, sub.id").
		Order("a.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.AssayWithDatasetInfo{}, total, nil
	}

	return items, total, nil
}

func (r *dataRepository) ListAssayByProjectID(ctx context.Context, projectID string) ([]*types.AssayWithDatasetInfo, error) {
	items := make([]*types.AssayWithDatasetInfo, 0)
	err := r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(assayWithDatasetSelect).
		Joins("JOIN go_dataset_assay AS da ON da.dataset_id = pd.dataset_id").
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_assay AS a ON a.id = da.assay_id").
		Joins("LEFT JOIN go_sample AS s ON s.id = a.sample_id").
		Joins("LEFT JOIN go_subject AS sub ON sub.id = s.subject_id").
		Where("pd.project_id = ?", projectID).
		Group("a.id, d.id, d.dataset_name, s.id, sub.id").
		Order("a.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error {
	return r.db.WithContext(ctx).Create(datasetAssay).Error
}

func (r *dataRepository) GetDatasetAssayByID(ctx context.Context, id int64) (*types.DatasetAssay, error) {
	item := &types.DatasetAssay{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) GetDatasetAssayByAssayID(ctx context.Context, assayID int64) (*types.DatasetAssay, error) {
	item := &types.DatasetAssay{}
	if err := r.db.WithContext(ctx).Where("assay_id = ?", assayID).Order("id ASC").Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateDatasetAssay(ctx context.Context, datasetAssay *types.DatasetAssay) error {
	return r.db.WithContext(ctx).Model(&types.DatasetAssay{}).
		Where("id = ?", datasetAssay.ID).
		Updates(map[string]interface{}{
			"dataset_id": datasetAssay.DatasetID,
			"assay_id":   datasetAssay.AssayID,
		}).Error
}

func (r *dataRepository) DeleteDatasetAssay(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.DatasetAssay{}).Error
}

func (r *dataRepository) ListDatasetAssay(ctx context.Context) ([]*types.DatasetAssay, error) {
	items := make([]*types.DatasetAssay, 0)
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

func (r *dataRepository) ExistsSubjectBySubjectName(ctx context.Context, subjectName string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Subject{}).Where("subject_name = ?", subjectName).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) ExistsSampleBySampleID(ctx context.Context, sampleID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Sample{}).Where("sample_id = ?", sampleID).Count(&count).Error
	return count > 0, err
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
			"sample_id":       sample.SampleID,
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
	s.sample_id,
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
		if v := strings.TrimSpace(query.SampleID); v != "" {
			db = db.Where("s.sample_id LIKE ?", "%"+v+"%")
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

func (r *dataRepository) DeleteDatasetWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dataset_id = ?", id).Delete(&types.ProjectDataset{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetAssay{}).Error; err != nil {
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

// DeleteAssayWithRelations removes an assay, its dataset binding and the files it
// owns. Files are assay-private, so they go away with their assay (along with any
// dataset bindings those files had).
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
		if err := tx.Where("assay_id = ?", id).Delete(&types.DatasetAssay{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.Assay{}).Error; err != nil {
			return err
		}
		return nil
	})
}
