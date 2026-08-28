package manager

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// AISummaryContent 是生成摘要所需的原始内容。
type AISummaryContent struct {
	// Title 是摘要标题的候选值。
	Title string
	// Text 是交给 Agent 生成摘要的原始内容。
	Text string

	WorkingDir string
}

// AISummaryContentProvider 根据摘要所属对象解析用于生成摘要的原始内容。
// 当前基于 Analysis / AnalysisNode 的字段组装文本；后续可扩展为读取
// 实际 output 文件内容。
type AISummaryContentProvider interface {
	Resolve(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (AISummaryContent, error)
}

type aiSummaryContentProvider struct {
	analysisRepo interfaces.AnalysisRepository
}

// NewAISummaryContentProvider 创建 AISummaryContentProvider。
func NewAISummaryContentProvider(analysisRepo interfaces.AnalysisRepository) AISummaryContentProvider {
	return &aiSummaryContentProvider{analysisRepo: analysisRepo}
}

// Resolve 按所属对象类型分发解析逻辑。
func (p *aiSummaryContentProvider) Resolve(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (AISummaryContent, error) {
	switch ownerType {
	case types.SummaryOwnerAnalysis:
		return p.resolveAnalysis(ctx, ownerID)
	case types.SummaryOwnerAnalysisNode:
		return p.resolveAnalysisNode(ctx, ownerID)
	default:
		return AISummaryContent{}, fmt.Errorf("unsupported summary owner type: %s", ownerType)
	}
}

func (p *aiSummaryContentProvider) resolveAnalysis(ctx context.Context, analysisID int64) (AISummaryContent, error) {
	a, err := p.analysisRepo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return AISummaryContent{}, err
	}

	return AISummaryContent{
		Title:      fmt.Sprintf("分析摘要：%s", a.AnalysisName),
		WorkingDir: a.OutputDir,
		Text: strings.Join(filterNonEmpty([]string{
			"分析名称: " + a.AnalysisName,
			"分析方法: " + a.AnalysisMethod,
			"运行状态: " + a.JobStatus,
			"输出目录: " + a.OutputDir,
			"输出格式: " + a.OutputFormat,
		}), "\n"),
	}, nil
}

func (p *aiSummaryContentProvider) resolveAnalysisNode(ctx context.Context, nodeID int64) (AISummaryContent, error) {
	n, err := p.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		return AISummaryContent{}, err
	}

	return AISummaryContent{
		Title:      fmt.Sprintf("节点摘要：%s", n.NodeName),
		WorkingDir: n.OutputDir,
		Text: strings.Join(filterNonEmpty([]string{
			"节点名称: " + n.NodeName,
			"节点 ID: " + n.NodeID,
			"样本 ID: " + n.SampleID,
			"状态: " + n.Status,
			"输出目录: " + n.OutputDir,
		}), "\n"),
	}, nil
}

func filterNonEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
