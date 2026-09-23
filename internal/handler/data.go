package handler

import (
	"encoding/csv"
	"encoding/json"
	stderrs "errors"
	"net/http"
	"os"
	"strings"

	appservice "github.com/biox-dev/gobrave/internal/application/service"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DataHandler struct {
	dataService    interfaces.DataService
	projectService interfaces.ProjectService
}

func NewDataHandler(dataService interfaces.DataService, projectService interfaces.ProjectService) *DataHandler {
	return &DataHandler{dataService: dataService, projectService: projectService}
}

type idQuery struct {
	ID int64 `form:"id" binding:"required"`
}

type idBody struct {
	ID int64 `json:"id,string" binding:"required"`
}

type projectIDQuery struct {
	ProjectID string `form:"project_id" binding:"required"`
}

type datasetByProjectPageRequest struct {
	types.Pagination
	types.QueryDataset
}

type projectFileQuery struct {
	ProjectID string   `form:"project_id" binding:"required"`
	Roles     []string `form:"role"`
}

type projectFilePageRequest struct {
	types.Pagination
	// ProjectID string   `json:"project_id" binding:"required"`
	Roles []string `json:"role"`
}

type assayByProjectPageRequest struct {
	types.Pagination
	// ProjectID string `json:"project_id" binding:"required"`
}

type subjectPageRequest struct {
	types.Pagination

	SubjectName string `json:"subject_name"`
	Species     string `json:"species"`
	Strain      string `json:"strain"`
	Sex         string `json:"sex"`
}

type samplePageRequest struct {
	types.Pagination

	SampleID   string `json:"sample_id"`
	SampleName string `json:"sample_name"`
	SubjectID  *int64 `json:"subject_id,string"`
	Tissue     string `json:"tissue"`
	CellType   string `json:"cell_type"`
}

// assayIDQuery addresses a resource by its owning assay (int64 PK).
type assayIDQuery struct {
	AssayID int64 `form:"assay_id" binding:"required"`
}

func handleDataError(c *gin.Context, err error, internalMsg string) {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	// 服务层已经明确 HTTP 语义（例如 409 冲突）时直接透传，避免被降级成 500。
	var appErr *errors.AppError
	if stderrs.As(err, &appErr) {
		c.Error(appErr)
		return
	}
	c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
}

// buildFileColumns reads the TSV header at path and returns a column list
// compatible with the Python build_collected_analysis_result output.
func buildFileColumns(path string, fileID int64) ([]map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, err
	}

	columns := make([]map[string]interface{}, 0, len(header))
	for _, col := range header {
		columns = append(columns, map[string]interface{}{
			"id":                 fileID,
			"analysis_result_id": fileID,
			"assay_name":         col,
			"columns_name":       col,
		})
	}
	return columns, nil
}

func buildCompatFileItem(item *types.FileWithDatasetInfo) (map[string]interface{}, error) {
	b, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}

	// if v, ok := result["path"]; ok {
	// 	// result["content"] = v
	// 	result["label"] = result["file_name"] // 保持 label 字段兼容
	// 	result["value"] = result["id"]        // 保持 value 字段兼容
	// 	// delete(result, "path")
	// }

	result["label"] = result["file_name"] // 保持 label 字段兼容
	result["value"] = result["id"]        // 保持 value 字段兼容
	// For EXP/TABLE roles, read TSV header and attach columns (mirrors Python build_collected_analysis_result).
	if (item.Role == "EXP" || item.Role == "TABLE") && item.Path != "" {
		if cols, err := buildFileColumns(item.Path, item.ID); err == nil {
			result["columns"] = cols
		}
		// silently skip if file is missing or unreadable
	}

	return result, nil
}

// CreateDataset godoc
// @Summary      创建数据集
// @Description  在当前用户激活的项目下创建 Dataset 记录，并自动建立 ProjectDataset 关联；主键由服务端生成
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.CreateDatasetRequest  true  "请求参数"
// @Success      200      {object}  types.Dataset
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/create [post]
func (h *DataHandler) CreateDataset(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to get active project")
		return
	}

	var req types.CreateDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	dataset := req.ToDataset()
	if err := h.dataService.CreateDataset(c.Request.Context(), dataset, project.ProjectID); err != nil {
		handleDataError(c, err, "failed to create dataset")
		return
	}

	c.JSON(http.StatusOK, dataset)
}

