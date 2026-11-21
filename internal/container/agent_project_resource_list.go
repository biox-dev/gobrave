package container

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/container/agentanalysis"
	"github.com/biox-dev/gobrave/internal/types"
)

// 本文件实现「浏览项目分析运行 / 分析节点」的列表能力：
//   - list_project_analysis_workflow：按 RuntimeContext.ProjectID 列出分析运行（nextflow 行）；
//   - list_project_analysis_node：按 RuntimeContext.ProjectID 列出分析节点。
//
// 两者都复用与 get_analysis_workflow / get_analysis_node 相同的路径口径：
// WorkspaceDir 由 repository 还原成绝对路径后，派生路径一律走 HydrateDerivedPaths，
// 不在调用点手写 join。列表只返回摘要，正文（params.json / run.sh）仍由按 ID 的
// 查看工具按需读取。

// 列表工具的默认 / 最大返回条数（与 builtin 侧的归一化阈值保持一致）。
const (
	defaultAnalysisListLimit = 50
	maxAnalysisListLimit     = 200
)

// ListAnalysesByProject 返回指定项目下的分析运行（按 id 倒序，不分页）。
//
// 过滤条件直接映射到 types.AnalysisQuey（名称 / 工作流 / 状态 / 缓存策略 / 报告标记），
// 关键字沿用 Pagination.Keyword（命中分析名称 / analysis_id）。条数在内存里截断：
// 项目内运行数量有限，一次多取一页即可，无需为工具层再引入分页参数。
func (b *projectResourceBrowser) ListAnalysesByProject(ctx context.Context, projectID int64, filters runtimecontext.AnalysisListFilters) ([]runtimecontext.AnalysisBrief, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("list_project_analysis_workflow: project_id is required")
	}
	if b.analysisRepo == nil {
		return nil, fmt.Errorf("list_project_analysis_workflow: required services are not configured")
	}

	limit := normalizeBrowserLimit(filters.Limit)
	query, err := analysisQueryFromFilters(filters.WorkflowID, filters.JobStatus, filters.CacheType, filters.IsReport)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_workflow: %w", err)
	}

	pagination := &types.Pagination{
		Page:     1,
		PageSize: limit,
		Keyword:  strings.TrimSpace(filters.Keyword),
	}
	analyses, _, err := b.analysisRepo.PageAnalysisByProjectID(ctx, pagination, projectID, query)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_workflow: failed to list analyses: %w", err)
	}

	out := make([]runtimecontext.AnalysisBrief, 0, len(analyses))
	for _, analysis := range analyses {
		if analysis == nil {
			continue
		}
		out = append(out, b.analysisBrief(ctx, analysis))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// analysisBrief 把一次分析运行转成只读摘要（工作流名称 + 节点状态分布）。
//
// 节点状态分布复用 analysisRepo.ListAnalysisNodesByAnalysisID：统计失败不影响摘要返回
// （此时 NodeCount 保持 0，避免把「统计失败」误报成「没有节点」）。
func (b *projectResourceBrowser) analysisBrief(ctx context.Context, analysis *types.Analysis) runtimecontext.AnalysisBrief {
	// repository 读取时已还原 WorkspaceDir 并填充派生路径；这里兜底再推导一次（幂等）。
	if analysis.ParamsPath == "" && analysis.WorkspaceDir != "" {
		analysis.HydrateDerivedPaths()
	}

	brief := runtimecontext.AnalysisBrief{
		AnalysisID:    analysis.ID,
		AnalysisKey:   analysis.AnalysisID,
		AnalysisName:  analysis.AnalysisName,
		ProjectID:     analysis.ProjectID,
		WorkflowID:    analysis.WorkflowID,
		JobStatus:     analysis.JobStatus,
		SchedulerMode: types.NormalizeSchedulerMode(analysis.SchedulerMode),
		CacheType:     analysis.CacheType,
		WorkspaceDir:  analysis.WorkspaceDir,
		ParamsPath:    analysis.ParamsPath,
		RequestParam:  agentanalysis.DecodeRequestParam(analysis.RequestParam),
	}
	if b.workflowSvc != nil && analysis.WorkflowID > 0 {
		if workflow, wfErr := b.workflowSvc.GetWorkflowByID(ctx, analysis.WorkflowID); wfErr == nil && workflow != nil {
			brief.WorkflowName = workflow.Name
		}
	}

	nodes, nodeErr := b.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if nodeErr == nil {
		counts := make(map[string]int, 8)
		for _, node := range nodes {
			if node == nil {
				continue
			}
			status := strings.TrimSpace(node.Status)
			if status == "" {
				status = "unknown"
			}
			counts[status]++
		}
		brief.NodeCount = len(nodes)
		if len(counts) > 0 {
			brief.NodeStatusCounts = counts
		}
	}
	return brief
}

// ListAnalysisNodesByProject 返回指定项目下的分析节点（按创建 id 倒序）。
//
// 过滤语义：
//   - 显式给出 script_id 时始终按脚本返回（跨分析运行），不再退化为关键字搜索；
//   - 否则先用 analysis_id / workflow_id / status / keyword 精确筛选，命中 0 条且给了
//     keyword 时，再退化为「按名字搜索」（命中节点名称 / 节点 ID / 脚本名称），
//     这样模型既能把项目名当过滤条件，也能把用户的一句话直接当关键字。
//
// 节点记录里没有工作流主键，因此 workflow_id 过滤先解析成该工作流下的分析运行集合；
// 集合为空时直接返回空结果（不去全表扫描）。
func (b *projectResourceBrowser) ListAnalysisNodesByProject(ctx context.Context, projectID int64, filters runtimecontext.AnalysisNodeListFilters) ([]runtimecontext.AnalysisNodeSummary, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("list_project_analysis_node: project_id is required")
	}
	if b.analysisRepo == nil {
		return nil, fmt.Errorf("list_project_analysis_node: required services are not configured")
	}

	limit := normalizeBrowserLimit(filters.Limit)
	scriptID, err := parseOptionalID(filters.ScriptID)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_node: invalid script_id: %w", err)
	}
	analysisID, err := parseOptionalID(filters.AnalysisID)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_node: invalid analysis_id: %w", err)
	}
	workflowID, err := parseOptionalID(filters.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_node: invalid workflow_id: %w", err)
	}

	keyword := strings.TrimSpace(filters.Keyword)
	status := strings.TrimSpace(filters.Status)

	// 1) 显式按脚本查：跨分析运行，不再套其它分析运行维度。
	if scriptID > 0 && analysisID == 0 && workflowID == 0 {
		nodes, err := b.analysisRepo.ListAnalysisNodesByProjectIDAndScriptID(ctx, projectID, scriptID)
		if err != nil {
			return nil, fmt.Errorf("list_project_analysis_node: failed to list nodes by script: %w", err)
		}
		nodes = filterNodeList(nodes, keyword, status)
		return b.analysisNodeSummaries(ctx, projectID, nodes, limit), nil
	}

	// 2) 按工作流查：先解析成该工作流下的分析运行集合。
	if workflowID > 0 {
		analyses, err := b.analysisRepo.ListAnalysisByWorkflowID(ctx, workflowID)
		if err != nil {
			return nil, fmt.Errorf("list_project_analysis_node: failed to list analyses by workflow: %w", err)
		}
		nodes := make([]*types.AnalysisNode, 0, len(analyses))
		for _, analysis := range analyses {
			if analysis == nil || analysis.ProjectID != projectID {
				continue
			}
			if analysisID > 0 && analysis.ID != analysisID {
				continue
			}
			children, listErr := b.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
			if listErr != nil {
				return nil, fmt.Errorf("list_project_analysis_node: failed to list nodes of analysis %d: %w", analysis.ID, listErr)
			}
			nodes = append(nodes, children...)
		}
		nodes = filterNodeList(nodes, keyword, status)
		return b.analysisNodeSummaries(ctx, projectID, nodes, limit), nil
	}

	// 3) 按分析运行（或全项目）查；关键字未命中时退化为按名字搜索。
	pagination := &types.Pagination{Page: 1, PageSize: limit}
	nodes, _, err := b.analysisRepo.PageAnalysisNodesByProjectID(ctx, pagination, projectID, scriptID, analysisID)
	if err != nil {
		return nil, fmt.Errorf("list_project_analysis_node: failed to list nodes: %w", err)
	}
	nodes = filterNodeList(nodes, keyword, status)
	if len(nodes) == 0 && keyword != "" {
		fuzzy := &types.Pagination{Page: 1, PageSize: limit, Keyword: keyword}
		nodes, _, err = b.analysisRepo.PageAnalysisNodesByProjectID(ctx, fuzzy, projectID, scriptID, analysisID)
		if err != nil {
			return nil, fmt.Errorf("list_project_analysis_node: failed to search nodes: %w", err)
		}
		nodes = filterNodeStatus(nodes, status)
	}
	return b.analysisNodeSummaries(ctx, projectID, nodes, limit), nil
}

