package prepare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

type FileSystemNodeRuntimePreparer struct {
	analysisRepo    interfaces.AnalysisRepository
	workflowService interfaces.WorkflowService
	workflowRepo    interfaces.WorkflowRepository
	projectRepo     interfaces.ProjectRepository
	cfg             *config.Config
	builders        *RunScriptBuilderRegistry
}

func NewFileSystemNodeRuntimePreparer(
	analysisRepo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	cfg *config.Config,
	builders *RunScriptBuilderRegistry,
) *FileSystemNodeRuntimePreparer {
	return &FileSystemNodeRuntimePreparer{
		analysisRepo:    analysisRepo,
		workflowService: workflowService,
		workflowRepo:    workflowRepo,
		projectRepo:     projectRepo,
		cfg:             cfg,
		builders:        builders,
	}
}

// baseDir 返回存储根目录（源自注入的 Config，而非直接注入字符串）。
func (p *FileSystemNodeRuntimePreparer) baseDir() string {
	if p.cfg == nil || p.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(p.cfg.Storage.BaseDir)
}

// skipCleanOutputContextKey 使用私有空结构体类型作为 context key，避免与其它包冲突。
type skipCleanOutputContextKey struct{}

// WithSkipCleanOutput 返回一个携带“跳过清理输出目录”标识的 context。
// Prepare 检测到该标识后，将不再调用 cleanDirContents。
func WithSkipCleanOutput(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipCleanOutputContextKey{}, true)
}

func shouldSkipCleanOutput(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(skipCleanOutputContextKey{}).(bool)
	return v
}

func (p *FileSystemNodeRuntimePreparer) Prepare(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	if node.IsCommandNode() {
		return p.prepareCommandNode(ctx, node)
	}
	script, err := p.workflowRepo.GetScriptByID(ctx, node.ScriptID)
	if err != nil {
		return fmt.Errorf("load script failed: %w", err)
	}

	// scriptPath := p.resolveScriptPath(script.ScriptID, scriptType)
	project, err := p.projectRepo.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return fmt.Errorf("load project failed: %w", err)
	}
	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
		return err
	}
	// create cached dir
	// nodeCachedDir := filepath.Join(node.WorkspaceDir, "cached")
	if strings.TrimSpace(node.CacheDir) == "" {
		node.CacheDir = utils.GetAnalysisNodeCacheDir(node.WorkspaceDir) //filepath.Join(node.WorkspaceDir, "cached")
	}
	if err := os.MkdirAll(node.CacheDir, 0o755); err != nil {
		return err
	}

	prefix := filepath.Join(p.baseDir(), "data", strconv.FormatInt(project.ID, 10))

	// projectCachedDir := filepath.Join(prefix, "cached")
	// if err := os.MkdirAll(projectCachedDir, 0o755); err != nil {
	// 	return err
	// }
	// 脚本路径统一绝对化，供软链与 run script 构造共用。
	scriptDir, scriptFile, _ := utils.GetScriptFile(p.baseDir(), project.ID, script.ScriptType, script.ScriptID)
	scriptPath := filepath.Join(scriptDir, scriptFile)
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(p.baseDir(), scriptPath)
	}

	// 初始化 paramsPayload：先构建分支专属的基础参数，
	// 再在条件外统一注入所有分支共享的公共参数。
	paramsPayload, err := p.resolveBaseParams(ctx, node)
	if err != nil {
		return err
	}
	paramsPayload["project_dir"] = utils.GetProjectDir(p.baseDir(), project.ID)
	paramsPayload["output_dir"] = node.OutputDir
	paramsPayload["node_cached_dir"] = node.CacheDir
	// paramsPayload["project_cached_dir"] = projectCachedDir

	if err := writeJSONAtomic(node.ParamsPath, paramsPayload, 0o644); err != nil {
		return fmt.Errorf("write params json failed: %w", err)
	}

	// script_dir 仅用于构造 run script，不写入 params.json。
	paramsPayload["script_dir"] = scriptDir

	// 软链脚本：若已存在但目标失效（或指向其它脚本），先删除再重建。
	scriptWorkspacePath := filepath.Join(node.WorkspaceDir, scriptFile)
	if err := ensureSymlink(scriptWorkspacePath, scriptPath); err != nil {
		return fmt.Errorf("symlink script %s -> %s failed: %w", scriptWorkspacePath, scriptPath, err)
	}

	// synlink io_schema.json：同样在目标失效或指向错误时重建。
	ioSchemaPath := filepath.Join(scriptDir, "io_schema.json")
	scriptWorkspaceIoSchemaPath := filepath.Join(node.WorkspaceDir, "io_schema.json")
	if _, err := os.Lstat(ioSchemaPath); err == nil {
		if err := ensureSymlink(scriptWorkspaceIoSchemaPath, ioSchemaPath); err != nil {
			return fmt.Errorf("symlink io_schema %s -> %s failed: %w", scriptWorkspaceIoSchemaPath, ioSchemaPath, err)
		}
	}
	// 在 node.WorkspaceDir/env.sh 中写入
	//  export PATH={scriptDir}:$PATH
	envFilePath := utils.GetAnalysisNodeEnvFile(node.WorkspaceDir)
	if err := p.WriteEnvFile(ctx, envFilePath, node, scriptDir); err != nil {
		return fmt.Errorf("write env.sh failed: %w", err)
	}
	if err := p.WriteCommand(node, script.ScriptType, scriptPath, scriptWorkspacePath, paramsPayload); err != nil {
		return fmt.Errorf("write command failed: %w", err)
	}

	if !shouldSkipCleanOutput(ctx) {
		if err := cleanDirContents(node.OutputDir, prefix); err != nil {
			return fmt.Errorf("clean output dir failed: %w", err)
		}
	}

	return nil
}

