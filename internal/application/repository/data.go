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

// ListFileByProjectIDViaAnalysisNode returns the files produced by the project's
// DAG analysis nodes. Unlike ListFileByProjectID (which walks the
// go_project_dataset -> go_dataset_file chain), the binding here is the file's
// own go_file.analysis_node_id -> analysis_nodes.id. analysis_nodes.project_id
// holds the numeric project PK (t_project.id), so the business project_id is
// resolved through t_project.id. These files are node-private and not
// dataset-bound, so dataset_id/dataset_name stay empty; role is filled from the
// file's own file_key for shape compatibility, and the caller decides how to
// group them.
func (r *dataRepository) ListFileByProjectIDViaAnalysisNode(ctx context.Context, projectID string) ([]*types.FileWithDatasetInfo, error) {
	items := make([]*types.FileWithDatasetInfo, 0)
	err := r.db.WithContext(ctx).
		Table("analysis_nodes AS an").
		Select(`
			f.id,
			f.file_id,
			f.file_name,
			f.path,
			f.format,
			f.analysis_node_id,
			f.size,
			f.md5,
			f.storage,
			f.description,
			f.created_at,
			f.updated_at,
			f.file_key AS role
		`).
		Joins("JOIN t_project AS p ON p.id = an.project_id").
		Joins("JOIN go_file AS f ON f.analysis_node_id = an.id").
		Where("p.project_id = ?", projectID).
		Order("f.id DESC").
		Find(&items).Error
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
			"sample_name": assay.SampleName,
			"assay_type":  assay.AssayType,
			"platform":    assay.Platform,
			"library_id":  assay.LibraryID,
			"role":        assay.Role,
			"metadata":    assay.Metadata,
			"description": assay.Description,
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
// assay row plus the dataset it is bound to through go_dataset_assay. The assay
// itself carries its SampleName, so no sample join is needed.
const assayWithDatasetSelect = `
	a.id,
	a.sample_name,
	a.assay_type,
	a.platform,
	a.library_id,
	a.role,
	a.metadata,
	a.description,
	a.created_at,
	a.updated_at,
	d.id AS dataset_id,
	d.dataset_name`

// assayByProjectBase builds the query resolving a project's assays through the
// project's datasets: go_project_dataset -> go_dataset_assay -> go_assay. A
// dataset binds directly to the assay, and files follow the assay.
func (r *dataRepository) assayByProjectBase(ctx context.Context, projectID string) *gorm.DB {
	return r.db.WithContext(ctx).
		Table("go_project_dataset AS pd").
		Select(assayWithDatasetSelect).
		Joins("JOIN go_dataset AS d ON d.id = pd.dataset_id").
		Joins("JOIN go_dataset_assay AS da ON da.dataset_id = d.id").
		Joins("JOIN go_assay AS a ON a.id = da.assay_id").
		Where("pd.project_id = ?", projectID)
}

func (r *dataRepository) PageAssayByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.AssayWithDatasetInfo, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.AssayWithDatasetInfo, 0)
	var total int64

	if err := r.assayByProjectBase(ctx, projectID).
		Select("COUNT(DISTINCT a.id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	// Grouping by the joined PKs (a.id, d.id) keeps the extra columns valid
	// under MySQL's ONLY_FULL_GROUP_BY while collapsing the row to one per assay
	// (an assay bound to several datasets would otherwise duplicate).
	err := r.assayByProjectBase(ctx, projectID).
		Group("a.id, d.id").
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

// ListAssayByProjectID returns the project's assays, resolved through the
// project's datasets. When roles is non-empty the query filters on
// go_assay.role; an empty roles slice adds no role condition at all, so every
// assay of the project is returned.
func (r *dataRepository) ListAssayByProjectID(ctx context.Context, projectID string, roles []string) ([]*types.AssayWithDatasetInfo, error) {
	items := make([]*types.AssayWithDatasetInfo, 0)
	query := r.assayByProjectBase(ctx, projectID)

	if len(roles) > 0 {
		query = query.Where("a.role IN ?", roles)
	}

	err := query.
		Group("a.id, d.id").
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

// ExistsAssayNameInDataset reports whether another assay with the same
// sample_name + role (excluding excludeAssayID) is already bound to datasetID.
// sample_name is only unique inside a dataset (per role), so the check joins
// go_dataset_assay instead of looking at go_assay alone.
func (r *dataRepository) ExistsAssayNameInDataset(ctx context.Context, datasetID int64, sampleName, role string, excludeAssayID int64) (bool, error) {
	if datasetID == 0 || strings.TrimSpace(sampleName) == "" {
		return false, nil
	}

	var count int64
	query := r.db.WithContext(ctx).
		Table("go_assay AS a").
		Joins("JOIN go_dataset_assay AS da ON da.assay_id = a.id").
		Where("da.dataset_id = ? AND a.sample_name = ? AND a.role = ?", datasetID, sampleName, role)
	if excludeAssayID != 0 {
		query = query.Where("a.id <> ?", excludeAssayID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetAssayByNameAndDatasetID resolves an assay by its business natural key inside
// one dataset (sample_name + role). Assays are only "in" a dataset through
// go_dataset_assay, so the lookup joins that binding; it returns
// gorm.ErrRecordNotFound when no assay is bound to the dataset yet (the importer
// then creates the assay and the binding).
func (r *dataRepository) GetAssayByNameAndDatasetID(ctx context.Context, datasetID int64, sampleName, role string) (*types.Assay, error) {
	item := &types.Assay{}
	err := r.db.WithContext(ctx).
		Table("go_assay AS a").
		Select("a.*").
		Joins("JOIN go_dataset_assay AS da ON da.assay_id = a.id").
		Where("da.dataset_id = ? AND a.sample_name = ? AND a.role = ?", datasetID, sampleName, role).
		Order("a.id ASC").
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

// GetFileByAnalysisNodeIDAndFileKey resolves a file produced by a DAG node by the
// node that produced it and its file key (an output_patterns handle, e.g. BAM).
// Files are node-private, so the lookup is scoped to the node and a re-run updates
// the existing row instead of inserting a duplicate.
func (r *dataRepository) GetFileByAnalysisNodeIDAndFileKey(ctx context.Context, analysisNodeID int64, fileKey string) (*types.File, error) {
	item := &types.File{}
	err := r.db.WithContext(ctx).
		Where("analysis_node_id = ? AND file_key = ?", analysisNodeID, fileKey).
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

// deleteAssayTreeTx removes one assay together with the files it owns, those
// files' dataset bindings (go_dataset_file) and the assay's own dataset bindings
// (go_dataset_assay). Files are assay-private, so they go away with their assay.
// It runs on an existing transaction so callers can cascade across a whole
// subtree.
func deleteAssayTreeTx(tx *gorm.DB, id int64) error {
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
	return tx.Where("id = ?", id).Delete(&types.Assay{}).Error
}

// DeleteAssayWithRelations removes an assay, its dataset bindings
// (go_dataset_assay) and the files it owns. Files are assay-private, so they go
// away with their assay (along with any dataset bindings those files had).
func (r *dataRepository) DeleteAssayWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return deleteAssayTreeTx(tx, id)
	})
}
