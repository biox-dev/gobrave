package prepare

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/flosch/pongo2/v6"
)

// RunScriptBuilder 是「运行脚本构建器」接口：根据脚本类型（r / python / shell /
// qmd / jupyter）生成节点实际执行的 run.sh 内容。
type RunScriptBuilder interface {
	// Name 返回构建器唯一标识（小写脚本类型，如 r / python / shell / qmd / jupyter）。
	Name() string
	// Build 根据节点、脚本路径、脚本内容与参数生成 run.sh 内容。
	Build(node *types.AnalysisNode, scriptPath string, scriptContent string, params map[string]any) (string, error)
}

// RunScriptBuilderRegistry 负责注册与解析 RunScriptBuilder。
// 采用与 agent.Registry 一致的注册表模式，由容器启动时注册内置构建器，
// 运行时按脚本类型解析；未匹配到对应类型时回退到 "shell"。
type RunScriptBuilderRegistry struct {
	mu       sync.RWMutex
	builders map[string]RunScriptBuilder
}

// NewRunScriptBuilderRegistry 创建注册表并注册传入的构建器。
func NewRunScriptBuilderRegistry(builders ...RunScriptBuilder) *RunScriptBuilderRegistry {
	r := &RunScriptBuilderRegistry{builders: make(map[string]RunScriptBuilder)}
	for _, b := range builders {
		r.Register(b)
	}
	return r
}

// Register 注册构建器；同名覆盖。
func (r *RunScriptBuilderRegistry) Register(b RunScriptBuilder) {
	if b == nil {
		return
	}
	name := normalizeScriptType(b.Name())
	r.mu.Lock()
	r.builders[name] = b
	r.mu.Unlock()
}

// Resolve 按脚本类型解析构建器；未匹配到对应类型时回退到 "shell"。
// 返回 nil 表示 "shell" 也未注册。
func (r *RunScriptBuilderRegistry) Resolve(scriptType string) RunScriptBuilder {
	name := normalizeScriptType(scriptType)
	r.mu.RLock()
	b, ok := r.builders[name]
	r.mu.RUnlock()
	if ok {
		return b
	}
	r.mu.RLock()
	b = r.builders["shell"]
	r.mu.RUnlock()
	return b
}

// Has 报告指定脚本类型是否已注册。
func (r *RunScriptBuilderRegistry) Has(scriptType string) bool {
	name := normalizeScriptType(scriptType)
	r.mu.RLock()
	_, ok := r.builders[name]
	r.mu.RUnlock()
	return ok
}

// Names 返回已注册的构建器名称列表（升序，便于调试与校验）。
func (r *RunScriptBuilderRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.builders))
	for name := range r.builders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultRunScriptBuilders 返回框架内置的 RunScriptBuilder 列表。
func DefaultRunScriptBuilders() []RunScriptBuilder {
	return []RunScriptBuilder{
		RScriptBuilder{},
		PythonScriptBuilder{},
		ShellScriptBuilder{},
		QmdScriptBuilder{},
		JupyterScriptBuilder{},
	}
}

type RScriptBuilder struct{}

func (RScriptBuilder) Name() string { return "r" }

func (RScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	return fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nRscript %q %q %q\n", scriptPath, node.ParamsPath, node.OutputDir), nil
}

type JupyterScriptBuilder struct{}

func (JupyterScriptBuilder) Name() string { return "jupyter" }

func (JupyterScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	outputFileName := "output.md"
	return fmt.Sprintf(`#!/usr/bin/env bash
export HOME=$PWD/.home
export XDG_CACHE_HOME=$HOME/.cache
export TMPDIR=$PWD/.tmp
mkdir -p "$TMPDIR"

set -euo pipefail
jupyter nbconvert --to markdown --execute %q --output-dir %q  --output %q
`, scriptPath, node.OutputDir, outputFileName), nil
}

type QmdScriptBuilder struct{}

func (QmdScriptBuilder) Name() string { return "qmd" }

func (QmdScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	// quarto preview chapter_5.qmd --to md --no-watch-inputs --no-browse
	// 判断node.WorkspaceDir 是否存在 main.qmd文件，不存在创建 链接 ln -s scriptPath main.qmd
	mainFile := filepath.Join(node.WorkspaceDir, "main.qmd")
	if _, err := os.Stat(mainFile); os.IsNotExist(err) {
		if err := os.Symlink(scriptPath, mainFile); err != nil {
			return "", fmt.Errorf("failed to create symlink for main.qmd: %w", err)
		}
	}

	outputFileName := "output.md"
	outputFile := filepath.Join(node.OutputDir, outputFileName)
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export HOME=$PWD/.home
export XDG_CACHE_HOME=$HOME/.cache
export TMPDIR=$PWD/.tmp
mkdir -p "$TMPDIR"

quarto render main.qmd --to md --output-dir %q --execute-dir %q --output - > output.md
mv output.md %q
`, node.OutputDir, node.WorkspaceDir, outputFile), nil

}

type PythonScriptBuilder struct{}

func (PythonScriptBuilder) Name() string { return "python" }

func (PythonScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	return fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\npython %q %q %q\n", scriptPath, node.ParamsPath, node.OutputDir), nil
}

type ShellScriptBuilder struct{}

func (ShellScriptBuilder) Name() string { return "shell" }

func (ShellScriptBuilder) Build(_ *types.AnalysisNode, scriptPath string, scriptContent string, params map[string]any) (string, error) {
	rendered, err := renderShellTemplate(scriptContent, params)
	if err != nil {
		return "", err
	}
	return rendered + "\n\n#" + scriptPath + "\n", nil
}

func renderShellTemplate(content string, params map[string]any) (string, error) {
	tpl, err := pongo2.FromString(content)
	if err != nil {
		return "", fmt.Errorf("parse shell template failed: %w", err)
	}

	ctx := pongo2.Context{}
	for k, v := range params {
		ctx[k] = v
	}

	if meta, ok := templateAsMap(ctx["meta"]); ok {
		if _, exists := ctx["meta_file_name"]; !exists {
			if fileName, ok := meta["file_name"]; ok {
				ctx["meta_file_name"] = fileName
			}
		}
	}

	rendered, err := tpl.Execute(ctx)
	if err != nil {
		return "", fmt.Errorf("render shell template failed: %w", err)
	}
	return rendered, nil
}

func templateAsMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}