// EnsureDatasetDir godoc
// @Summary      创建数据集目录
// @Description  校验 Dataset 存在后，基于当前用户激活的项目创建数据集目录（目录已存在则直接返回）
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/ensure-dir [post]
func (h *DataHandler) EnsureDatasetDir(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to get active project")
		return
	}

	dir, err := h.dataService.EnsureDatasetDir(c.Request.Context(), req.ID, project.ProjectID)
	if err != nil {
		handleDataError(c, err, "failed to ensure dataset dir")
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": dir})
}

// GetDataset godoc
// @Summary      获取数据集
// @Description  按 ID 查询 Dataset 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer           true  "主键 ID"
// @Success      200      {object}  types.Dataset
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/get [get]
func (h *DataHandler) GetDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetDatasetByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get dataset")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateDataset godoc
// @Summary      更新数据集
// @Description  按 ID 更新 Dataset 记录，ID 由请求显式指定，其余字段为业务字段
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.UpdateDatasetRequest  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/update [post]
func (h *DataHandler) UpdateDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.UpdateDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.UpdateDataset(c.Request.Context(), req.ToDataset()); err != nil {
		handleDataError(c, err, "failed to update dataset")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset updated successfully"})
}

// DeleteDataset godoc
// @Summary      删除数据集
// @Description  按 ID 删除 Dataset，并手动清理关联映射关系
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/delete [post]
func (h *DataHandler) DeleteDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteDataset(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete dataset")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset deleted successfully"})
}

// ListDataset godoc
// @Summary      数据集列表
// @Description  查询 Dataset 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.Dataset
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/list [get]
func (h *DataHandler) ListDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListDataset(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list dataset")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageDatasetByProjectID godoc
// @Summary      按项目分页查询数据集
// @Description  根据当前用户激活的项目分页查询关联的 Dataset 列表
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      datasetByProjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset/list-by-project-page [post]
func (h *DataHandler) PageDatasetByProjectID(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req datasetByProjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to get active project")
		return
	}

	result, err := h.dataService.PageDatasetByProjectID(c.Request.Context(), &req.Pagination, &req.QueryDataset, project.ProjectID)
	if err != nil {
		handleDataError(c, err, "failed to page dataset by project id")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// PageFileByProjectID godoc
// @Summary      按项目分页查询文件
// @Description  根据 project_id 分页查询项目下关联文件，并返回 role、dataset_name、dataset_id 等信息
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      projectFilePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/list-by-project-page [post]
func (h *DataHandler) PageFileByProjectID(c *gin.Context) {
	userID, ok := getCurrentUserID(c)

	if !ok {
		c.Error(errors.NewUnauthorizedError("unauthorized"))
		return
	}

	var req projectFilePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	roles := make([]string, 0, len(req.Roles))
	for _, item := range req.Roles {
		for _, role := range strings.Split(item, ",") {
			role = strings.TrimSpace(role)
			if role != "" {
				roles = append(roles, role)
			}
		}
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)

	result, err := h.dataService.PageFileByProjectID(c.Request.Context(), &req.Pagination, project.ProjectID, roles)
	if err != nil {
		handleDataError(c, err, "failed to page file by project id")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// CreateProjectDataset godoc
// @Summary      创建项目-数据集映射
// @Description  创建 ProjectDataset 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ProjectDataset  true  "请求参数"
// @Success      200      {object}  types.ProjectDataset
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/project-dataset/create [post]
func (h *DataHandler) CreateProjectDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ProjectDataset
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateProjectDataset(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create project dataset")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetProjectDataset godoc
// @Summary      获取项目-数据集映射
// @Description  按 ID 查询 ProjectDataset 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer               true  "主键 ID"
// @Success      200      {object}  types.ProjectDataset
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/project-dataset/get [get]
func (h *DataHandler) GetProjectDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetProjectDatasetByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get project dataset")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateProjectDataset godoc
// @Summary      更新项目-数据集映射
// @Description  按 ID 更新 ProjectDataset 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ProjectDataset  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/project-dataset/update [post]
func (h *DataHandler) UpdateProjectDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ProjectDataset
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateProjectDataset(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update project dataset")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project dataset updated successfully"})
}

// DeleteProjectDataset godoc
// @Summary      删除项目-数据集映射
// @Description  按 ID 删除 ProjectDataset 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody                true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/project-dataset/delete [post]
func (h *DataHandler) DeleteProjectDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteProjectDataset(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete project dataset")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project dataset deleted successfully"})
}

// ListProjectDataset godoc
// @Summary      项目-数据集映射列表
// @Description  查询 ProjectDataset 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.ProjectDataset
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/project-dataset/list [get]
func (h *DataHandler) ListProjectDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListProjectDataset(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list project dataset")
		return
	}

	c.JSON(http.StatusOK, items)
}

