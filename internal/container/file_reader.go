package container

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// previewRowLimit 是文件内容预览的最大行数（列名之外）。
const previewRowLimit = 5

// fileReader 实现 tool.FileReader：把「按 go_file 主键 ID 读取文件内容预览」的业务能力
// 暴露给内置工具 read_file_by_id。
//
// 它复用 DataService（ID → 文件元信息 / path）与 SheetFileService（表格解析）：对
// tsv / csv / xlsx（全部 sheet）解析出每个 sheet 的列名与前若干行数据；其它格式只返回
// 文件路径（Message 说明「不解析内容」）。
type fileReader struct {
	dataService  interfaces.DataService
	sheetService interfaces.SheetFileService
}

// newFileReader 构建文件读取器并注入内置工具体系。
func newFileReader(dataService interfaces.DataService, sheetService interfaces.SheetFileService) tool.FileReader {
	return &fileReader{
		dataService:  dataService,
		sheetService: sheetService,
	}
}

// ReadFileByID 按 go_file 主键 ID 读取文件，返回文件元信息与内容预览。
func (r *fileReader) ReadFileByID(ctx context.Context, fileID int64) (tool.FileReadResult, error) {
	if fileID <= 0 {
		return tool.FileReadResult{FileID: fileID}, fmt.Errorf("invalid file id")
	}
	if r.dataService == nil {
		return tool.FileReadResult{FileID: fileID}, fmt.Errorf("data service is not configured")
	}

	file, err := r.dataService.GetFileByID(ctx, fileID)
	if err != nil {
		return tool.FileReadResult{FileID: fileID}, fmt.Errorf("failed to get file by id: %w", err)
	}
	if file == nil {
		return tool.FileReadResult{FileID: fileID}, fmt.Errorf("file %d not found", fileID)
	}

	result := tool.FileReadResult{
		FileID:   file.ID,
		FileName: file.FileName,
		FilePath: file.Path,
		Format:   normalizePreviewFormat(file.Format, file.Path),
	}

	// 仅对 tsv / csv / xlsx 解析内容；其它格式只返回路径。
	switch result.Format {
	case "tsv", "csv", "xlsx":
	default:
		result.Message = "该文件格式暂不支持内容预览，仅返回文件路径。"
		return result, nil
	}

	if r.sheetService == nil {
		result.Message = "表格服务不可用，仅返回文件路径。"
		return result, nil
	}

	read, err := r.sheetService.ReadWorkbook(ctx, file.Path, "")
	if err != nil {
		// 解析失败不阻断：仍返回路径，把原因写进 Message 供模型参考。
		result.Message = fmt.Sprintf("解析文件内容失败：%v", err)
		return result, nil
	}

	result.Sheets = buildSheetPreviews(read.WorkbookData)
	if len(result.Sheets) == 0 {
		result.Message = "文件无可预览的表格数据。"
	}
	return result, nil
}

// normalizePreviewFormat 推断文件预览用的格式：优先用 file.Format，为空时回退到路径后缀。
func normalizePreviewFormat(format, path string) string {
	normalized := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(format, ".")))
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	return normalized
}

// buildSheetPreviews 把 ReadWorkbook 返回的 workbookData 转换为每个 sheet 的列名与预览行。
//
// workbookData 形如：
//
//	{"name":..., "sheetOrder":[...], "sheets":{sheetID:{"name":...,"cellData":{row:{"0":{"v":..}}}}}}
//
// 其中 cellData 的键是「行下标字符串」，值是「列下标字符串 → {v: 值}」。因此第一行
// （下标 "0"）被视为表头，其余前 previewRowLimit 行作为数据行。
func buildSheetPreviews(workbookData map[string]any) []tool.FileSheetTab {
	if workbookData == nil {
		return nil
	}

	rawSheets, ok := workbookData["sheets"].(map[string]any)
	if !ok || len(rawSheets) == 0 {
		return nil
	}

	// 按 sheetOrder 排序，缺省时按 sheetID 升序，保证输出稳定。
	order := sheetOrderOf(workbookData, rawSheets)

	out := make([]tool.FileSheetTab, 0, len(order))
	for _, sheetID := range order {
		rawSheet, ok := rawSheets[sheetID].(map[string]any)
		if !ok {
			continue
		}

		tab := tool.FileSheetTab{Name: sheetNameOf(rawSheet, sheetID)}
		tab.Columns, tab.Rows = previewFromCellData(cellDataOf(rawSheet))
		out = append(out, tab)
	}
	return out
}

