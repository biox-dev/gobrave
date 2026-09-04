package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// runtimeVariableDefinitions 是所有运行时变量的声明式元数据（单一事实来源）。
// 新增变量时只需在此追加一行定义，并在对应的 provider 中补充取值逻辑。
var runtimeVariableDefinitions = map[string]types.RuntimeVariableDefinition{
	"USERID":             {Name: "USERID", Category: types.RuntimeVariableCategorySystem, Description: "运行容器的宿主用户 UID", Source: types.RuntimeVariableSourceEnv},
	"GROUPID":            {Name: "GROUPID", Category: types.RuntimeVariableCategorySystem, Description: "运行容器的宿主用户 GID", Source: types.RuntimeVariableSourceEnv},
	"DOCKER_GID":         {Name: "DOCKER_GID", Category: types.RuntimeVariableCategorySystem, Description: "docker.sock 所在组的 GID", Source: types.RuntimeVariableSourceComputed},
	"DOCKER_GROUPID":     {Name: "DOCKER_GROUPID", Category: types.RuntimeVariableCategorySystem, Description: "docker.sock 所在组的 GID（别名）", Source: types.RuntimeVariableSourceComputed},
	"R_PROFILE":          {Name: "R_PROFILE", Category: types.RuntimeVariableCategoryPackage, Description: "R 启动配置文件 Rprofile 路径", Source: types.RuntimeVariableSourceStatic},
	"PACKAGE_DIR":        {Name: "PACKAGE_DIR", Category: types.RuntimeVariableCategoryPackage, Description: "包安装根目录", Source: types.RuntimeVariableSourceStatic},
	"R_PACKAGE_DIR":      {Name: "R_PACKAGE_DIR", Category: types.RuntimeVariableCategoryPackage, Description: "R 包库目录", Source: types.RuntimeVariableSourceStatic},
	"PYTHON_PACKAGE_DIR": {Name: "PYTHON_PACKAGE_DIR", Category: types.RuntimeVariableCategoryPackage, Description: "Python 包库目录", Source: types.RuntimeVariableSourceStatic},
	"CONDA_PACKAGE_DIR":  {Name: "CONDA_PACKAGE_DIR", Category: types.RuntimeVariableCategoryPackage, Description: "Conda 环境目录", Source: types.RuntimeVariableSourceStatic},
	"PROJECT_DIR":        {Name: "PROJECT_DIR", Category: types.RuntimeVariableCategoryProject, Description: "项目数据目录", Source: types.RuntimeVariableSourceStatic},
	"PROJECT_CONFIG_DIR": {Name: "PROJECT_CONFIG_DIR", Category: types.RuntimeVariableCategoryProject, Description: "项目配置目录", Source: types.RuntimeVariableSourceStatic},
	"APP_SESSION_ID":     {Name: "APP_SESSION_ID", Category: types.RuntimeVariableCategoryAppSession, Description: "应用会话 ID", Source: types.RuntimeVariableSourceDB},
	"APPSESSION_ID":      {Name: "APPSESSION_ID", Category: types.RuntimeVariableCategoryAppSession, Description: "应用会话 ID（别名）", Source: types.RuntimeVariableSourceDB},
	"SYS_USER_ID":        {Name: "SYS_USER_ID", Category: types.RuntimeVariableCategoryAppSession, Description: "会话所属用户 ID", Source: types.RuntimeVariableSourceDB},
	"WORKSPACE_PATH":     {Name: "WORKSPACE_PATH", Category: types.RuntimeVariableCategoryWorkspace, Description: "工作区路径", Source: types.RuntimeVariableSourceDB},
	"USER_DIR":           {Name: "USER_DIR", Category: types.RuntimeVariableCategoryWorkspace, Description: "用户主目录", Source: types.RuntimeVariableSourceStatic},
	"USER_CONFIG_DIR":    {Name: "USER_CONFIG_DIR", Category: types.RuntimeVariableCategoryWorkspace, Description: "用户配置目录", Source: types.RuntimeVariableSourceStatic},
	"SCRIPT_FILE":        {Name: "SCRIPT_FILE", Category: types.RuntimeVariableCategoryAppSession, Description: "工作流脚本主文件", Source: types.RuntimeVariableSourceComputed},
	"ANALYSIS_NODE_ID":   {Name: "ANALYSIS_NODE_ID", Category: types.RuntimeVariableCategoryDagNode, Description: "DAG 分析节点 ID", Source: types.RuntimeVariableSourceDB},
	"ANALYSIS_ID":        {Name: "ANALYSIS_ID", Category: types.RuntimeVariableCategoryDagNode, Description: "分析 ID", Source: types.RuntimeVariableSourceDB},
	"NODE_ID":            {Name: "NODE_ID", Category: types.RuntimeVariableCategoryDagNode, Description: "节点标识", Source: types.RuntimeVariableSourceDB},
	"WORKSPACE_DIR":      {Name: "WORKSPACE_DIR", Category: types.RuntimeVariableCategoryWorkspace, Description: "节点工作区目录", Source: types.RuntimeVariableSourceDB},
	"OUTPUT_DIR":         {Name: "OUTPUT_DIR", Category: types.RuntimeVariableCategoryDagNode, Description: "节点输出目录", Source: types.RuntimeVariableSourceDB},
	"COMMAND_PATH":       {Name: "COMMAND_PATH", Category: types.RuntimeVariableCategoryDagNode, Description: "命令脚本路径", Source: types.RuntimeVariableSourceDB},
	"LOG_PATH":           {Name: "LOG_PATH", Category: types.RuntimeVariableCategoryDagNode, Description: "节点日志路径", Source: types.RuntimeVariableSourceDB},
}