// analysisNodeSummaries 把节点记录转成列表摘要，并补上所属分析运行的执行事实。
//
// 每个分析运行只查一次（用 map 缓存），避免 N 个节点重复查同一行 nextflow；
// 分析行缺失时只保留节点自身信息，不视为错误。
func (b *projectResourceBrowser) analysisNodeSummaries(ctx context.Context, projectID int64, nodes []*types.AnalysisNode, limit int) []runtimecontext.AnalysisNodeSummary {
	out := make([]runtimecontext.AnalysisNodeSummary, 0, minInt(len(nodes), limit))
	analysisCache := make(map[int64]*types.Analysis)
	for _, node := range nodes {
		if node == nil || node.ProjectID != projectID {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, b.analysisNodeSummary(ctx, analysisCache, node))
	}
	return out
}

// analysisNodeSummary 组装单个节点的列表摘要（节点自身 + 所属分析运行）。
func (b *projectResourceBrowser) analysisNodeSummary(ctx context.Context, cache map[int64]*types.Analysis, node *types.AnalysisNode) runtimecontext.AnalysisNodeSummary {
	// 兜底：个别读取路径可能没有填充派生路径，这里按 WorkspaceDir 再推导一次（幂等）。
	if (node.ParamsPath == "" || node.OutputDir == "" || node.CommandPath == "") && node.WorkspaceDir != "" {
		node.HydrateDerivedPaths()
	}

	brief := runtimecontext.AnalysisNodeBrief{
		AnalysisNodeID:      node.ID,
		NodeName:            node.NodeName,
		CreationSource:      node.CreationSource,
		NodeType:            node.NodeType(),
		NodeTypeDescription: node.NodeTypeDescription(),
		ScriptID:            node.ScriptID,
		WorkspaceDir:        node.WorkspaceDir,
		CommandPath:         node.CommandPath,
		ParamsPath:          node.ParamsPath,
		OutputDir:           node.OutputDir,
		RequestParam:        agentanalysis.DecodeRequestParam(node.RequestParam),
	}

	summary := runtimecontext.AnalysisNodeSummary{
		AnalysisNodeBrief: brief,
		AnalysisNodeKey:   node.AnalysisNodeID,
		NodeID:            node.NodeID,
		SampleID:          node.SampleID,
		ScriptName:        node.ScriptName,
		AnalysisRunID:     node.AnalysisID,
		Status:            node.Status,
		ExitCode:          node.ExitCode,
		StartedAt:         formatTimePtr(node.StartedAt),
		FinishedAt:        formatTimePtr(node.FinishedAt),
	}

	if node.AnalysisID <= 0 {
		return summary
	}
	analysis, ok := cache[node.AnalysisID]
	if !ok {
		if b.analysisRepo != nil {
			if loaded, err := b.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID); err == nil {
				analysis = loaded
			}
		}
		cache[node.AnalysisID] = analysis
	}
	if analysis != nil {
		summary.AnalysisKey = analysis.AnalysisID
		summary.AnalysisName = analysis.AnalysisName
		summary.JobStatus = analysis.JobStatus
		summary.SchedulerMode = types.NormalizeSchedulerMode(analysis.SchedulerMode)
	}
	return summary
}

