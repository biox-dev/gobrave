package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// PageStoreToolName 是「分页查询组件仓库」工具名。
const PageStoreToolName = "page_store"

// PageStoreInput 是 page_store 工具的入参。
//
// StoreType 必填（workflow / script）：store 按组件类型分区，两类组件不能混在一页里查。
// Keywords 会同时模糊匹配 name / category 列；分页与排序遵循 store 表的分页查询语义。
type PageStoreInput struct {
	StoreType string `json:"store_type"`
	Keywords  string `json:"keywords"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	SortBy    string `json:"sort_by"`
	SortOrder string `json:"sort_order"`
}

// PageStoreOutput 是 page_store 工具返回给模型的结果。
type PageStoreOutput struct {
	runtimecontext.StorePageResult
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// PageStore 返回一个「分页查询组件仓库」工具。
//
// 行为：
//   - store_type 必填（workflow / script），可按关键字搜索与分页 / 排序；
//   - user_id 取自执行事实，由服务层据此解析当前激活项目并回填 installed / installed_id；
//   - 返回每条 store 的 store_id（字符串，可直接传给 install_workflow / install_script）、
//     名称 / 分类 / 标签 / 本地目录，以及当前项目是否已安装。
//
// 该工具是只读的，因此声明 read 授权。
func PageStore(browser runtimecontext.StoreBrowser) tool.Tool {
	return tool.NewFunc(
		PageStoreToolName,
		`分页查询组件仓库（store）：查看可安装的工作流 / 脚本。

store 有两种组件类型，必须通过 store_type 指定其一：
- workflow：工作流 store（安装时用 install_workflow 按 store_id 安装）；
- script：脚本 store（安装时用 install_script 按 store_id 安装）。

可选参数：
- keywords：按名称 / 分类模糊搜索；
- page / page_size：分页（page_size 上限 1000，默认 20）；
- sort_by：id / name / category / created_at / updated_at；sort_order：ASC / DESC。

返回每条的 store_id（字符串，直接传给安装工具）、store_type、name、category、tags、origin（local=本地发布，remote=远程下载）、store_path（store 裸仓库目录）、installed / installed_id（当前激活项目是否已安装该组件及其主键）。需要先看清仓库里有什么、再决定安装哪个时使用。`,
		tool.Schema("分页查询组件仓库的入参", map[string]any{
			"store_type": map[string]any{
				"type":        "string",
				"enum":        []string{"workflow", "script"},
				"description": "组件类型（必填）：workflow 查工作流 store，script 查脚本 store。",
			},
			"keywords":   tool.StringProperty("按名称 / 分类模糊搜索的关键字；留空表示不过滤。"),
			"page":       tool.IntegerProperty("页码，从 1 开始；留空默认 1。"),
			"page_size":  tool.IntegerProperty("每页条数，上限 1000；留空默认 20。"),
			"sort_by":    tool.StringProperty("排序字段：id / name / category / created_at / updated_at；留空默认 id。"),
			"sort_order": tool.StringProperty("排序方向：ASC / DESC；留空默认 DESC。"),
		}, "store_type"),
		func(ctx context.Context, in PageStoreInput) (PageStoreOutput, error) {
			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return PageStoreOutput{}, fmt.Errorf("%s: user_id is required (no user in this conversation)", PageStoreToolName)
			}
			if browser == nil {
				return PageStoreOutput{}, fmt.Errorf("%s: store browser is not available in this runtime", PageStoreToolName)
			}

			result, err := browser.PageStore(ctx, userID, runtimecontext.StorePageInput{
				StoreType: in.StoreType,
				Keywords:  in.Keywords,
				Page:      in.Page,
				PageSize:  in.PageSize,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
			})
			out := PageStoreOutput{StorePageResult: result}
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}
			if out.Total == 0 {
				out.Message = fmt.Sprintf("没有符合条件的 %s store。", strings.ToLower(strings.TrimSpace(in.StoreType)))
			}
			return out, nil
		},
	).WithPermission("read", "", "分页查询组件仓库（workflow / script）")
}

// parseStoreID 解析必填的 store ID：为空或非法时返回错误。
//
// 容错处理用户 / 模型可能带上的 "@store:" 引用前缀，统一剥离后再解析。
func parseStoreID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@store:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@store:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("store_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid store_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("store_id must be a positive integer")
	}
	return id, nil
}