// CreateFile godoc
// @Summary      创建文件
// @Description  创建 File 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.File        true  "请求参数"
// @Success      200      {object}  types.File
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/create [post]
func (h *DataHandler) CreateFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.File
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateFile(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create file")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetFile godoc
// @Summary      获取文件
// @Description  按 ID 查询 File 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer           true  "主键 ID"
// @Success      200      {object}  types.File
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/get [get]
func (h *DataHandler) GetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetFileByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get file")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateFile godoc
// @Summary      更新文件
// @Description  按 ID 更新 File 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.File        true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/update [post]
func (h *DataHandler) UpdateFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.File
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateFile(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update file")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "file updated successfully"})
}

// DeleteFile godoc
// @Summary      删除文件
// @Description  按 ID 删除 File，并手动清理关联映射关系
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/delete [post]
func (h *DataHandler) DeleteFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteFile(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete file")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "file deleted successfully"})
}

// ListFile godoc
// @Summary      文件列表
// @Description  查询 File 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.File
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/list [get]
func (h *DataHandler) ListFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListFile(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list file")
		return
	}

	c.JSON(http.StatusOK, items)
}

// ListFileByAssayID godoc
// @Summary      按 Assay 查询文件列表
// @Description  返回某个 Assay 所拥有的全部文件（无文件时返回空数组）
// @Tags         数据管理
// @Produce      json
// @Param        assay_id  query     integer           true  "Assay 主键 ID"
// @Success      200       {array}   types.File
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/list-by-assay [get]
func (h *DataHandler) ListFileByAssayID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req assayIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	items, err := h.dataService.ListFileByAssayID(c.Request.Context(), req.AssayID)
	if err != nil {
		handleDataError(c, err, "failed to list file by assay id")
		return
	}

	c.JSON(http.StatusOK, items)
}

// ListFileByProjectID godoc
// @Summary      按项目查询文件列表
// @Description  根据 project_id 查询关联的所有文件；支持按 go_dataset_file.role 过滤
// @Tags         数据管理
// @Produce      json
// @Param        project_id  query     string           true  "项目业务ID"
// @Param        role        query     []string         false "角色过滤，可多选；不传默认查询全部" collectionFormat(multi)
// @Success      200         {array}   types.FileWithDatasetInfo
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/list-by-project [get]
func (h *DataHandler) ListFileByProjectID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req projectFileQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	roles := make([]string, 0, len(req.Roles))
	for _, item := range req.Roles {
		for _, role := range strings.Split(item, ",") {
			role = strings.TrimSpace(role)
			if role != "" {
				roles = append(roles, role)
			}
		}
	}

	items, err := h.dataService.ListFileByProjectID(c.Request.Context(), req.ProjectID, roles)
	if err != nil {
		handleDataError(c, err, "failed to list file by project id")
		return
	}

	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		compatItem, err := buildCompatFileItem(item)
		if err != nil {
			handleDataError(c, err, "failed to build file response")
			return
		}
		result = append(result, compatItem)
	}

	c.JSON(http.StatusOK, result)
}

// ListFileByProjectIDGroupByRole godoc
// @Summary      按项目查询文件列表并按角色分组
// @Description  根据 project_id 查询关联文件，并按 role 分组
// @Tags         数据管理
// @Produce      json
// @Param        project_id  query     string                      true  "项目业务ID"
// @Success      200         {object}  map[string][]types.FileWithDatasetInfo
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /data/file/list-by-project-group [get]
func (h *DataHandler) ListFileByProjectIDGroupByRole(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req projectIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	items, err := h.dataService.ListFileByProjectIDGroupByRole(c.Request.Context(), req.ProjectID)
	if err != nil {
		handleDataError(c, err, "failed to list grouped file by project id")
		return
	}

	result := make(map[string]interface{}, len(items))
	for _, group := range items {
		groupItems := make([]map[string]interface{}, 0, len(group.Items))
		for _, item := range group.Items {
			compatItem, err := buildCompatFileItem(item)
			if err != nil {
				handleDataError(c, err, "failed to build grouped file response")
				return
			}
			groupItems = append(groupItems, compatItem)
		}
		result[group.Role] = groupItems
	}

	c.JSON(http.StatusOK, result)
}

