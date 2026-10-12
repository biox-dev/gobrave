package handler

import (
	stderrs "errors"
	"net/http"
	"os"

	"github.com/biox-dev/gobrave/internal/application/service"
	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type SheetHandler struct {
	sheetService interfaces.SheetFileService
	dataService  interfaces.DataService
}

func NewSheetHandler(sheetService interfaces.SheetFileService, dataService interfaces.DataService) *SheetHandler {
	return &SheetHandler{sheetService: sheetService, dataService: dataService}
}

type readWorkbookQuery struct {
	FilePath string `form:"file_path" binding:"required"`
	Format   string `form:"format"`
}

type writeWorkbookRequest struct {
	FilePath     string         `json:"file_path" binding:"required"`
	Format       string         `json:"format"`
	WorkbookData map[string]any `json:"workbook_data" binding:"required"`
}

type readWorkbookByFileIDQuery struct {
	// ID is the go_file primary key (go_file.id).
	ID int64 `form:"id" binding:"required"`
}

type writeWorkbookByFileIDRequest struct {
	// ID is the go_file primary key (go_file.id).
	ID           int64          `json:"id,string" binding:"required"`
	WorkbookData map[string]any `json:"workbook_data" binding:"required"`
}

// ReadWorkbook godoc
// @Summary      读取本地工作簿
// @Description  从本地文件读取工作簿并转换为 Univer 可用结构。当前优先支持 xlsx，csv/tsv 将在后续版本支持。
// @Tags         表格
// @Produce      json
// @Param        file_path  query     string  true   "本地文件路径（相对路径以 sheets 目录为根）"
// @Param        format     query     string  false  "文件格式，可选：xlsx"
// @Success      200        {object}  interfaces.WorkbookReadResult
// @Failure      400        {object}  apperrors.AppError
// @Failure      401        {object}  apperrors.AppError
// @Failure      404        {object}  apperrors.AppError
// @Failure      500        {object}  apperrors.AppError
// @Security     Bearer
// @Router       /sheet/workbook [get]
func (h *SheetHandler) ReadWorkbook(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req readWorkbookQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.sheetService.ReadWorkbook(c.Request.Context(), req.FilePath, req.Format)
	if err != nil {
		switch {
		case stderrs.Is(err, os.ErrNotExist):
			c.Error(apperrors.NewNotFoundError("sheet file not found").WithDetails(err.Error()))
		case stderrs.Is(err, service.ErrUnsupportedSheetFormat):
			c.Error(apperrors.NewValidationError("unsupported sheet format").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to read sheet file").WithDetails(err.Error()))
		}
		return
	}

	c.JSON(http.StatusOK, result)
}

// WriteWorkbook godoc
// @Summary      保存工作簿到本地
// @Description  将 Univer 工作簿结构写入本地文件。当前优先支持 xlsx，csv/tsv 将在后续版本支持。
// @Tags         表格
// @Accept       json
// @Produce      json
// @Param        request  body      writeWorkbookRequest  true  "请求参数"
// @Success      200      {object}  interfaces.WorkbookWriteResult
// @Failure      400      {object}  apperrors.AppError
// @Failure      401      {object}  apperrors.AppError
// @Failure      500      {object}  apperrors.AppError
// @Security     Bearer
// @Router       /sheet/workbook/save [post]
func (h *SheetHandler) WriteWorkbook(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req writeWorkbookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request payload").WithDetails(err.Error()))
		return
	}

	result, err := h.sheetService.WriteWorkbook(c.Request.Context(), req.FilePath, req.Format, req.WorkbookData)
	if err != nil {
		switch {
		case stderrs.Is(err, service.ErrUnsupportedSheetFormat):
			c.Error(apperrors.NewValidationError("unsupported sheet format").WithDetails(err.Error()))
		case stderrs.Is(err, service.ErrInvalidWorkbookData):
			c.Error(apperrors.NewValidationError("invalid workbook data").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to write sheet file").WithDetails(err.Error()))
		}
		return
	}

	c.JSON(http.StatusOK, result)
}

// ReadWorkbookByFileID godoc
// @Summary      根据文件ID读取工作簿
// @Description  根据 go_file 主键 ID 查找文件 path，由 path 后缀推断读取格式（前端无需传递 format），再读取工作簿并转换为 Univer 可用结构。结果中附带文件信息；不支持的格式返回“页面不支持预览”。
// @Tags         表格
// @Produce      json
// @Param        id       query     integer  true   "文件主键 ID"
// @Success      200      {object}  interfaces.WorkbookReadResult
// @Failure      400      {object}  apperrors.AppError
// @Failure      401      {object}  apperrors.AppError
// @Failure      404      {object}  apperrors.AppError
// @Failure      500      {object}  apperrors.AppError
// @Security     Bearer
// @Router       /sheet/workbook/by-file-id [get]
func (h *SheetHandler) ReadWorkbookByFileID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req readWorkbookByFileIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	file, err := h.dataService.GetFileByID(c.Request.Context(), req.ID)
	if err != nil {
		c.Error(apperrors.NewNotFoundError("file not found").WithDetails(err.Error()))
		return
	}

	// 文件格式由文件路径后缀推断，前端不再传递 format。
	result, err := h.sheetService.ReadWorkbook(c.Request.Context(), file.Path, "")
	if err != nil {
		switch {
		case stderrs.Is(err, os.ErrNotExist):
			c.Error(apperrors.NewNotFoundError("sheet file not found").WithDetails(err.Error()))
		case stderrs.Is(err, service.ErrUnsupportedSheetFormat):
			c.Error(apperrors.NewValidationError("页面不支持预览").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to read sheet file").WithDetails(err.Error()))
		}
		return
	}

	result.File = file

	c.JSON(http.StatusOK, result)
}

// WriteWorkbookByFileID godoc
// @Summary      根据文件ID保存工作簿
// @Description  根据 go_file 主键 ID 查找文件 path，再将 Univer 工作簿结构写入本地文件。
// @Tags         表格
// @Accept       json
// @Produce      json
// @Param        request  body      writeWorkbookByFileIDRequest  true  "请求参数"
// @Success      200      {object}  interfaces.WorkbookWriteResult
// @Failure      400      {object}  apperrors.AppError
// @Failure      401      {object}  apperrors.AppError
// @Failure      404      {object}  apperrors.AppError
// @Failure      500      {object}  apperrors.AppError
// @Security     Bearer
// @Router       /sheet/workbook/save/by-file-id [post]
func (h *SheetHandler) WriteWorkbookByFileID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req writeWorkbookByFileIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request payload").WithDetails(err.Error()))
		return
	}

	file, err := h.dataService.GetFileByID(c.Request.Context(), req.ID)
	if err != nil {
		c.Error(apperrors.NewNotFoundError("file not found").WithDetails(err.Error()))
		return
	}

	// 文件格式由文件路径后缀推断，前端不再传递 format。
	result, err := h.sheetService.WriteWorkbook(c.Request.Context(), file.Path, "", req.WorkbookData)
	if err != nil {
		switch {
		case stderrs.Is(err, service.ErrUnsupportedSheetFormat):
			c.Error(apperrors.NewValidationError("页面不支持预览").WithDetails(err.Error()))
		case stderrs.Is(err, service.ErrInvalidWorkbookData):
			c.Error(apperrors.NewValidationError("invalid workbook data").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to write sheet file").WithDetails(err.Error()))
		}
		return
	}

	c.JSON(http.StatusOK, result)
}
