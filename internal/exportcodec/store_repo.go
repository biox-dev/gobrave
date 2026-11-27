package exportcodec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/utils"
)

// 本文件提供「从 store 仓库读取导出文件」的共用实现。
//
// store 是裸仓库（本地 publish 与远程 clone 都是裸仓库），没有工作区文件，因此
// workflow.json / script.json 只能从 git 对象里读。handler（分页 / 详情回填 description）
// 与 container（page_store 工具）都需要这份读取逻辑，统一放在这里，避免两处口径漂移。

// ReadStoreExportJSON 读取 store 仓库 HEAD 提交里的导出文件（workflow.json / script.json）内容。
//
// 文件在仓库内的位置不固定，由 utils.FindFileInGitRepo 按「根目录优先、其次任意层级
// （忽略大小写）」查找后交给 utils.ReadFileFromGitRepo 读取。
func ReadStoreExportJSON(storeDir, fileName string) ([]byte, error) {
	relPath, err := utils.FindFileInGitRepo(storeDir, fileName)
	if err != nil {
		return nil, err
	}
	return utils.ReadFileFromGitRepo(storeDir, relPath)
}

// StoreFileNameAndField 按 store_type 返回该类型 store 仓库里的导出文件名，
// 以及承载组件元数据的子对象键（workflow / script）。
//
// store_type 不是 workflow / script 时返回 ok=false。
func StoreFileNameAndField(storeType string) (fileName, field string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(storeType)) {
	case "workflow":
		return WorkflowJSONFileName, "workflow", true
	case "script":
		return ScriptJSONFileName, "script", true
	default:
		return "", "", false
	}
}

// StoreDescription 读取 store 仓库 HEAD 提交里 workflow.json / script.json 的 description。
//
// 导出文件把组件元数据放在固定的子对象里（workflow / script），description 与 name /
// category 等字段同级。这里按子对象键读取而不是反序列化到具体版本的结构体，未来新增
// 导出格式版本时依然可用。
//
// 描述只用于展示，因此目录缺失、文件不存在、JSON 损坏、store_type 不支持等情况一律
// 返回空串，不向调用方抛错。
func StoreDescription(storeDir, storeType string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	fileName, field, ok := StoreFileNameAndField(storeType)
	if !ok {
		return ""
	}

	content, err := ReadStoreExportJSON(storeDir, fileName)
	if err != nil {
		return ""
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(content, &payload); err != nil {
		return ""
	}
	rawSection, ok := payload[field]
	if !ok {
		return ""
	}
	var section map[string]any
	if err := json.Unmarshal(rawSection, &section); err != nil {
		return ""
	}
	value, ok := section["description"]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