// AddFileToDataset godoc
// @Summary      按路径添加文件到数据集
// @Description  根据 BaseDir + path 检查文件是否存在，创建 File 记录并关联到 Dataset
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.AddFileToDatasetRequest  true  "请求参数"
// @Success      200      {object}  types.AddFileToDatasetResponse
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/add-file [post]
func (h *DataHandler) AddFileToDataset(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	userID, _ := getCurrentUserID(c)
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to get active project")
		return
	}

	var req types.AddFileToDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	req.ProjectID = project.ProjectID

	result, err := h.dataService.AddFileToDataset(c.Request.Context(), &req)
	if err != nil {
		if stderrs.Is(err, appservice.ErrDatasetFileAlreadyAdded) {
			c.Error(errors.NewConflictError("文件已经添加"))
			return
		}
		handleDataError(c, err, "failed to add file to dataset")
		return
	}

	c.JSON(http.StatusOK, result)
}

// CreateDatasetFile godoc
// @Summary      创建数据集-文件映射
// @Description  创建 DatasetFile 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.DatasetFile  true  "请求参数"
// @Success      200      {object}  types.DatasetFile
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/create [post]
func (h *DataHandler) CreateDatasetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.DatasetFile
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateDatasetFile(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create dataset file")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetDatasetFile godoc
// @Summary      获取数据集-文件映射
// @Description  按 ID 查询 DatasetFile 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer             true  "主键 ID"
// @Success      200      {object}  types.DatasetFile
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/get [get]
func (h *DataHandler) GetDatasetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetDatasetFileByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get dataset file")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateDatasetFile godoc
// @Summary      更新数据集-文件映射
// @Description  根据 DatasetID + FileID 更新 DatasetFile 的 Role 字段
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.UpdateDatasetFileRequest  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/update [post]
func (h *DataHandler) UpdateDatasetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.UpdateDatasetFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	// Convert DTO to domain model
	datasetFile := &types.DatasetFile{
		DatasetID: req.DatasetID,
		FileID:    req.FileID,
		Role:      req.Role,
	}

	if err := h.dataService.UpdateDatasetFile(c.Request.Context(), datasetFile); err != nil {
		handleDataError(c, err, "failed to update dataset file")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset file updated successfully"})
}

// DeleteDatasetFile godoc
// @Summary      删除数据集-文件映射
// @Description  按 ID 删除 DatasetFile 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody              true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/delete [post]
func (h *DataHandler) DeleteDatasetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteDatasetFile(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete dataset file")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset file deleted successfully"})
}

// ListDatasetFile godoc
// @Summary      数据集-文件映射列表
// @Description  查询 DatasetFile 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.DatasetFile
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-file/list [get]
func (h *DataHandler) ListDatasetFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListDatasetFile(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list dataset file")
		return
	}

	c.JSON(http.StatusOK, items)
}

// CreateAssay godoc
// @Summary      创建 Assay
// @Description  创建 Assay 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Assay      true  "请求参数"
// @Success      200      {object}  types.Assay
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/create [post]
func (h *DataHandler) CreateAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Assay
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateAssay(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create assay")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetAssay godoc
// @Summary      获取 Assay
// @Description  按 ID 查询 Assay 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer           true  "主键 ID"
// @Success      200      {object}  types.Assay
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/get [get]
func (h *DataHandler) GetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetAssayByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get assay")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateAssay godoc
// @Summary      更新 Assay
// @Description  按 ID 更新 Assay 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Assay      true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/update [post]
func (h *DataHandler) UpdateAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Assay
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateAssay(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update assay")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "assay updated successfully"})
}

// DeleteAssay godoc
// @Summary      删除 Assay
// @Description  按 ID 删除 Assay，并手动清理关联映射关系
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/delete [post]
func (h *DataHandler) DeleteAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteAssay(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete assay")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "assay deleted successfully"})
}

// ListAssay godoc
// @Summary      Assay 列表
// @Description  查询 Assay 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.Assay
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/list [get]
func (h *DataHandler) ListAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListAssay(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list assay")
		return
	}

	c.JSON(http.StatusOK, items)
}