// prepareCommandNode 为 Agent 命令节点准备运行期文件。
//
// 命令节点没有脚本 / io_schema：params.json 只带公共路径参数，run.sh 直接由
// node.Command 生成，因此不需要脚本软链与 RunScriptBuilder。
func (p *FileSystemNodeRuntimePreparer) prepareCommandNode(ctx context.Context, node *types.AnalysisNode) error {
	project, err := p.projectRepo.GetProjectByID(ctx, node.ProjectID)
	if err != nil {
		return fmt.Errorf("load project failed: %w", err)
	}
	if err := os.MkdirAll(node.WorkspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir failed: %w", err)
	}
	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir failed: %w", err)
	}
	if err := os.MkdirAll(node.CacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir failed: %w", err)
	}

	paramsPayload := cloneAnyMapForNode(map[string]interface{}(node.ResolvedInputs))
	paramsPayload["project_dir"] = utils.GetProjectDir(p.baseDir(), project.ID)
	paramsPayload["output_dir"] = node.OutputDir
	paramsPayload["node_cached_dir"] = node.CacheDir
	if err := writeJSONAtomic(node.ParamsPath, paramsPayload, 0o644); err != nil {
		return fmt.Errorf("write params json failed: %w", err)
	}

	projectDir := utils.GetProjectDir(p.baseDir(), project.ID)
	if err := p.WriteEnvFile(ctx, utils.GetAnalysisNodeEnvFile(node.WorkspaceDir), node, projectDir); err != nil {
		return fmt.Errorf("write env.sh failed: %w", err)
	}

	command := strings.TrimSpace(node.Command)
	if command == "" {
		return fmt.Errorf("command is required for command node %d", node.ID)
	}
	if !strings.HasSuffix(command, "\n") {
		command += "\n"
	}
	if err := writeTextAtomic(node.CommandPath, command, 0o755); err != nil {
		return fmt.Errorf("write run.sh failed: %w", err)
	}

	if !shouldSkipCleanOutput(ctx) {
		prefix := filepath.Join(p.baseDir(), "data", strconv.FormatInt(project.ID, 10))
		if err := cleanDirContents(node.OutputDir, prefix); err != nil {
			return fmt.Errorf("clean output dir failed: %w", err)
		}
	}
	return nil
}

