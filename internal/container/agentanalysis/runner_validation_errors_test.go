package agentanalysis

import (
	"context"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// stubAnalysisService 只实现 runner 等待终态路径用到的读节点方法；其余方法走内嵌接口，
// 一旦被测代码越界调用会 panic，正好说明测试假设不成立。
type stubAnalysisService struct {
	interfaces.AnalysisService
	node     *types.AnalysisNode
	getCalls int
}

func (s *stubAnalysisService) GetAnalysisNodeByID(_ context.Context, _ int64) (*types.AnalysisNode, error) {
	s.getCalls++
	return s.node, nil
}

// 终态读取必须回传「运行结束后」的校验错误快照：失败节点由编排器在运行期写入的错误
// 要如实返回，供模型判断本次运行确实缺产物。
func TestWaitForCompletionReportsPostRunValidationErrors(t *testing.T) {
	svc := &stubAnalysisService{node: &types.AnalysisNode{
		ID:           7,
		AnalysisID:   1,
		Status:       "failed",
		ErrorMessage: "output validation failed: missing output: kegg_enrichment_tsv",
		OutputValidationErrors: types.JSONSlice{
			"missing output: kegg_enrichment_tsv",
		},
	}}
	runner := &analysisNodeRunner{
		analysisSvc:  svc,
		pollInterval: time.Millisecond,
		maxLogBytes:  1024,
	}

	result, err := runner.waitForCompletion(context.Background(), &types.AnalysisNode{ID: 7, AnalysisID: 1}, 2*time.Second)
	if err == nil {
		t.Fatalf("expected a failing node to return an error")
	}
	if result.Success {
		t.Fatalf("expected failed status to be reported as unsuccessful")
	}
	if len(result.OutputValidationErrors) != 1 {
		t.Fatalf("expected the post-run validation error to be returned, got %#v", result.OutputValidationErrors)
	}
}

// 成功终态的 output_validation_errors 一定是空（运行前清空、编排器成功分支写成空），
// 因此结果里不能出现任何缺失产物；返回非 nil 空切片让 JSON omitempty 直接省略该字段，
// 也避免 RunAnalysisNode 再为 nil 重读一次节点。
func TestWaitForCompletionSuccessHasNoValidationErrors(t *testing.T) {
	svc := &stubAnalysisService{node: &types.AnalysisNode{
		ID:         8,
		AnalysisID: 1,
		Status:     "done",
		// 运行前的旧记录在这里无关紧要：waitForCompletion 用的是终态读取结果，不会回退到
		// 调用方传入节点的校验错误。
		OutputValidationErrors: types.JSONSlice{},
	}}
	runner := &analysisNodeRunner{
		analysisSvc:  svc,
		pollInterval: time.Millisecond,
		maxLogBytes:  1024,
	}

	result, err := runner.waitForCompletion(context.Background(), &types.AnalysisNode{ID: 8, AnalysisID: 1}, 2*time.Second)
	if err != nil {
		t.Fatalf("expected a succeeded node to return no error, got %v", err)
	}
	if !result.Success {
		t.Fatalf("expected status done to be reported as successful")
	}
	if len(result.OutputValidationErrors) != 0 {
		t.Fatalf("expected no output validation errors on success, got %#v", result.OutputValidationErrors)
	}
	if result.OutputValidationErrors == nil {
		t.Fatalf("expected a non-nil empty snapshot so the JSON field is omitted")
	}
}

// fillValidationErrors 已删除，这里改用直接可观测的事实：waitForCompletion 不再在
// 等待前读一次节点。上下文立即取消时应该一次库都不读，第一次读库发生在被事件或兜底
// 轮询唤醒之后（即「运行有进展后再查」）。
func TestWaitForCompletionDoesNotReadNodeBeforeFirstWake(t *testing.T) {
	svc := &stubAnalysisService{}
	runner := &analysisNodeRunner{analysisSvc: svc, pollInterval: time.Hour, maxLogBytes: 128}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runner.waitForCompletion(ctx, &types.AnalysisNode{ID: 11, AnalysisID: 1}, time.Second)
	if err == nil {
		t.Fatalf("expected a canceled context to return an error")
	}
	if result.Status != "canceled" {
		t.Fatalf("expected canceled status, got %q", result.Status)
	}
	if svc.getCalls != 0 {
		t.Fatalf("expected no node read before the first wake, got %d", svc.getCalls)
	}
}