// ListAssayByProjectID godoc
// @Summary      按项目查询 Assay 列表
// @Description  根据 project_id 查询关联的所有 Assay
// @Tags         数据管理
// @Produce      json
// @Param        project_id  query     string          true  "项目业务ID"
// @Success      200         {array}   types.AssayWithDatasetInfo
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/list-by-project [get]
func (h *DataHandler) ListAssayByProjectID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req projectIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	items, err := h.dataService.ListAssayByProjectID(c.Request.Context(), req.ProjectID, nil)
	if err != nil {
		handleDataError(c, err, "failed to list assay by project id")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageAssayByProjectID godoc
// @Summary      按项目分页查询 Assay
// @Description  根据 project_id 分页查询项目下关联 Assay，并返回 dataset_name、dataset_id 等信息
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      assayByProjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/assay/list-by-project-page [post]
func (h *DataHandler) PageAssayByProjectID(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req assayByProjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to get active project by user id")
		return
	}

	result, err := h.dataService.PageAssayByProjectID(c.Request.Context(), &req.Pagination, project.ProjectID)
	if err != nil {
		handleDataError(c, err, "failed to page assay by project id")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// CreateDatasetAssay godoc
// @Summary      创建数据集-Assay 映射
// @Description  创建 DatasetAssay 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.DatasetAssay  true  "请求参数"
// @Success      200      {object}  types.DatasetAssay
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/create [post]
func (h *DataHandler) CreateDatasetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.DatasetAssay
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateDatasetAssay(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create dataset assay")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetDatasetAssay godoc
// @Summary      获取数据集-Assay 映射
// @Description  按 ID 查询 DatasetAssay 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer               true  "主键 ID"
// @Success      200      {object}  types.DatasetAssay
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/get [get]
func (h *DataHandler) GetDatasetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetDatasetAssayByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get dataset assay")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateDatasetAssay godoc
// @Summary      更新数据集-Assay 映射
// @Description  按 ID 更新 DatasetAssay 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.DatasetAssay   true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/update [post]
func (h *DataHandler) UpdateDatasetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.DatasetAssay
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateDatasetAssay(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update dataset assay")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset assay updated successfully"})
}

// DeleteDatasetAssay godoc
// @Summary      删除数据集-Assay 映射
// @Description  按 ID 删除 DatasetAssay 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody                true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/delete [post]
func (h *DataHandler) DeleteDatasetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteDatasetAssay(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete dataset assay")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "dataset assay deleted successfully"})
}

// ListDatasetAssay godoc
// @Summary      数据集-Assay 映射列表
// @Description  查询 DatasetAssay 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.DatasetAssay
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/list [get]
// ListDatasetAssay godoc
// @Summary      数据集-Assay 映射列表
// @Description  查询 DatasetAssay 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.DatasetAssay
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/list [get]
func (h *DataHandler) ListDatasetAssay(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListDatasetAssay(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list dataset assay")
		return
	}

	c.JSON(http.StatusOK, items)
}

// GetDatasetAssayByAssayID godoc
// @Summary      按 Assay 查询数据集映射
// @Description  返回 Assay 绑定的 DatasetAssay；尚未绑定任何数据集时返回 null
// @Tags         数据管理
// @Produce      json
// @Param        assay_id  query     integer               true  "Assay 主键 ID"
// @Success      200       {object}  types.DatasetAssay
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /data/dataset-assay/get-by-assay [get]
func (h *DataHandler) GetDatasetAssayByAssayID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req assayIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetDatasetAssayByAssayID(c.Request.Context(), req.AssayID)
	if err != nil {
		// An assay that is not bound to a dataset yet is a normal state, not an error.
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, nil)
			return
		}
		handleDataError(c, err, "failed to get dataset assay by assay id")
		return
	}

	c.JSON(http.StatusOK, item)
}

