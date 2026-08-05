package repository

import (
	"context"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
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
			"dataset_id":   dataset.DatasetID,
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

		if datasetID := query.GetDatasetID(); datasetID != "" {
			db = db.Where("dataset.dataset_id = ?", datasetID)
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
		Select("dataset.id, dataset.dataset_id, dataset.dataset_name, dataset.description, dataset.metadata, dataset.created_at, dataset.updated_at").
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

func (r *dataRepository) GetFileByPath(ctx context.Context, path string) (*types.File, error) {
	item := &types.File{}
	if err := r.db.WithContext(ctx).Where("path = ?", path).Take(item).Error; err != nil {
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
			"sample_id":   sample.SampleID,
			"sample_name": sample.SampleName,
			"subject_id":  sample.SubjectID,
			"group_name":  sample.GroupName,
			"phenotype":   sample.Phenotype,
			"metadata":    sample.Metadata,
			"description": sample.Description,
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

func (r *dataRepository) PageSampleByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.SampleWithDatasetInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.SampleWithDatasetInfo, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Table("go_project_dataset AS pd").
			Select(`
				s.id,
				s.sample_id,
				s.sample_name,
				s.subject_id,
				s.group_name,
				s.phenotype,
				s.metadata,
				s.description,
				s.created_at,
				s.updated_at,
				d.id AS dataset_id,
				d.dataset_name
			`).
			Joins("JOIN go_dataset_sample AS ds ON ds.dataset_id = pd.dataset_id").
			Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
			Joins("JOIN go_sample AS s ON s.id = ds.sample_id").
			Where("pd.project_id = ?", projectID)
	}

	if err := buildQuery().
		Select("COUNT(DISTINCT ds.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	err := buildQuery().
		Group("s.id, d.dataset_id, d.dataset_name").
		Order("s.id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.SampleWithDatasetInfo{}, total, nil
	}

	return items, total, nil
}

func (r *dataRepository) ListSampleByProjectID(ctx context.Context, projectID string) ([]*types.SampleWithDatasetInfo, error) {
	items := make([]*types.SampleWithDatasetInfo, 0)
	err := r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(`
			s.id,
			s.sample_id,
			s.sample_name,
			s.subject_id,
			s.group_name,
			s.phenotype,
			s.metadata,
			s.description,
			s.created_at,
			s.updated_at,
			d.id as dataset_id,
			d.dataset_name
		`).
		Joins("JOIN go_dataset_sample AS ds ON ds.dataset_id = pd.dataset_id").
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_sample AS s ON s.id = ds.sample_id").
		Where("pd.project_id = ?", projectID).
		Group("s.id, d.dataset_id, d.dataset_name").
		Order("s.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error {
	return r.db.WithContext(ctx).Create(sampleFile).Error
}

func (r *dataRepository) GetSampleFileByID(ctx context.Context, id int64) (*types.SampleFile, error) {
	item := &types.SampleFile{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateSampleFile(ctx context.Context, sampleFile *types.SampleFile) error {
	return r.db.WithContext(ctx).Model(&types.SampleFile{}).
		Where("id = ?", sampleFile.ID).
		Updates(map[string]interface{}{
			"sample_id": sampleFile.SampleID,
			"file_id":   sampleFile.FileID,
			"role":      sampleFile.Role,
			"lane":      sampleFile.Lane,
			"replicate": sampleFile.Replicate,
		}).Error
}

func (r *dataRepository) DeleteSampleFile(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.SampleFile{}).Error
}

func (r *dataRepository) ListSampleFile(ctx context.Context) ([]*types.SampleFile, error) {
	items := make([]*types.SampleFile, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *dataRepository) CreateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error {
	return r.db.WithContext(ctx).Create(datasetSample).Error
}

func (r *dataRepository) GetDatasetSampleByID(ctx context.Context, id int64) (*types.DatasetSample, error) {
	item := &types.DatasetSample{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *dataRepository) UpdateDatasetSample(ctx context.Context, datasetSample *types.DatasetSample) error {
	return r.db.WithContext(ctx).Model(&types.DatasetSample{}).
		Where("id = ?", datasetSample.ID).
		Updates(map[string]interface{}{
			"dataset_id": datasetSample.DatasetID,
			"sample_id":  datasetSample.SampleID,
		}).Error
}

func (r *dataRepository) DeleteDatasetSample(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.DatasetSample{}).Error
}

func (r *dataRepository) ListDatasetSample(ctx context.Context) ([]*types.DatasetSample, error) {
	items := make([]*types.DatasetSample, 0)
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

func (r *dataRepository) ExistsSampleByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Sample{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *dataRepository) DeleteDatasetWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dataset_id = ?", id).Delete(&types.ProjectDataset{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("dataset_id = ?", id).Delete(&types.DatasetSample{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.Dataset{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *dataRepository) DeleteFileWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ?", id).Delete(&types.DatasetFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id = ?", id).Delete(&types.SampleFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.File{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *dataRepository) DeleteSampleWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("sample_id = ?", id).Delete(&types.DatasetSample{}).Error; err != nil {
			return err
		}
		if err := tx.Where("sample_id = ?", id).Delete(&types.SampleFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.Sample{}).Error; err != nil {
			return err
		}
		return nil
	})
}