// sheetOrderOf 返回 sheetID 的顺序：优先 workbookData.sheetOrder，否则按 sheetID 升序。
func sheetOrderOf(workbookData, rawSheets map[string]any) []string {
	// ReadWorkbook 返回的 sheetOrder 是 []string；JSON 反序列化路径上可能是 []any。
	switch order := workbookData["sheetOrder"].(type) {
	case []string:
		out := make([]string, 0, len(order))
		for _, id := range order {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) > 0 {
			return out
		}
	case []any:
		out := make([]string, 0, len(order))
		for _, v := range order {
			if id := strings.TrimSpace(fmt.Sprintf("%v", v)); id != "" {
				out = append(out, id)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	ids := make([]string, 0, len(rawSheets))
	for id := range rawSheets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sheetNameOf 取 sheet 名，缺省回退到 sheetID。
func sheetNameOf(rawSheet map[string]any, sheetID string) string {
	if name, ok := rawSheet["name"].(string); ok {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			return trimmed
		}
	}
	return sheetID
}

// cellDataOf 取 sheet 的 cellData。
func cellDataOf(rawSheet map[string]any) map[string]any {
	if cellData, ok := rawSheet["cellData"].(map[string]any); ok {
		return cellData
	}
	return nil
}

// previewFromCellData 从 cellData 提取列名（首行）与前 previewRowLimit 行数据。
//
// 列名取自首个非空行的值（通常为表头）；若该行没有可用值，则用列下标生成占位列名。
func previewFromCellData(cellData map[string]any) ([]string, []map[string]any) {
	if len(cellData) == 0 {
		return nil, nil
	}

	// 收集所有行下标并升序排列，得到逻辑上的行顺序。
	rowIndexes := make([]int, 0, len(cellData))
	for key := range cellData {
		if idx, err := strconv.Atoi(key); err == nil {
			rowIndexes = append(rowIndexes, idx)
		}
	}
	if len(rowIndexes) == 0 {
		return nil, nil
	}
	sort.Ints(rowIndexes)

	// 找出最大列下标，用于补齐列名与行单元格。
	maxCol := -1
	rowCells := make(map[int]map[int]any, len(rowIndexes))
	for _, idx := range rowIndexes {
		row, ok := cellData[strconv.Itoa(idx)].(map[string]any)
		if !ok {
			continue
		}
		cells := make(map[int]any, len(row))
		for colKey, cell := range row {
			colIdx, err := strconv.Atoi(colKey)
			if err != nil {
				continue
			}
			if colIdx > maxCol {
				maxCol = colIdx
			}
			cells[colIdx] = cellValueOf(cell)
		}
		rowCells[idx] = cells
	}
	if maxCol < 0 {
		return nil, nil
	}

	// 首行作为表头；若表头行在 cellData 中不存在，则按“首个存在行”作为表头。
	headerRow := rowIndexes[0]
	columns := make([]string, maxCol+1)
	for c := 0; c <= maxCol; c++ {
		header := ""
		if cells, ok := rowCells[headerRow]; ok {
			if v, ok := cells[c]; ok {
				header = strings.TrimSpace(fmt.Sprintf("%v", v))
			}
		}
		if header == "" {
			header = fmt.Sprintf("column_%d", c+1)
		}
		columns[c] = header
	}

	// 表头之后的若干行作为预览数据。
	rows := make([]map[string]any, 0, previewRowLimit)
	count := 0
	for _, idx := range rowIndexes {
		if idx == headerRow {
			continue
		}
		if count >= previewRowLimit {
			break
		}
		cells, ok := rowCells[idx]
		if !ok {
			continue
		}
		rowMap := make(map[string]any, len(columns))
		for c, name := range columns {
			if v, ok := cells[c]; ok {
				rowMap[name] = v
			} else {
				rowMap[name] = nil
			}
		}
		rows = append(rows, rowMap)
		count++
	}

	return columns, rows
}

// cellValueOf 从单元格结构 {"v": ...} 中取出 v 值；兼容直接传值的情形。
func cellValueOf(cell any) any {
	switch c := cell.(type) {
	case map[string]any:
		if v, ok := c["v"]; ok {
			return v
		}
		return nil
	case map[string]string:
		if v, ok := c["v"]; ok {
			return v
		}
		return nil
	default:
		return cell
	}
}