// CreateSubject godoc
// @Summary      创建 Subject
// @Description  创建实验对象/个体记录，主键由服务端生成，subject_name 业务名必须唯一
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Subject  true  "请求参数"
// @Success      200      {object}  types.Subject
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/create [post]
func (h *DataHandler) CreateSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Subject
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateSubject(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create subject")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetSubject godoc
// @Summary      获取 Subject
// @Description  按 ID 查询 Subject 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer      true  "主键 ID"
// @Success      200      {object}  types.Subject
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/get [get]
func (h *DataHandler) GetSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetSubjectByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get subject")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateSubject godoc
// @Summary      更新 Subject
// @Description  按 ID 更新 Subject 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Subject     true  "请求参数"
// @Success      200      {object}  types.Subject
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/update [post]
func (h *DataHandler) UpdateSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Subject
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateSubject(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update subject")
		return
	}

	c.JSON(http.StatusOK, req)
}

// DeleteSubject godoc
// @Summary      删除 Subject
// @Description  按 ID 删除 Subject；当该 Subject 下仍存在 Sample 时返回 409，需先删除其 Sample
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/delete [post]
func (h *DataHandler) DeleteSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteSubject(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete subject")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "subject deleted successfully"})
}

// ListSubject godoc
// @Summary      Subject 列表
// @Description  查询 Subject 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.Subject
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/list [get]
func (h *DataHandler) ListSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListSubject(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list subject")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageSubject godoc
// @Summary      分页查询 Subject
// @Description  按 subject_name / species / strain / sex 过滤并分页
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      subjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/subject/page [post]
func (h *DataHandler) PageSubject(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req subjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.dataService.PageSubject(c.Request.Context(), &req.Pagination, &types.QuerySubject{
		SubjectName: req.SubjectName,
		Species:     req.Species,
		Strain:      req.Strain,
		Sex:         req.Sex,
	})
	if err != nil {
		handleDataError(c, err, "failed to page subject")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// CreateSample godoc
// @Summary      创建 Sample
// @Description  创建采样记录，主键由服务端生成；sample_id 业务号唯一，subject_id 必须指向已存在的 Subject 主键
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Sample  true  "请求参数"
// @Success      200      {object}  types.Sample
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/create [post]
func (h *DataHandler) CreateSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Sample
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.CreateSample(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create sample")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetSample godoc
// @Summary      获取 Sample
// @Description  按 ID 查询 Sample 详情
// @Tags         数据管理
// @Produce      json
// @Param        id       query     integer     true  "主键 ID"
// @Success      200      {object}  types.Sample
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/get [get]
func (h *DataHandler) GetSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.dataService.GetSampleByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get sample")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateSample godoc
// @Summary      更新 Sample
// @Description  按 ID 更新 Sample 记录
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.Sample      true  "请求参数"
// @Success      200      {object}  types.Sample
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/update [post]
func (h *DataHandler) UpdateSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Sample
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.dataService.UpdateSample(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update sample")
		return
	}

	c.JSON(http.StatusOK, req)
}

// DeleteSample godoc
// @Summary      删除 Sample
// @Description  按 ID 删除 Sample；当该 Sample 下仍存在 Assay 时返回 409，需先删除其 Assay
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody            true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/delete [post]
func (h *DataHandler) DeleteSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.dataService.DeleteSample(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete sample")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "sample deleted successfully"})
}

// ListSample godoc
// @Summary      Sample 列表
// @Description  查询 Sample 列表
// @Tags         数据管理
// @Produce      json
// @Success      200      {array}   types.Sample
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/list [get]
func (h *DataHandler) ListSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.dataService.ListSample(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list sample")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageSample godoc
// @Summary      分页查询 Sample
// @Description  按 sample_id / sample_name / subject_id（Subject 主键）/ tissue / cell_type 过滤并分页，附带 Subject 业务名
// @Tags         数据管理
// @Accept       json
// @Produce      json
// @Param        request  body      samplePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /data/sample/page [post]
func (h *DataHandler) PageSample(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req samplePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.dataService.PageSample(c.Request.Context(), &req.Pagination, &types.QuerySample{
		SampleID:   req.SampleID,
		SampleName: req.SampleName,
		SubjectID:  req.SubjectID,
		Tissue:     req.Tissue,
		CellType:   req.CellType,
	})
	if err != nil {
		handleDataError(c, err, "failed to page sample")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}