func (p *FileSystemNodeRuntimePreparer) WriteEnvFile(ctx context.Context, envFilePath string, node *types.AnalysisNode, scriptDir string) error {
	content := fmt.Sprintf("export PATH=%s:$PATH\n", scriptDir)
	content += `
	
# BRAVE environment
if [ -f /etc/profile.d/brave-env.sh ]; then
    source /etc/profile.d/brave-env.sh
fi
`

	// 如果 scriptDir/bin 目录存在，则将其加入 PATH
	binDir := filepath.Join(scriptDir, "bin")
	if stat, err := os.Stat(binDir); err == nil && stat.IsDir() {
		content += fmt.Sprintf("export PATH=%s:$PATH\n", binDir)
	}

	// 如果 scriptDir/lib 目录存在，则将其加入 LD_LIBRARY_PATH
	libDir := filepath.Join(scriptDir, "lib")
	if stat, err := os.Stat(libDir); err == nil && stat.IsDir() {
		content += fmt.Sprintf("export LD_LIBRARY_PATH=%s:$LD_LIBRARY_PATH\n", libDir)
	}
	if err := writeTextAtomic(envFilePath, content, 0o644); err != nil {
		return fmt.Errorf("write env.sh failed: %w", err)
	}
	return nil
}

// resolveBaseParams 构建节点的基础参数（不含公共参数）：
//   - 独立节点（AnalysisID == 0）：使用节点自带的 ResolvedInputs；
//   - 分析节点：合并分析级参数与节点已解析的输入参数。
func (p *FileSystemNodeRuntimePreparer) resolveBaseParams(ctx context.Context, node *types.AnalysisNode) (map[string]any, error) {
	if node.AnalysisID == 0 {
		return cloneAnyMapForNode(map[string]interface{}(node.ResolvedInputs)), nil
	}

	if strings.TrimSpace(node.AnalysisNodeID) == "" {
		return nil, fmt.Errorf("analysis_node_id is required")
	}
	if node.ScriptID == 0 {
		return nil, fmt.Errorf("script_id is required")
	}

	analysis, err := p.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID)
	if err != nil {
		return nil, fmt.Errorf("load analysis failed: %w", err)
	}

	if err := p.ensureNodePaths(node, analysis); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(node.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir failed: %w", err)
	}
	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir failed: %w", err)
	}

	return p.buildNodeParams(ctx, node, analysis)
}

func (p *FileSystemNodeRuntimePreparer) WriteCommand(node *types.AnalysisNode, scriptType, scriptPath, scriptWorkspacePath string, params map[string]any) error {

	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("read script file failed: %w", err)
	}
	builder := p.builders.Resolve(scriptType)
	if builder == nil {
		return fmt.Errorf("no run script builder registered for script type %q", scriptType)
	}
	runScript, err := builder.Build(node, scriptPath, scriptWorkspacePath, string(scriptContent), params)
	if err != nil {
		return fmt.Errorf("build run script failed: %w", err)
	}
	if err := writeTextAtomic(node.CommandPath, runScript, 0o755); err != nil {
		return fmt.Errorf("write run.sh failed: %w", err)
	}
	return nil
}