// analysisQueryFromFilters 把工具层过滤条件转成仓储查询（ID 字符串 → int64）。
func analysisQueryFromFilters(workflowIDRaw, jobStatus string, cacheType int, isReport *bool) (*types.AnalysisQuey, error) {
	query := &types.AnalysisQuey{}
	if workflowID, err := parseOptionalID(workflowIDRaw); err != nil {
		return nil, fmt.Errorf("invalid workflow_id: %w", err)
	} else if workflowID > 0 {
		query.WorkflowID = workflowID
	}
	if status := strings.TrimSpace(jobStatus); status != "" {
		query.JobStatus = status
	}
	if cacheType > 0 {
		value := cacheType
		query.CacheType = &value
	}
	if isReport != nil {
		value := *isReport
		query.IsReport = &value
	}
	return query, nil
}

// filterNodeList 按关键字（节点名称 / 节点 ID / analysis_node_id / 脚本名称）与状态过滤节点。
//
// 关键字是「且」语义：调用方通常已经用同一个词做过一次数据库筛选，这里只负责收敛
// 内存结果（如按脚本 / 按工作流取回的分支）。
func filterNodeList(nodes []*types.AnalysisNode, keyword, status string) []*types.AnalysisNode {
	out := make([]*types.AnalysisNode, 0, len(nodes))
	lowerKeyword := strings.ToLower(keyword)
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if status != "" && !strings.EqualFold(strings.TrimSpace(node.Status), status) {
			continue
		}
		if lowerKeyword != "" && !nodeMatchesKeyword(node, lowerKeyword) {
			continue
		}
		out = append(out, node)
	}
	return out
}

// filterNodeStatus 只按状态过滤节点（关键字已由数据库模糊搜索命中）。
func filterNodeStatus(nodes []*types.AnalysisNode, status string) []*types.AnalysisNode {
	if status == "" {
		return nodes
	}
	out := make([]*types.AnalysisNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(node.Status), status) {
			out = append(out, node)
		}
	}
	return out
}

// nodeMatchesKeyword 报告节点是否命中关键字（全部小写比较）。
func nodeMatchesKeyword(node *types.AnalysisNode, lowerKeyword string) bool {
	for _, field := range []string{node.NodeName, node.NodeID, node.AnalysisNodeID, node.ScriptName} {
		if field != "" && strings.Contains(strings.ToLower(field), lowerKeyword) {
			return true
		}
	}
	return false
}

// parseOptionalID 解析可选 ID 字符串：空串返回 0（表示不过滤）。
func parseOptionalID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, nil
	}
	if idx := strings.LastIndex(idStr, ":"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+1:])
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return id, nil
}

// normalizeBrowserLimit 归一化列表工具返回条数：<=0 取默认值，超过上限时截断。
func normalizeBrowserLimit(limit int) int {
	if limit <= 0 {
		return defaultAnalysisListLimit
	}
	if limit > maxAnalysisListLimit {
		return maxAnalysisListLimit
	}
	return limit
}

// formatTimePtr 把可选时间格式化成 RFC3339（nil 返回空串，便于 JSON 省略）。
func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// minInt 返回两个整数中的较小值。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