// RuntimeVariableSet 是一组已求值的运行时变量，可派生扁平 map 或分类列表。
type RuntimeVariableSet struct {
	items []types.RuntimeVariable
}

// List 返回全部变量（含元数据），供 API 返回。
func (s *RuntimeVariableSet) List() []types.RuntimeVariable {
	if s == nil {
		return nil
	}
	return s.items
}

// ByCategory 返回指定分类的变量，供前端分组展示。
func (s *RuntimeVariableSet) ByCategory(cat types.RuntimeVariableCategory) []types.RuntimeVariable {
	if s == nil {
		return nil
	}
	out := make([]types.RuntimeVariable, 0)
	for _, v := range s.items {
		if v.Category == cat {
			out = append(out, v)
		}
	}
	return out
}

// ToMap 派生扁平 map，供 ContainerRuntimeResolver 替换占位符，行为与原 setRuntimeVar 一致（跳过空值）。
func (s *RuntimeVariableSet) ToMap() map[string]string {
	if s == nil {
		return nil
	}
	m := make(map[string]string, len(s.items))
	for _, v := range s.items {
		key := strings.TrimSpace(v.Name)
		val := strings.TrimSpace(v.Value)
		if key == "" || val == "" {
			continue
		}
		m[key] = val
	}
	return m
}

// add 追加一条变量，并用注册表补齐分类/描述等元数据。
func (s *RuntimeVariableSet) add(name, value string) {
	if s == nil {
		return
	}
	v := types.RuntimeVariable{Name: name, Value: value}
	if def, ok := runtimeVariableDefinitions[name]; ok {
		v.Category = def.Category
		v.Description = def.Description
		v.Source = def.Source
	}
	s.items = append(s.items, v)
}

// runtimeVariableBuildInput 是构建运行时变量的输入上下文。
type runtimeVariableBuildInput struct {
	baseDir   string
	tpl       *types.ContainerTemplate
	ownerType types.ContainerOwnerType
	ownerCtx  *ownerRuntimeContext
}

// buildRuntimeVariables 组装全部运行时变量（含分类、描述元数据）。
func (w *ContainerCreateWorker) buildRuntimeVariables(
	ctx context.Context,
	tpl *types.ContainerTemplate,
	ownerType types.ContainerOwnerType,
	ownerCtx *ownerRuntimeContext,
) *RuntimeVariableSet {
	in := &runtimeVariableBuildInput{
		tpl:       tpl,
		ownerType: ownerType,
		ownerCtx:  ownerCtx,
	}
	if w.cfg != nil && w.cfg.Storage != nil {
		in.baseDir = strings.TrimSpace(w.cfg.Storage.BaseDir)
	}

	set := &RuntimeVariableSet{}
	w.buildSystemVariables(set, in)
	w.buildPackageVariables(ctx, set, in)
	w.buildProjectVariables(ctx, set, in)
	w.buildAppSessionVariables(ctx, set, in)
	w.buildDagNodeVariables(ctx, set, in)

	return set
}

// buildSystemVariables 负责 USERID/GROUPID/DOCKER_GID/DOCKER_GROUPID。
func (w *ContainerCreateWorker) buildSystemVariables(set *RuntimeVariableSet, in *runtimeVariableBuildInput) {
	userID := os.Getenv("USERID")
	if userID == "" {
		userID = strconv.Itoa(os.Getuid())
	}
	groupID := os.Getenv("GROUPID")
	if groupID == "" {
		groupID = strconv.Itoa(os.Getgid())
	}
	set.add("USERID", userID)
	set.add("GROUPID", groupID)

	dockerGID := os.Getenv("DOCKER_GID")
	if dockerGID == "" {
		if gid, ok := resolvePathGID("/var/run/docker.sock"); ok {
			dockerGID = gid
		}
	}
	if dockerGID == "" {
		dockerGID = groupID
	}
	set.add("DOCKER_GID", dockerGID)
	set.add("DOCKER_GROUPID", dockerGID)
}