func (p *FileSystemNodeRuntimePreparer) ensureNodePaths(node *types.AnalysisNode, analysis *types.Analysis) error {
	baseWorkspace := strings.TrimSpace(node.WorkspaceDir)
	if baseWorkspace == "" {
		analysisOutputDir := ""
		if analysis != nil {
			analysisOutputDir = strings.TrimSpace(analysis.WorkspaceDir)
		}
		if analysisOutputDir == "" {
			return fmt.Errorf("node workspace_dir is empty and analysis output_dir is empty")
		}
		baseWorkspace = filepath.Join(analysisOutputDir, fmt.Sprintf("%d", node.ID))
		node.WorkspaceDir = baseWorkspace
	}

	if strings.TrimSpace(node.OutputDir) == "" {
		node.OutputDir = utils.GetAnalysisNodeOutputDir(baseWorkspace) //filepath.Join(baseWorkspace, "output")
	}
	if strings.TrimSpace(node.CacheDir) == "" {
		node.CacheDir = utils.GetAnalysisNodeCacheDir(baseWorkspace) //filepath.Join(baseWorkspace, "cache")
	}
	if strings.TrimSpace(node.ParamsPath) == "" {
		node.ParamsPath = filepath.Join(baseWorkspace, "params.json")
	}
	if strings.TrimSpace(node.CommandPath) == "" {
		node.CommandPath = filepath.Join(baseWorkspace, "run.sh")
	}

	if err := os.MkdirAll(filepath.Dir(node.ParamsPath), 0o755); err != nil {
		return fmt.Errorf("create params dir failed: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(node.CommandPath), 0o755); err != nil {
		return fmt.Errorf("create command dir failed: %w", err)
	}
	return nil
}

func (p *FileSystemNodeRuntimePreparer) buildNodeParams(ctx context.Context, node *types.AnalysisNode, analysis *types.Analysis) (map[string]any, error) {
	baseParams := map[string]any{}
	if analysis != nil && strings.TrimSpace(analysis.ParamsPath) != "" {
		raw, err := os.ReadFile(analysis.ParamsPath)
		if err != nil {
			return nil, fmt.Errorf("read analysis params failed: %w", err)
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &baseParams); err != nil {
				return nil, fmt.Errorf("parse analysis params failed: %w", err)
			}
		}
	}

	resolvedInputs := map[string]any(node.ResolvedInputs)

	merged := map[string]any{}
	for k, v := range baseParams {
		merged[k] = v
	}
	for k, v := range resolvedInputs {
		merged[k] = v
	}

	// 只保留脚本 io_schema（GetScriptFormJSONByID 的 formJson）中声明过的参数名，
	// 而不是把分析级 params.json 的全部参数原样透传给脚本。
	formNames, err := p.scriptFormParamNames(ctx, node.ScriptID)
	if err != nil {
		return nil, err
	}
	if formNames != nil {
		filtered := make(map[string]any, len(merged))
		for name, value := range merged {
			if _, ok := formNames[name]; ok {
				filtered[name] = value
			}
		}
		merged = filtered
	}

	// output_dir 是运行期公共参数，不依赖 formJson，始终注入。
	merged["output_dir"] = node.OutputDir

	return merged, nil
}

