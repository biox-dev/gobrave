package container

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// projectReportWriter 实现 runtimecontext.ProjectReportWriter：创建 / 更新项目报告，
// 报告正文落在报告工作目录下的 output.md（utils.GetProjectReportOutputFile）。
//
// 与 skill.Writer 同构：Plan* 只读现状并计算 before / after（不写库、不落盘），
// 供 create_report / update_report 在授权前展示 unified diff；Apply 在授权通过后
// 先用 ProjectService 写库，再覆写 output.md（建目录 / 初始化 git 仓库由 service 负责）。
type projectReportWriter struct {
	projectSvc interfaces.ProjectService
	// resources 记录「当前会话 ↔ 项目报告」的关联（创建 / 更新成功后写入）。
	resources agentinterfaces.SessionResourceService
	cfg       *config.Config
}

// newProjectReportWriter 构建项目报告写能力（create_report / update_report）。
func newProjectReportWriter(
	projectSvc interfaces.ProjectService,
	resources agentinterfaces.SessionResourceService,
	cfg *config.Config,
) *projectReportWriter {
	return &projectReportWriter{projectSvc: projectSvc, resources: resources, cfg: cfg}
}

// PlanCreate 实现 runtimecontext.ProjectReportWriter：校验入参并计算新建报告的写入计划。
func (w *projectReportWriter) PlanCreate(_ context.Context, in runtimecontext.ProjectReportCreateInput) (runtimecontext.ProjectReportWritePlan, error) {
	if err := w.available(); err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}
	if in.UserID <= 0 {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("user_id is required")
	}
	if in.ProjectID <= 0 {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("project_id is required")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("title is required")
	}

	// 报告 ID 在计划阶段就分配：diff 预览需要真实的目标文件路径，Apply 沿用同一个 ID。
	report := &types.ProjectReport{ID: utils.GenerateID(), ProjectID: in.ProjectID}
	file, err := w.outputFile(report)
	if err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}

	content := in.Content
	if strings.TrimSpace(content) == "" {
		content = defaultReportContent(title)
	}
	return runtimecontext.ProjectReportWritePlan{
		UserID:    in.UserID,
		ReportID:  report.ID,
		ProjectID: report.ProjectID,
		Title:     title,
		FilePath:  file,
		After:     content,
		Create:    true,
	}, nil
}

// PlanEdit 实现 runtimecontext.ProjectReportWriter：按 old_string → new_string 计算
// 一次唯一文本替换的写入计划（不落盘）。
//
// 语义与 coder 的 edit 工具一致：old_string 必须在 output.md 中唯一出现，替换结果
// 必须真实改变内容。
func (w *projectReportWriter) PlanEdit(ctx context.Context, in runtimecontext.ProjectReportEditInput) (runtimecontext.ProjectReportWritePlan, error) {
	if err := w.available(); err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}
	if in.OldString == "" {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("old_string is required")
	}

	report, before, err := w.load(ctx, in.UserID, in.ReportID)
	if err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}

	index := strings.Index(before, in.OldString)
	if index == -1 {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("old_string not found in report file. Make sure it matches exactly, including whitespace and line breaks")
	}
	if last := strings.LastIndex(before, in.OldString); last != index {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("old_string appears multiple times in the report file. Please provide more context to ensure a unique match")
	}
	after := before[:index] + in.NewString + before[index+len(in.OldString):]
	if after == before {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("new content is the same as old content. No changes made")
	}

	file, err := w.outputFile(report)
	if err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}
	return runtimecontext.ProjectReportWritePlan{
		UserID:    in.UserID,
		ReportID:  report.ID,
		ProjectID: report.ProjectID,
		Title:     report.Title,
		FilePath:  file,
		Before:    before,
		After:     after,
	}, nil
}

// PlanUpdate 实现 runtimecontext.ProjectReportWriter：按结构化字段（title / content）
// 计算写入计划（不落盘）。空串字段表示保持不变。
func (w *projectReportWriter) PlanUpdate(ctx context.Context, in runtimecontext.ProjectReportUpdateInput) (runtimecontext.ProjectReportWritePlan, error) {
	if err := w.available(); err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}
	title := strings.TrimSpace(in.Title)
	content := in.Content
	if title == "" && strings.TrimSpace(content) == "" {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("title or content is required")
	}

	report, before, err := w.load(ctx, in.UserID, in.ReportID)
	if err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}

	after := before
	if strings.TrimSpace(content) != "" {
		after = content
	}
	newTitle := report.Title
	if title != "" {
		newTitle = title
	}
	if after == before && newTitle == report.Title {
		return runtimecontext.ProjectReportWritePlan{}, errors.New("new content is the same as old content. No changes made")
	}

	file, err := w.outputFile(report)
	if err != nil {
		return runtimecontext.ProjectReportWritePlan{}, err
	}
	return runtimecontext.ProjectReportWritePlan{
		UserID:    in.UserID,
		ReportID:  report.ID,
		ProjectID: report.ProjectID,
		Title:     newTitle,
		FilePath:  file,
		Before:    before,
		After:     after,
	}, nil
}