// buildPackageVariables 负责 R/Python/Conda 包目录与 Rprofile 路径。
func (w *ContainerCreateWorker) buildPackageVariables(ctx context.Context, set *RuntimeVariableSet, in *runtimeVariableBuildInput) {
	baseDir := in.baseDir

	packageDir := fmt.Sprintf("%s/package", baseDir)
	profilePath := fmt.Sprintf("%s/Rprofile", packageDir)
	ensureEmptyFileIfNotExists(ctx, profilePath)
	set.add("R_PROFILE", profilePath)
	set.add("PACKAGE_DIR", packageDir)

	rPackageDir := fmt.Sprintf("%s/package/R/%s", baseDir, in.tpl.GetRLibraryPath())
	set.add("R_PACKAGE_DIR", rPackageDir)
	pythonPackageDir := fmt.Sprintf("%s/package/python/%s", baseDir, in.tpl.GetPythonLibraryPath())
	set.add("PYTHON_PACKAGE_DIR", pythonPackageDir)
	condaPackageDir := fmt.Sprintf("%s/package/conda/%s", baseDir, in.tpl.GetCondaLibraryPath())
	set.add("CONDA_PACKAGE_DIR", condaPackageDir)

	ensureDirs(ctx, []string{rPackageDir, pythonPackageDir, condaPackageDir})
}

// buildProjectVariables 负责 PROJECT_DIR/PROJECT_CONFIG_DIR。
func (w *ContainerCreateWorker) buildProjectVariables(ctx context.Context, set *RuntimeVariableSet, in *runtimeVariableBuildInput) {
	if in.ownerCtx == nil || in.ownerCtx.project == nil {
		return
	}
	projectDir := fmt.Sprintf("%s/data/%s", in.baseDir, in.ownerCtx.project.ProjectID)
	set.add("PROJECT_DIR", projectDir)
	projectConfigDir := fmt.Sprintf("%s/data/%s/.config", in.baseDir, in.ownerCtx.project.ProjectID)
	set.add("PROJECT_CONFIG_DIR", projectConfigDir)

	ensureDirs(ctx, []string{projectDir, projectConfigDir})
}

// buildAppSessionVariables 负责应用会话相关变量。
func (w *ContainerCreateWorker) buildAppSessionVariables(ctx context.Context, set *RuntimeVariableSet, in *runtimeVariableBuildInput) {
	if in.ownerCtx == nil || in.ownerCtx.session == nil {
		return
	}
	session := in.ownerCtx.session

	set.add("APP_SESSION_ID", strconv.FormatInt(session.ID, 10))
	set.add("APPSESSION_ID", strconv.FormatInt(session.ID, 10))
	set.add("SYS_USER_ID", session.UserID)

	workspacePath := session.WorkspacePath
	if workspacePath == "" && in.ownerCtx.projectID != 0 && in.baseDir != "" && in.ownerCtx.project != nil {
		workspacePath = fmt.Sprintf("%s/data/%s", in.baseDir, in.ownerCtx.project.ProjectID)
		session.WorkspacePath = workspacePath
	}
	set.add("WORKSPACE_PATH", workspacePath)

	userDir := fmt.Sprintf("%s/user/%s", in.baseDir, session.UserID)
	set.add("USER_DIR", userDir)
	userConfigDir := fmt.Sprintf("%s/user/%s/.config", in.baseDir, session.UserID)
	set.add("USER_CONFIG_DIR", userConfigDir)

	if in.ownerCtx.node != nil && w.workflowService != nil {
		scriptDir, mainFile, err := w.workflowService.GetScriptFileByScriptID(ctx, in.ownerCtx.node.ScriptID)
		if err == nil && strings.TrimSpace(mainFile) != "" && strings.TrimSpace(scriptDir) != "" {
			set.add("SCRIPT_FILE", filepath.Join(scriptDir, mainFile))
		}
	}

	ensureDirs(ctx, []string{workspacePath, userDir, userConfigDir})
}

// buildDagNodeVariables 负责 DAG 节点相关变量。
func (w *ContainerCreateWorker) buildDagNodeVariables(ctx context.Context, set *RuntimeVariableSet, in *runtimeVariableBuildInput) {
	if in.ownerCtx == nil || in.ownerCtx.node == nil {
		return
	}
	node := in.ownerCtx.node

	set.add("ANALYSIS_NODE_ID", strconv.FormatUint(uint64(node.ID), 10))
	set.add("ANALYSIS_ID", strconv.FormatInt(node.AnalysisID, 10))
	set.add("NODE_ID", node.NodeID)
	set.add("WORKSPACE_PATH", node.WorkspaceDir)
	set.add("WORKSPACE_DIR", node.WorkspaceDir)
	set.add("OUTPUT_DIR", node.OutputDir)
	set.add("COMMAND_PATH", node.CommandPath)

	logPath := node.LogPath
	if strings.TrimSpace(logPath) == "" {
		if outputDir := strings.TrimSpace(node.OutputDir); outputDir != "" {
			logPath = filepath.Join(outputDir, "run.log")
		}
	}
	set.add("LOG_PATH", logPath)

	ensureDirs(ctx, []string{node.WorkspaceDir, node.OutputDir})
}