// scriptFormParamNames 读取脚本 GetScriptFormJSONByID 返回的 formJson，
// 汇总其中每个条目的 name 值作为参数名白名单。
// 脚本没有 form 定义（无 io_schema / 无 name）时返回 nil，表示不做过滤。
func (p *FileSystemNodeRuntimePreparer) scriptFormParamNames(ctx context.Context, scriptID int64) (map[string]struct{}, error) {
	formJSON, _, err := p.workflowService.GetScriptFormJSONByID(ctx, scriptID)
	if err != nil {
		return nil, fmt.Errorf("load script form json failed: %w", err)
	}
	if len(formJSON) == 0 {
		return nil, nil
	}

	names := make(map[string]struct{}, len(formJSON))
	for _, itemAny := range formJSON {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		names[name] = struct{}{}
	}
	if len(names) == 0 {
		return nil, nil
	}

	return names, nil
}
func cloneAnyMapForNode(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// func (p *FileSystemNodeRuntimePreparer) initializeStandaloneNodeArtifacts(
// 	ctx context.Context,
// 	// scriptID int64,
// 	node *types.AnalysisNode,
// 	// analysis *types.Analysis,
// 	// artifacts *standaloneNodeArtifacts,
// 	// params map[string]interface{},
// ) error {
// 	// if artifacts == nil {
// 	// 	return fmt.Errorf("artifacts is nil")
// 	// }

// 	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
// 		return err
// 	}
// 	// projectDir := utils.GetProjectDir(p.storageBase, projectID)

// 	paramsPayload := cloneAnyMapForNode(map[string]interface{}(node.Params))
// 	paramsPayload["output_dir"] = node.OutputDir
// 	// paramsPayload["project_dir"] = node.projectDir
// 	paramsBytes, err := json.MarshalIndent(paramsPayload, "", "  ")
// 	if err != nil {
// 		return err
// 	}
// 	paramsBytes = append(paramsBytes, '\n')
// 	if err := os.WriteFile(node.ParamsPath, paramsBytes, 0o644); err != nil {
// 		return err
// 	}

// 	script, err := p.workflowService.GetScriptByID(ctx, node.ScriptID)
// 	if err != nil {
// 		return err
// 	}
// 	if script == nil {
// 		return fmt.Errorf("script not found")
// 	}
// 	scriptDir, scriptMainFile, err := p.workflowService.GetScriptFileByScriptID(ctx, script.ID)
// 	if err != nil {
// 		return err
// 	}
// 	scriptPath := filepath.Join(scriptDir, scriptMainFile)
// 	// baseDir := "."
// 	// if h != nil && h.config != nil && h.config.Storage != nil {
// 	// 	if v := strings.TrimSpace(h.config.Storage.BaseDir); v != "" {
// 	// 		baseDir = v
// 	// 	}
// 	// }
// 	if !filepath.IsAbs(scriptPath) {
// 		scriptPath = filepath.Join(p.storageBase, scriptPath)
// 	}

// 	scriptContent, err := os.ReadFile(scriptPath)
// 	if err != nil {
// 		return err
// 	}

// 	scriptWorkspaceDir := filepath.Join(node.WorkspaceDir, scriptMainFile)
// 	if _, err := os.Lstat(scriptWorkspaceDir); err != nil {
// 		if os.IsNotExist(err) {
// 			if err := os.Symlink(scriptPath, scriptWorkspaceDir); err != nil {
// 				return err
// 			}
// 		} else {
// 			return err
// 		}
// 	}
// 	// synlink io_schema.json
// 	ioSchemaPath := filepath.Join(scriptDir, "io_schema.json")
// 	scriptWorkspaceIoSchemaPath := filepath.Join(node.WorkspaceDir, "io_schema.json")
// 	if _, err := os.Lstat(ioSchemaPath); err == nil {
// 		if _, err := os.Lstat(scriptWorkspaceIoSchemaPath); err != nil {

// 			if os.IsNotExist(err) {
// 				if err := os.Symlink(ioSchemaPath, scriptWorkspaceIoSchemaPath); err != nil {
// 					return err
// 				}
// 			}
// 		}
// 	}

// 	// node := &types.AnalysisNode{
// 	// 	ID:           artifacts.ID,
// 	// 	ParamsPath:   artifacts.ParamsPath,
// 	// 	OutputDir:    artifacts.OutputDir,
// 	// 	WorkspaceDir: artifacts.WorkspaceDir,
// 	// 	CommandPath:  artifacts.CommandPath,
// 	// 	LogPath:      artifacts.LogPath,
// 	// }
// 	// runScript, err := buildStandaloneRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)

// 	runScript, err := BuildRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)
// 	if err != nil {
// 		return err
// 	}
// 	if err := os.WriteFile(node.CommandPath, []byte(runScript), 0o755); err != nil {
// 		return err
// 	}

// 	if _, err := os.Stat(node.LogPath); err != nil {
// 		if os.IsNotExist(err) {
// 			if err := os.WriteFile(node.LogPath, []byte(""), 0o644); err != nil {
// 				return err
// 			}
// 		} else {
// 			return err
// 		}
// 	}

// 	return nil
// }

// func buildStandaloneRunScript(
// 	node *types.AnalysisNode,
// 	scriptType string,
// 	scriptPath string,
// 	scriptContent string,
// 	params map[string]interface{},
// ) (string, error) {

// 	return BuildRunScript(node, scriptType, scriptPath, scriptContent, params)
// }

// func (p *FileSystemNodeRuntimePreparer) resolveScriptPath(scriptID string, scriptType string) string {
// 	mainFile := mainFileByScriptType(scriptType)
// 	if strings.TrimSpace(p.storageBase) == "" {
// 		return filepath.Join("pipeline", "script", scriptID, mainFile)
// 	}
// 	return filepath.Join(p.storageBase, "pipeline", "script", scriptID, mainFile)
// }

func normalizeScriptType(scriptType string) string {
	typeName := strings.ToLower(strings.TrimSpace(scriptType))
	switch typeName {
	case "", "bash", "sh":
		return "shell"
	default:
		return typeName
	}
}

func mainFileByScriptType(scriptType string) string {
	switch normalizeScriptType(scriptType) {
	case "r":
		return "main.R"
	case "python":
		return "main.py"
	case "shell":
		return "main.sh"
	default:
		return "main.sh"
	}
}

// ensureSymlink 确保 linkPath 是一个指向 targetPath 的有效软链：
//   - 链接不存在：创建软链；
//   - 链接已存在、目标有效且指向 targetPath：保持不变；
//   - 链接已失效（目标不存在）或指向其它目标：删除后重建；
//   - 链接位置上已存在普通文件/目录：不覆盖，直接返回 nil（避免误删用户文件）。
func ensureSymlink(linkPath, targetPath string) error {
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.Symlink(targetPath, linkPath)
		}
		return err
	}

	// 已存在的普通文件/目录不是软链，保持原有语义，不做覆盖。
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}

	// 校验软链：指向的目标路径一致，且目标仍然存在，才算有效。
	current, err := os.Readlink(linkPath)
	if err == nil && current == targetPath {
		if _, statErr := os.Stat(linkPath); statErr == nil {
			return nil
		}
	}

	// 失效链接或指向错误路径，删除后重建。
	if err := os.Remove(linkPath); err != nil {
		return err
	}
	return os.Symlink(targetPath, linkPath)
}