// Apply 实现 runtimecontext.ProjectReportWriter：按计划写库并落盘 output.md。
func (w *projectReportWriter) Apply(ctx context.Context, plan runtimecontext.ProjectReportWritePlan) (runtimecontext.ProjectReportWriteInfo, error) {
	if err := w.available(); err != nil {
		return runtimecontext.ProjectReportWriteInfo{}, err
	}
	if strings.TrimSpace(plan.After) == "" {
		return runtimecontext.ProjectReportWriteInfo{}, errors.New("report content is empty")
	}

	if plan.Create {
		report := &types.ProjectReport{ID: plan.ReportID, ProjectID: plan.ProjectID, Title: plan.Title}
		if err := w.projectSvc.AddProjectReport(ctx, plan.UserID, report); err != nil {
			return runtimecontext.ProjectReportWriteInfo{}, fmt.Errorf("failed to create project report: %w", err)
		}
		if err := w.projectSvc.UpdateProjectReportFile(ctx, plan.UserID, report.ID, plan.After); err != nil {
			return runtimecontext.ProjectReportWriteInfo{}, fmt.Errorf("failed to write report file: %w", err)
		}
		w.link(ctx, report.ID)
		return writeInfo(report.ID, report.ProjectID, report.Title, plan.FilePath), nil
	}

	// 预览后文件若被改动则拒绝，避免覆盖他人修改（与 skill.Writer.Apply 同口径）。
	current, err := w.projectSvc.GetProjectReportFile(ctx, plan.UserID, plan.ReportID)
	if err != nil {
		return runtimecontext.ProjectReportWriteInfo{}, fmt.Errorf("failed to read report file: %w", err)
	}
	if current != plan.Before {
		return runtimecontext.ProjectReportWriteInfo{}, errors.New("report file changed since the diff was prepared; please retry")
	}

	if err := w.projectSvc.UpdateProjectReport(ctx, plan.UserID, &types.ProjectReport{
		ID:        plan.ReportID,
		ProjectID: plan.ProjectID,
		Title:     plan.Title,
	}); err != nil {
		return runtimecontext.ProjectReportWriteInfo{}, fmt.Errorf("failed to update project report: %w", err)
	}
	if plan.After != plan.Before {
		if err := w.projectSvc.UpdateProjectReportFile(ctx, plan.UserID, plan.ReportID, plan.After); err != nil {
			return runtimecontext.ProjectReportWriteInfo{}, fmt.Errorf("failed to write report file: %w", err)
		}
	}
	w.link(ctx, plan.ReportID)
	return writeInfo(plan.ReportID, plan.ProjectID, plan.Title, plan.FilePath), nil
}

// link 记录「当前工具执行会话 ↔ 项目报告」的关联（供会话结果面板展示）。
//
// 与其它资源工具一致：不是 Agent 会话触发（ctx 无会话 ID）时静默跳过，关联失败也不影响写入结果。
func (w *projectReportWriter) link(ctx context.Context, reportID int64) {
	if w.resources == nil || reportID <= 0 {
		return
	}
	_ = w.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeProjectReport, reportID)
}

// load 读取报告元信息与当前正文（输出文件不存在时正文为空串）。
func (w *projectReportWriter) load(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, string, error) {
	if userID <= 0 {
		return nil, "", errors.New("user_id is required")
	}
	if reportID <= 0 {
		return nil, "", errors.New("report_id is required")
	}

	// GetProjectReportDetailByID 会校验报告所属项目已绑定当前用户（越权时返回 not found）。
	report, err := w.projectSvc.GetProjectReportDetailByID(ctx, userID, reportID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load project report: %w", err)
	}
	content, err := w.projectSvc.GetProjectReportFile(ctx, userID, reportID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read report file: %w", err)
	}
	return report, content, nil
}

// outputFile 返回报告正文文件的绝对路径（由 ProjectReport.OutputFile 统一推导）。
func (w *projectReportWriter) outputFile(report *types.ProjectReport) (string, error) {
	if w.cfg == nil || w.cfg.Storage == nil {
		return "", errors.New("storage config is missing")
	}
	baseDir := strings.TrimSpace(w.cfg.Storage.BaseDir)
	if baseDir == "" {
		return "", errors.New("storage base dir is empty")
	}
	return report.OutputFile(baseDir), nil
}

// available 校验写能力是否已装配。
func (w *projectReportWriter) available() error {
	if w == nil || w.projectSvc == nil {
		return errors.New("project report service is not configured")
	}
	return nil
}

// defaultReportContent 是未提供正文时的初始 report 内容（与 service 建目录时的口径一致）。
func defaultReportContent(title string) string {
	return fmt.Sprintf("# %s\n", title)
}

// writeInfo 组装写入结果。
func writeInfo(reportID, projectID int64, title, file string) runtimecontext.ProjectReportWriteInfo {
	return runtimecontext.ProjectReportWriteInfo{
		ReportID:  reportID,
		ProjectID: projectID,
		Title:     title,
		FilePath:  file,
	}
}