func cleanDirContents(outputDir, prefix string) error {
	// entries, err := os.ReadDir(dir)
	// if err != nil {
	// 	if os.IsNotExist(err) {
	// 		return nil
	// 	}
	// 	return err
	// }
	// for _, entry := range entries {
	// 	if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
	// 		return err
	// 	}
	// }
	// return nil
	//判断是否以prefix开头 如果不是就不删除
	if !strings.HasPrefix(outputDir, prefix) {
		logger.Warnf(context.Background(), "[cleanDirContents] output dir=%s is not under project data dir=%s, skip delete", outputDir, prefix)
		return fmt.Errorf("output dir=%s is not under project data dir=%s", outputDir, prefix)
	}
	// 删除outputDir下的所有内容，，直接判断文件夹是否存在，如果存在就删除，如果不存在就不删除
	if _, err := os.Stat(outputDir); err == nil {
		// 不删除outputDir本身，只删除里面的内容
		files, err := os.ReadDir(outputDir)
		if err != nil {
			logger.Warnf(context.Background(), "[cleanDirContents] failed to read output dir=%s, err=%v", outputDir, err)
			return err
		}
		for _, file := range files {
			filePath := filepath.Join(outputDir, file.Name())
			if err := os.RemoveAll(filePath); err != nil {
				logger.Warnf(context.Background(), "[cleanDirContents] failed to delete file=%s in output dir=%s, err=%v", filePath, outputDir, err)
				return err
			}
		}
	}

	return nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeBytesAtomic(path, content, mode)
}

func writeTextAtomic(path string, content string, mode os.FileMode) error {
	return writeBytesAtomic(path, []byte(content), mode)
}

func writeBytesAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
