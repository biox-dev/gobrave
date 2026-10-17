package container

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	// sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	// "github.com/minebiome/ai-agent-go/internal/application/repository"
	// "github.com/minebiome/ai-agent-go/internal/application/service"
	// "github.com/minebiome/ai-agent-go/internal/config"
	// "github.com/minebiome/ai-agent-go/internal/grpcserver"
	// "github.com/minebiome/ai-agent-go/internal/handler"
	"github.com/biox-dev/gobrave/internal/agent"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/repository"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/builtin"
	"github.com/biox-dev/gobrave/internal/application/repository"
	"github.com/biox-dev/gobrave/internal/application/service"
	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	dockerruntime "github.com/biox-dev/gobrave/internal/container_runtime/docker"
	kubernetesruntime "github.com/biox-dev/gobrave/internal/container_runtime/kubernetes"
	"github.com/biox-dev/gobrave/internal/dag"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/executor"
	nodeorchestrator "github.com/biox-dev/gobrave/internal/dag/node_orchestrator"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	scheduler "github.com/biox-dev/gobrave/internal/dag/scheduler"
	schedulerdag "github.com/biox-dev/gobrave/internal/dag/scheduler/dag"
	schedulerdataflow "github.com/biox-dev/gobrave/internal/dag/scheduler/dataflow"
	schedulerdynamic "github.com/biox-dev/gobrave/internal/dag/scheduler/dynamic"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	exportcodecv1 "github.com/biox-dev/gobrave/internal/exportcodec/v1"
	"github.com/biox-dev/gobrave/internal/handler"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/route"
	"github.com/biox-dev/gobrave/internal/router"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"

	// "github.com/minebiome/ai-agent-go/internal/router"
	// "github.com/minebiome/ai-agent-go/internal/types"
	// "github.com/minebiome/ai-agent-go/internal/utils"

	"github.com/ncruces/go-sqlite3/gormlite"
	"go.uber.org/dig"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// must is a helper function for error handling
// Panics if the error is not nil, useful for configuration steps that must succeed
// Parameters:
//   - err: Error to check
func must(err error) {
	if err != nil {
		panic(err)
	}
}

// resolveSkillsDir 计算用户技能目录（$BaseDir/.skills）；未配置返回空串。
func resolveSkillsDir(cfg *config.Config) string {
	if cfg == nil || cfg.Storage == nil {
		return ""
	}
	baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, ".skills")
}

// buildSkillRegistry 构建 Agent 技能注册表。
//
// 技能是带 frontmatter 的 SKILL.md 文档（不是可执行工具）：用户技能从 $BaseDir/.skills
// 目录发现。该注册表既用于把技能元信息注入系统提示词，也用于技能查看 API。
//
// 注册表在进程内是单例，并被 skill.Store 在运行期热更新（保存 / 删除技能时
// Register / Unregister），因此 Agent 运行期间新增的改动立即对后续调用生效。
func buildSkillRegistry(cfg *config.Config) *skill.Registry {
	reg := skill.NewRegistry()

	skillsDir := resolveSkillsDir(cfg)
	if skillsDir == "" {
		return reg
	}
	// 目录不存在，则创建
	if _, err := os.Stat(skillsDir); os.IsNotExist(err) {
		if err := os.MkdirAll(skillsDir, 0755); err != nil {
			logger.Warnf(context.Background(), "[Container] Failed to create skills directory: %v", err)
		}
	}
	for _, s := range skill.Discover([]string{skillsDir}) {
		reg.Register(s)
	}

	return reg
}

// buildSkillStore 构建用户技能的文件存储（落盘目录同 buildSkillRegistry）。
func buildSkillStore(cfg *config.Config) *skill.Store {
	return skill.NewStore(resolveSkillsDir(cfg))
}

// ensureBuiltinSkills 在启动时把内置技能写入用户技能目录（$BaseDir/.skills）。
//
// 每次都覆盖写入，保证磁盘上的内置技能与代码中的定义一致；写入失败只记录日志，
// 不阻断启动（技能缺失不影响主流程，可随下次重启重新写入）。
func ensureBuiltinSkills(cfg *config.Config) error {
	dir := resolveSkillsDir(cfg)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warnf(context.Background(), "[Container] Failed to create skills directory: %v", err)
		return err
	}
	if err := skill.EnsureBuiltinSkills(dir); err != nil {
		logger.Warnf(context.Background(), "[Container] Failed to write builtin skills: %v", err)
		return err
	}
	return nil
}

// buildAgentClient 根据配置构建 Agent 调用门面。
// 模型提供商配置表（按模型名索引）从 config.agent.providers 映射进 Options.Providers，
// 供 Provider 在每次调用时按 Profile.Model 解析出 base_url / api_key 等 LLM 配置。
// 使用哪个 Agent（Provider）由 Profile.Provider 决定。
//
// runner 是「运行分析节点」的进程级静态服务能力，注入 Options 后由 Provider 在执行工具时
// 注入工具 ctx（与可序列化的 RuntimeContext 分开，避免服务对象被序列化进 DB）。
// fileReader 同理，承载「按文件 ID 读取内容预览」的能力，供 read_file_by_id 工具使用。
// paramsBuilder 承载「按 io_schema 生成节点参数」的能力，供 build_analysis_node_params 工具使用。
// nodeUpdater 承载「更新分析节点名称」的能力，供 update_analysis_node 工具使用。
// envUpdater 承载「写回用户业务上下文（EnvID / EnvType）」的能力，供 create_script 工具使用。
// memorySaver 承载「保存一条用户长期记忆」的能力，供 save_memory 工具使用。
// skills 是技能注册表：既提供系统提示词的技能元信息，也供 get_skill /
// load_skill_resource 工具在执行期读取技能指令与附属资源。
func buildAgentClient(cfg *config.Config, registry *agent.Registry, runner tool.AnalysisNodeRunner, fileReader tool.FileReader, scriptCreator tool.ScriptCreator, paramsBuilder tool.AnalysisNodeParamsBuilder, nodeUpdater tool.AnalysisNodeUpdater, envUpdater tool.AgentEnvUpdater, memorySaver tool.MemorySaver, browser tool.ProjectResourceBrowser, skills *skill.Registry) *agent.Client {
	opts := agent.Options{}

	if cfg != nil && cfg.Agent != nil {
		providers := make(map[string]agent.ModelProviderConfig, len(cfg.Agent.Providers))
		for name, pc := range cfg.Agent.Providers {
			providers[strings.ToLower(strings.TrimSpace(name))] = agent.ModelProviderConfig{
				Model:       pc.Model,
				BaseURL:     pc.BaseURL,
				APIKey:      pc.APIKey,
				BearerToken: pc.BearerToken,
				WorkingDir:  pc.WorkingDir,
				Extra:       pc.Extra,
			}
		}
		opts.Providers = providers
	}

	// 注册框架内置工具（get_weather / run_analysis_node 等），供 Provider 的
	// tool-call 链路使用。后续新增内置工具时在 builtin.All() 中追加即可，无需改动此处。
	opts.Tools = tool.NewRegistryWith(builtin.All()...)
	// 进程级服务能力：run_analysis_node 等 builtin 工具在执行期通过它触发节点运行。
	opts.AnalysisNodeRunner = runner
	// 进程级服务能力：read_file_by_id 等 builtin 工具在执行期通过它按文件 ID 读取内容预览。
	opts.FileReader = fileReader
	// 进程级服务能力：create_script / create_analysis_node 工具在执行期通过它创建脚本与分析节点。
	opts.ScriptCreator = scriptCreator
	// 进程级服务能力：build_analysis_node_params 工具在执行期通过它按 io_schema 生成 params.json。
	opts.AnalysisNodeParamsBuilder = paramsBuilder
	// 进程级服务能力：update_analysis_node 工具在执行期通过它更新分析节点名称。
	opts.AnalysisNodeUpdater = nodeUpdater
	// 进程级服务能力：create_script 工具在执行期通过它把当前用户业务上下文切到新分析节点。
	opts.AgentEnvUpdater = envUpdater
	// 进程级服务能力：save_memory 工具在执行期通过它把注意事项写入用户长期记忆。
	opts.MemorySaver = memorySaver
	// 进程级服务能力：list_project_scripts / get_script / get_analysis_node 等只读浏览工具
	// 在执行期通过它按当前项目读取脚本与分析节点。
	opts.ProjectResourceBrowser = browser
	// 进程级服务能力：get_skill / load_skill_resource 工具在执行期通过它按名称解析技能，
	// 读取 SKILL.md 指令正文与技能目录内的附属资源。
	if skills != nil {
		opts.Skills = skills.Resolver()
	}

	return agent.NewClient(registry, opts)
}

type eventHandlerGroupIn struct {
	dig.In
	Handlers []event.Handler `group:"event_handlers"`
}

// userAgentConfigProvider 把用户的 Agent 配置读取适配为 agent.UserAgentConfigProvider，
// 使权限策略能按 userID 读取存储在 User.AgentConfig 中的许可配置。
type userAgentConfigProvider struct {
	repo interfaces.UserRepository
}

func (p userAgentConfigProvider) GetAgentConfig(ctx context.Context, userID int64) (agent.UserAgentConfig, error) {
	if userID == 0 {
		return agent.UserAgentConfig{}, nil
	}
	user, err := p.repo.GetUserByID(ctx, userID)
	if err != nil {
		return agent.UserAgentConfig{}, err
	}
	return user.AgentConfig, nil
}

// BuildContainer 注册依赖并返回容器。
// cfg 由调用方（main）提前加载，因此 Gin 模式等需要在容器构建前生效的配置
// 可以直接从 cfg 读取，避免重复解析 config.yml。
func BuildContainer(container *dig.Container, cfg *config.Config) *dig.Container {
	ctx := context.Background()
	logger.Debugf(ctx, "[Container] Starting container initialization...")

	logger.Debugf(ctx, "[Container] Registering core infrastructure...")
	must(container.Provide(func() *config.Config { return cfg }))
	must(container.Provide(initDatabase))
	must(container.Provide(func() event.Bus { return event.NewOrderedMemoryBus() }))
	must(container.Provide(containerruntime.NewRegistry))
	must(container.Provide(func(cfg *config.Config) containerruntime.MonitoringRegistry {
		// Default to in-memory monitoring registry. This provider is intentionally
		// wired through DI so we can later switch to Redis-backed implementation
		// for distributed deployments.
		_ = cfg
		return containerruntime.NewInMemoryMonitoringRegistry()
	}))
	must(container.Invoke(func(reg containerruntime.MonitoringRegistry) {
		containerruntime.SetMonitoringRegistry(reg)
	}))
	// Register container runtimes based on configuration
	must(container.Invoke(func(cfg *config.Config, reg *containerruntime.Registry) error {
		runtimes := config.ResolveContainerRuntimes(cfg)
		for _, name := range runtimes {
			switch name {
			case "docker":
				rt := dockerruntime.NewDockerRuntime()
				reg.Register(rt.Name(), rt)
			case "k8s", "k3s":
				kcfg := &config.KubernetesRuntimeConfig{Namespace: "default"}
				if cfg != nil && cfg.Container != nil && cfg.Container.Kubernetes != nil {
					kcfg = cfg.Container.Kubernetes
				}

				rt, err := kubernetesruntime.NewKubernetesRuntime(kubernetesruntime.KubernetesRuntimeConfig{
					RuntimeName: name,
					Namespace:   kcfg.Namespace,
					Kubeconfig:  kcfg.Kubeconfig,
					InCluster:   kcfg.InCluster,
				})
				if err != nil {
					return err
				}
				reg.Register(rt.Name(), rt)
			default:
				return fmt.Errorf("unsupported container runtime: %s", name)
			}
		}
		return nil
	}))

	logger.Debugf(ctx, "[Container] Registering timeline repository...")
	// must(container.Provide(repository.NewTimelineRepository))
	// must(container.Provide(repository.NewArticleRepository))
	// must(container.Provide(repository.NewArticleTranslationRepository))
	// must(container.Provide(repository.NewArticleAudioRepository))
	// must(container.Provide(repository.NewEntityRepository))
	// must(container.Provide(repository.NewTopicRepository))
	// must(container.Provide(repository.NewTopicArticleRepository))
	// must(container.Provide(repository.NewEntityTranslationRepository))
	// must(container.Provide(repository.NewArticleEntityRepository))
	// must(container.Provide(repository.NewArticleFeedRepository))
	// must(container.Provide(repository.NewSystemSettingRepository))
	must(container.Provide(repository.NewUserRepository))
	must(container.Provide(repository.NewAuthTokenRepository))
	// RunScriptBuilder 注册表：注册框架内置构建器（r / python / shell / qmd / jupyter），
	// 运行时按脚本类型解析，供 NodeRuntimePreparer 生成 run.sh 使用。
	must(container.Provide(func() *prepare.RunScriptBuilderRegistry {
		return prepare.NewRunScriptBuilderRegistry(prepare.DefaultRunScriptBuilders()...)
	}))
	must(container.Provide(repository.NewProjectRepository))
	must(container.Provide(repository.NewDataRepository))
	must(container.Provide(repository.NewStoreRepository))
	must(container.Provide(repository.NewAnalysisRepository))
	must(container.Provide(repository.NewWorkflowRepository))
	must(container.Provide(repository.NewContainerRepository))
	must(container.Provide(repository.NewLLMRepository))
	must(container.Provide(repository.NewAISummaryRepository))
	must(container.Provide(manager.NewDefaultContainerRuntimeResolver))
	must(container.Provide(manager.NewImageManager))
	must(container.Provide(manager.NewContainerManager))
	must(container.Provide(func(mgr *manager.ContainerManager) dagruntime.NodeContainerOperator {
		return mgr
	}))
	// Register the DAG executor factory so all concrete executors
	// (container, nextflow, local) are created once and resolved by name.
	must(container.Provide(executor.NewFactory))

	must(container.Provide(manager.NewOutboxDispatcher))
	// Provide ContainerCreateWorker as its concrete type.
	must(container.Provide(manager.NewContainerCreateWorker))
	// Also register the same instance in the event_handlers group so it receives bus events.
	must(container.Provide(func(w *manager.ContainerCreateWorker) event.Handler {
		return w
	}, dig.Group("event_handlers")))
	// Provide AISummaryWorker as its concrete type and register it as an event handler.
	must(container.Provide(func() *agent.Registry {
		return agent.NewRegistry(agentproviders.All()...)
	}))
	must(container.Provide(buildSkillRegistry))
	must(container.Provide(buildSkillStore))
	// 启动时把内置技能覆盖写入技能目录，确保磁盘上的内置技能与代码定义一致；
	// 必须在上面的 buildSkillRegistry 之后执行，使新写入的技能能被本次启动发现。
	must(container.Invoke(func(cfg *config.Config) error {
		return ensureBuiltinSkills(cfg)
	}))
	// 分析节点运行器：把「运行分析节点 + 读取日志」能力暴露给内置工具 run_analysis_node，
	// 经 RuntimeContextResolver 注入 RuntimeContext 后传到工具执行期。
	// 注入 EventRouter 后，等待节点完成改为订阅 NodeCompletionCoordinator 发布的
	// dag.node.completed / dag.node.failed 事件，仅保留低频状态轮询兜底。
	must(container.Provide(newAnalysisNodeRunner))
	// 文件读取器：把「按 go_file 主键 ID 读取文件（含表格预览）」能力暴露给内置工具
	// read_file_by_id，由 Provider 在执行工具前注入工具 ctx。
	must(container.Provide(newFileReader))
	// 脚本业务能力：*scriptCreator 同时实现 tool.ScriptCreator（create_script）与
	// tool.ScriptUpdater（update_script）。dig 按构造函数返回类型注册，这里显式声明接口
	// 返回类型，避免只注册成具体类型导致上层注入 tool.ScriptCreator 时找不到依赖。
	must(container.Provide(func(
		workflowSvc interfaces.WorkflowService,
		analysisRepo interfaces.AnalysisRepository,
		projectRepo interfaces.ProjectRepository,
		containerRepo interfaces.ContainerRepository,
		dataSvc interfaces.DataService,
		preparer prepare.NodeRuntimePreparer,
		cfg *config.Config,
	) tool.ScriptCreator {
		return newScriptCreator(workflowSvc, analysisRepo, projectRepo, containerRepo, dataSvc, preparer, cfg)
	}))
	// 分析节点参数生成器：把「按脚本 io_schema 解析 request_param → 刷新节点 → 写出
	// params.json」能力暴露给内置工具 build_analysis_node_params。
	must(container.Provide(newAnalysisNodeParamsBuilder))
	// 分析节点名称更新器：把「更新分析节点 node_name」能力暴露给内置工具 update_analysis_node。
	must(container.Provide(newAnalysisNodeUpdater))
	// 项目资源浏览器：把「列出项目脚本 / 按 ID 查看脚本 / 按 ID 查看分析节点」能力暴露给
	// 内置只读工具 list_project_scripts / get_script / get_analysis_node。
	must(container.Provide(newProjectResourceBrowser))
	// 用户业务上下文写回器：把「更新 user.AgentConfig.EnvID / EnvType」能力暴露给内置工具
	// create_script，使其创建分析节点后把当前上下文切到新节点。
	must(container.Provide(newAgentEnvUpdater))
	// 记忆管理器以具体类型提供一次：既供 AgentService 使用，也供 memorySaver（save_memory 工具）
	// 复用同一个实例，避免记忆域出现两份独立的状态入口。
	must(container.Provide(func(mems agent.MemoryRepository) *agent.MemoryManager {
		return agent.NewMemoryManager(agent.MemoryConfig{Repo: mems}) // Extractor: agent.MockMemoryExtractor(),
	}))
	// 长期记忆写回器：把「保存一条用户记忆」能力暴露给内置工具 save_memory，
	// 使模型可把「遇到的可复现错误 / 注意事项」写入记忆，后续轮次随背景注入避免重复踩坑。
	must(container.Provide(newMemorySaver))
	must(container.Provide(func(cfg *config.Config, registry *agent.Registry, runner tool.AnalysisNodeRunner, fileReader tool.FileReader, scriptCreator tool.ScriptCreator, paramsBuilder tool.AnalysisNodeParamsBuilder, nodeUpdater tool.AnalysisNodeUpdater, envUpdater tool.AgentEnvUpdater, memorySaver tool.MemorySaver, browser tool.ProjectResourceBrowser, skills *skill.Registry) *agent.Client {
		return buildAgentClient(cfg, registry, runner, fileReader, scriptCreator, paramsBuilder, nodeUpdater, envUpdater, memorySaver, browser, skills)
	}))
	// Provide AgentService：编排层（任务/权限/事件/恢复）。
	// Repository 切换到数据库实现，使任务 / 权限 / 事件状态跨进程重启持久化。
	must(container.Provide(func(db *gorm.DB) agent.TaskRepository {
		return agentrepository.NewGormTaskRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.PermissionRepository {
		return agentrepository.NewGormPermissionRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.QuestionRepository {
		return agentrepository.NewGormQuestionRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.EventRepository {
		return agentrepository.NewGormEventRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.MemoryRepository {
		return agentrepository.NewGormMemoryRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.ProfileRepository {
		return agentrepository.NewGormProfileRepository(db)
	}))
	// 系统启动时同步内置 Profile 到数据库（按 ID 判断：不存在则新增、已存在则更新）。
	must(container.Invoke(func(repo agent.ProfileRepository) error {
		return agent.EnsureBuiltinProfiles(ctx, repo)
	}))
	// 启动时补齐默认容器模板：存量数据没有 is_default 标记，若没有任何默认为 create_script
	// 兜底选一条最早的绑定行，避免「创建脚本提示未配置默认容器模板」。
	must(container.Invoke(func(containerRepo interfaces.ContainerRepository) error {
		return service.EnsureDefaultContainerTemplate(ctx, containerRepo)
	}))

	must(container.Provide(func(
		client *agent.Client,
		tasks agent.TaskRepository,
		perms agent.PermissionRepository,
		questions agent.QuestionRepository,
		events agent.EventRepository,
		mems *agent.MemoryManager,
		profiles agent.ProfileRepository,
		skills *skill.Registry,
		userRepo interfaces.UserRepository,
	) *agent.AgentService {

		return agent.NewService(agent.ServiceConfig{
			Client:    client,
			Tasks:     tasks,
			Perms:     agent.NewPermissionManager(perms),
			Questions: agent.NewQuestionManager(questions),
			Events:    events,
			Memory:    mems,
			Profiles:  agent.NewProfileManager(profiles),
			Skills:    skills,
			Policy:    agent.NewUserPermissionPolicy(userAgentConfigProvider{repo: userRepo}),
		})
	}))
	// Provide ConversationService：多轮对话编排层（复用 AgentService 的任务状态机）。
	// 使用数据库 Repository，使会话历史跨进程重启持久化。
	must(container.Provide(func(svc *agent.AgentService, db *gorm.DB) *agent.ConversationService {
		return agent.NewConversationService(svc, agentrepository.NewGormConversationRepository(db))
	}))
	must(container.Provide(manager.NewAISummaryContentProvider))
	must(container.Provide(manager.NewAISummaryWorker))
	must(container.Provide(func(w *manager.AISummaryWorker) event.Handler {
		return w
	}, dig.Group("event_handlers")))
	must(container.Provide(func(cfg *config.Config) (*route.Registry, error) {
		return route.NewRegistryFromConfig(cfg)
	}))
	// In-process gateway: database-backed path->backend resolver used by
	// AppSessionProxy. It is injected both as its concrete type (for the route
	// handler to add/remove records) and as route.PathRouteResolver.
	must(container.Provide(func(db *gorm.DB) (*route.Gateway, error) {
		return route.NewGateway(db)
	}))
	must(container.Provide(func(g *route.Gateway) route.PathRouteResolver {
		return g
	}))
	must(container.Provide(
		route.NewRouteRegistryHandler,
		dig.As(new(event.Handler)),
		dig.Group("event_handlers"),
	))
	must(container.Provide(
		manager.NewAppSessionEventHandler,
		dig.As(new(event.Handler)),
		dig.Group("event_handlers"),
	))
	must(container.Provide(
		manager.NewAISummaryEventHandler,
		dig.As(new(event.Handler)),
		dig.Group("event_handlers"),
	))
	must(container.Provide(
		dag.NewDagRuntimeEventNotifier,
		dig.As(new(event.Handler)),
		dig.Group("event_handlers"),
	))
	// must(container.Provide(repository.NewTenantRepository))
	// must(container.Provide(repository.NewTraceRepository))
	// must(container.Provide(repository.NewRSSSourceRepository))

	logger.Debugf(ctx, "[Container] Registering timeline services...")
	// must(container.Provide(service.NewWeightedRankingStrategy))
	// must(container.Provide(service.NewFeedBuilder))
	// must(container.Provide(service.NewFeedDispatcher))
	// must(container.Provide(service.NewFeedEventProducer))
	// // must(container.Provide(service.NewFeedBackfillRunner))
	// // must(container.Provide(service.NewIngestionOrchestrator))
	// must(container.Provide(service.NewTimelineService))
	// must(container.Provide(service.NewArticleService))
	// must(container.Provide(service.NewArticleTranslationService))
	// must(container.Provide(service.NewArticleAudioService))
	// must(container.Provide(service.NewEntityService))
	// must(container.Provide(service.NewTopicService))
	// must(container.Provide(service.NewTopicArticleService))
	// must(container.Provide(service.NewEntityTranslationService))
	// must(container.Provide(service.NewArticleEntityService))
	// must(container.Provide(service.NewTenantService))
	must(container.Provide(service.NewUserService))
	must(container.Provide(service.NewProjectService))
	must(container.Provide(service.NewDataService))
	must(container.Provide(service.NewStoreService))
	must(container.Provide(service.NewAnalysisService))

	// NodeRuntimePreparer：文件系统实现，负责在节点执行前准备脚本 / 参数 / 工作目录。
	// 通过 dig.As 将其以 NodeRuntimePreparer 接口暴露，供 NewNodeDispatcher 注入。
	must(container.Provide(
		prepare.NewFileSystemNodeRuntimePreparer,
		dig.As(new(prepare.NodeRuntimePreparer)),
	))
	// 节点 request_param 解析器：复用 handler 侧静态分析的解析逻辑
	// (analysisform.BuildParseAnalysisResult)，把运行期 propagateOutputs 重写后的原始输入
	// （如 SelectAssay 的 {"files": "<id>"}）还原成结构化 resolved_inputs。
	// 以 dag.NodeRequestParser 接口注入 NodeDispatcher / NodeCompletionCoordinator。
	must(container.Provide(handler.NewNodeRequestParser))
	must(container.Provide(dag.NewNodeDispatcher))

	// 调度层指纹器：复用同一份 preparer 预测 run.sh / params.json 的摘要，
	// 只给缓存策略判断“已存在的节点能否复用”用。执行前的准备（含输出目录清理）
	// 仍然只保留 NodeDispatcher 一处，避免每个节点被 prepare 两次。
	must(container.Provide(
		dagruntime.NewPreparerFingerprinter,
		dig.As(new(dagruntime.NodeArtifactFingerprinter)),
	))

	// 进程级运行时事件路由器：整个进程只在下面的 event_handlers Invoke 里订阅 bus 一次，
	// 由它按 analysis_id 把 RuntimeEvent 路由给每个 run 自己的 sink。
	// 之所以不按 run 订阅：event.Bus 没有 Unsubscribe，每次 run Subscribe 都会永久留下
	// 一个订阅者 goroutine，且 OrderedMemoryBus 对每个订阅者是阻塞发送，并发 run 越多
	// 阻塞面越大、每个订阅者都会被其它 analysis 的事件唤醒。
	must(container.Provide(dagruntime.NewEventRouter))
	// 同一个 router 也注册为 event.Handler 进入 event_handlers 分组，
	// 复用容器末尾那一次统一的 bus.Subscribe。
	must(container.Provide(func(r *dagruntime.EventRouter) event.Handler {
		return r
	}, dig.Group("event_handlers")))
	// 进程级运行态登记表：v1 / v2 调度器共享同一份，使"本进程是否已有该 analysis 在跑"
	// 在重复提交判断、RequestStop、运行快照 / 恢复扫描上口径一致。
	must(container.Provide(dagruntime.NewRunningRegistry))

	must(container.Provide(nodeorchestrator.NewNodeOrchestrator))
	// All DAG schedulers are built once and registered under the stable name each
	// one persists in analysis.scheduler_mode. Submit / stop / recovery resolve the
	// owning scheduler through this single registry instead of hardcoding one per
	// endpoint. See internal/dag/scheduler/registry.go.
	must(container.Provide(newSchedulerRegistry))
	must(container.Provide(service.NewWorkflowService))
	must(container.Provide(service.NewContainerService))
	must(container.Provide(service.NewLLMService))
	must(container.Provide(service.NewAISummaryService))
	must(container.Provide(service.NewSheetFileService))
	// 导出/安装文件格式版本注册表（script.json / workflow.json 顶层的 version）。
	// 新增一个版本 = 新写一个 exportcodec 子包 + 这里多 Register 一行，
	// handler 侧只做 exportCodecs.Get(version)，不需要任何 switch version。
	// Codec 实现直接持有 WorkflowService（写侧按主键生成导出 payload，安装侧 upsert
	// workflow/script 行）、ContainerService（安装侧 upsert 容器资产）与 git 提交身份
	// （写侧落盘后自行提交目录改动，见 utils.CommitDirChanges）。
	must(container.Provide(func(cfg *config.Config, workflowService interfaces.WorkflowService, containerService interfaces.ContainerService) *exportcodec.Registry {
		reg := exportcodec.NewRegistry()
		gitUser, gitEmail := config.ResolveGitIdentity(cfg)
		reg.Register(exportcodecv1.NewCodec(workflowService, containerService, utils.GitIdentity{Name: gitUser, Email: gitEmail}))
		return reg
	}))
	must(container.Provide(
		dagruntime.NewNodeCompletionCoordinator,
		dig.As(new(event.Handler)),
		dig.Group("event_handlers"),
	))

	// must(container.Provide(service.NewTraceService))
	// must(container.Provide(service.NewAuthService))
	// must(container.Provide(service.NewRSSSourceService))

	// HTTP handlers layer
	logger.Debugf(ctx, "[Container] Registering HTTP handlers...")
	// must(container.Provide(handler.NewTimelineHandler))
	// // must(container.Provide(handler.NewParserCallbackHandler))
	// must(container.Provide(handler.NewArticleHandler))
	// must(container.Provide(handler.NewArticleTranslationHandler))
	// must(container.Provide(handler.NewArticleAudioHandler))
	// must(container.Provide(handler.NewEntityHandler))
	// must(container.Provide(handler.NewTopicHandler))
	// must(container.Provide(handler.NewTopicArticleHandler))
	// must(container.Provide(handler.NewEntityTranslationHandler))
	// must(container.Provide(handler.NewArticleEntityHandler))
	must(container.Provide(handler.NewAuthHandler))
	must(container.Provide(handler.NewProjectHandler))
	must(container.Provide(handler.NewDataHandler))
	must(container.Provide(handler.NewStoreHandler))
	must(container.Provide(handler.NewContainerHandler))
	must(container.Provide(handler.NewAnalysisHandler))
	must(container.Provide(handler.NewWorkflowHandler))
	must(container.Provide(handler.NewGitHandler))
	must(container.Provide(handler.NewSettingHandler))
	must(container.Provide(handler.NewConfigHandler))
	must(container.Provide(handler.NewSheetHandler))
	must(container.Provide(handler.NewUploadHandler))
	must(container.Provide(handler.NewFileHandler))
	must(container.Provide(handler.NewProxyHandler))
	must(container.Provide(realtime.NewHub))
	must(container.Provide(handler.NewRealtimeHandler))
	// RuntimeContextResolver：把业务 env(type,id) 解析为系统提示词 + 工作目录，
	// 供 LLM 桥接（LLMHandler）与 Agent 会话（AgentHandler）共用。
	must(container.Provide(handler.NewRuntimeContextResolver))
	must(container.Provide(handler.NewLLMHandler))
	must(container.Provide(handler.NewAISummaryHandler))
	must(container.Provide(handler.NewAgentHandler))
	// must(container.Provide(handler.NewTraceHandler))

	// must(container.Provide(grpcserver.NewTraceServer))
	// must(container.Provide(grpcserver.NewArticleServer))
	// must(container.Provide(grpcserver.NewServer))
	logger.Debugf(ctx, "[Container] Registering task enqueuer...")
	redisAvailable := os.Getenv("REDIS_ADDR") != ""
	if redisAvailable {
		// 当有人需要 *TaskEnqueuer  时，请调用 NewAsyncqClient() 创建
		// 遵循依赖倒置原则
		// 不要依赖 client 而依赖 TaskEnqueuer task interfaces.TaskEnqueuer
		must(container.Provide(router.NewAsyncqClient, dig.As(new(interfaces.TaskEnqueuer))))
		must(container.Provide(router.NewAsynqServer))
	} else {
		syncExec := router.NewSyncTaskExecutor()
		must(container.Provide(func() interfaces.TaskEnqueuer { return syncExec }))
		must(container.Provide(func() *router.SyncTaskExecutor { return syncExec }))
	}

	// Router configuration
	logger.Debugf(ctx, "[Container] Registering router and starting task server...")
	must(container.Provide(router.NewRouter))
	if redisAvailable {
		must(container.Invoke(router.RunAsynqServer))
	} else {
		must(container.Invoke(router.RegisterSyncHandlers))
	}
	must(container.Invoke(func(mgr *manager.ContainerManager, reg *containerruntime.Registry) {
		for _, rt := range reg.List() {
			rt.SetEventHandler(mgr)
		}
	}))

	// Startup runtime reconciler
	must(container.Invoke(func(mgr *manager.ContainerManager) {
		mgr.RunRuntimeReconciler(context.Background(), 600*time.Second)
	}))
	// Startup event handlers
	must(container.Invoke(func(bus event.Bus, in eventHandlerGroupIn) {
		for _, h := range in.Handlers {
			bus.Subscribe(h)
		}
	}))

	// Startup agent task recovery：重启后重建运行态（见 design.md 第 6 / 19 节）。
	must(container.Invoke(func(svc *agent.AgentService) {
		if err := svc.Recover(context.Background()); err != nil {
			logger.Warnf(context.Background(), "[Container] agent task recovery failed: %v", err)
		}
	}))

	// Startup image status refresh
	// must(container.Invoke(func(cfg *config.Config, imageMgr *manager.ImageManager) {
	// 	enabled := true
	// 	if cfg != nil && cfg.Container != nil {
	// 		enabled = cfg.Container.RefreshImageStatusOnStart
	// 	}
	// 	if !enabled {
	// 		logger.Infof(context.Background(), "[Container] startup image status refresh disabled by config")
	// 		return
	// 	}

	// 	manager.RunImageStatusRefreshOnStart(imageMgr)
	// }))

	// Startup DAG recovery: a single unified entry that fetches every
	// running/stopping analysis and dispatches each to the scheduler that owns it
	// (by scheduler_mode). See dag.RecoverDag for details.
	must(container.Invoke(dag.RecoverDag))
	must(container.Invoke(manager.RunOutboxDispatcher))

	// The worker is subscribed to the event bus via dig.Group("event_handlers")
	// and acts as a pure consumer/executor for outbox request events.

	return container
}

// newSchedulerRegistry builds every DAG scheduler exactly once and registers it
// under the stable name the scheduler persists in analysis.scheduler_mode.
//
// Submit, stop and crash recovery all resolve the owning scheduler through this
// single table, so the three code paths can never disagree about who advances an
// analysis. Adding a scheduler is one Register call here plus a
// types.NormalizeSchedulerMode entry.
func newSchedulerRegistry(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	workflowService interfaces.WorkflowService,
	projectRepo interfaces.ProjectRepository,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
	dispatcher *dagruntime.NodeDispatcher,
	fingerprinter dagruntime.NodeArtifactFingerprinter,
	runningRegistry *dagruntime.RunningRegistry,
	router *dagruntime.EventRouter,
	bus event.Bus,
	requestParser dagruntime.NodeRequestParser,
	cfg *config.Config,
) *scheduler.Registry {
	registry := scheduler.NewRegistry()

	registry.Register(schedulerdag.NewDagOrchestrator(
		repo, workflowRepo, projectRepo, containerRepo, dispatcher, containerMgr, cfg, bus, runningRegistry,
	))
	registry.Register(schedulerdynamic.NewDynamicDagOrchestratorV2(
		repo, workflowRepo, workflowService, containerRepo, dispatcher, fingerprinter, runningRegistry, router, bus, requestParser,
	))
	registry.Register(schedulerdataflow.NewDataflowDagOrchestratorV3(
		repo, workflowRepo, workflowService, containerMgr, projectRepo, dispatcher, fingerprinter, cfg, bus, router, requestParser,
	))

	return registry
}

// gormLogLevelFromString 把 database.log_level 映射为 GORM 的日志级别。
// 取值已在 config.ResolveDatabaseLogLevel 归一化，这里只做枚举映射，
// 未覆盖的取值兜底为 Warn，避免误开 Info 刷屏。
func gormLogLevelFromString(level string) gormlogger.LogLevel {
	switch level {
	case config.DatabaseLogLevelSilent:
		return gormlogger.Silent
	case config.DatabaseLogLevelError:
		return gormlogger.Error
	case config.DatabaseLogLevelInfo:
		return gormlogger.Info
	default:
		return gormlogger.Warn
	}
}

// newGormLogger 构建 GORM 日志器。
// 级别来自 database.log_level（silent/error/warn/info，默认 warn）；
// 慢查询阈值与输出目标保持 GORM 默认（200ms，stdout）；
// 同时忽略 ErrRecordNotFound，避免内置 Profile 等“按约定查不到”的查询
// 刷出 record not found 噪音，因为调用方已经把该错误转成领域错误处理。
func newGormLogger(cfg *config.Config) gormlogger.Interface {
	return gormlogger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormLogLevelFromString(config.ResolveDatabaseLogLevel(cfg)),
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
}

func initDatabase(cfg *config.Config) (*gorm.DB, error) {
	dbCfg := cfg.Database
	if dbCfg == nil {
		dbCfg = &config.DatabaseConfig{Driver: "sqlite", SSLMode: "disable"}
	}
	driver := dbCfg.Driver
	if driver == "" {
		driver = "sqlite"
	}
	driver = strings.ToLower(driver)

	var dialector gorm.Dialector
	switch driver {
	case "postgres":
		sslMode := dbCfg.SSLMode
		if sslMode == "" {
			sslMode = "disable"
		}
		port := dbCfg.Port
		if port == "" {
			port = "5432"
		}
		gormDSN := fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
			dbCfg.Host,
			port,
			dbCfg.User,
			dbCfg.Password,
			dbCfg.Name,
			sslMode,
		)
		dialector = postgres.Open(gormDSN)

		logger.Infof(context.Background(), "DB Config: user=%s host=%s port=%s dbname=%s",
			dbCfg.User,
			dbCfg.Host,
			port,
			dbCfg.Name,
		)
	case "mysql":
		host := dbCfg.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := dbCfg.Port
		if port == "" {
			port = "3306"
		}
		gormDSN := fmt.Sprintf(
			"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
			dbCfg.User,
			dbCfg.Password,
			host,
			port,
			dbCfg.Name,
		)
		dialector = mysql.Open(gormDSN)

		logger.Infof(context.Background(), "DB Config: driver=mysql user=%s host=%s port=%s dbname=%s",
			dbCfg.User,
			host,
			port,
			dbCfg.Name,
		)
	case "sqlite":
		dbPath := dbCfg.Path
		if dbPath == "" {
			baseDir := cfg.Storage.BaseDir
			dbPath = filepath.Join(baseDir, "db", "gobrave.db")
		}
		resolvedDBPath, err := utils.ResolveExternalPath(dbPath)
		logger.Infof(context.Background(), "Resolved SQLite DB path: %s", resolvedDBPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve sqlite db path: %w", err)
		}
		dbPath = resolvedDBPath
		if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create SQLite data directory %s: %w", dir, err)
			}
		}
		// sqlite_vec.Auto()
		dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
		dialector = gormlite.Open(dsn)
		logger.Infof(context.Background(), "DB Config: driver=sqlite path=%s", dbPath)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}
	logger.Infof(context.Background(), "DB Config: sql_log_level=%s", config.ResolveDatabaseLogLevel(cfg))
	db, err := gorm.Open(dialector, &gorm.Config{
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
		// SQL 日志级别由 database.log_level 控制（silent/error/warn/info，默认 warn）。
		Logger: newGormLogger(cfg),
	})
	if err != nil {
		return nil, err
	}

	if driver == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
		}
		if err := sqlDB.Ping(); err != nil {
			return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
		}
	}

	if err := db.AutoMigrate(
		// &types.Timeline{},
		// &types.Article{},
		// &types.ArticleTranslation{},
		// &types.ArticleAudio{},
		// &types.Entity{},
		// &types.Topic{},
		// &types.TopicArticle{},
		// &types.EntityTranslation{},
		// &types.ArticleEntity{},
		// &types.ArticleFeed{},
		// &types.SystemSetting{},
		&types.User{},
		// &types.Tenant{},
		&types.Project{},
		&types.UserProject{},
		&types.ProjectReport{},
		&types.ProjectReportItem{},
		&types.Literature{},
		&types.ProjectLiterature{},
		&types.Dataset{},
		&types.ProjectDataset{},
		&types.File{},
		&types.DatasetFile{},
		&types.Assay{},
		&types.DatasetAssay{},
		&types.Store{},
		&types.Script{},
		&types.Workflow{},
		&types.Analysis{},
		&types.AnalysisNode{},
		&types.AnalysisEdge{},
		&types.AuthToken{},
		&types.ContainerImage{},
		&types.ContainerTemplateSpec{},
		&types.ContainerTemplateDefinition{},
		&types.AppSession{},
		&types.ContainerInstance{},
		&types.ContainerEvent{},
		&types.LLMSession{},
		&types.LLMConversation{},
		&types.GatewayRoute{},
		&types.OutboxEvent{},
		&types.AISummary{},
		&agent.Task{},
		&agent.PermissionRequest{},
		&agent.QuestionRequest{},
		&agent.AgentEvent{},
		&agent.Conversation{},
		&agent.ConversationMessage{},
		&agent.Memory{},
		&agent.Profile{},
	// &types.Trace{},
	// &types.RSSSource{},
	); err != nil {
		return nil, fmt.Errorf("failed to auto migrate tables: %w", err)
	}

	// if err := migratePipelineComponentsContainerIDType(db, driver); err != nil {
	// 	return nil, fmt.Errorf("failed to migrate pipeline_components.container_id type: %w", err)
	// }

	// Get underlying SQL DB object
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	// Configure connection pool parameters
	if driver == "sqlite" {
		// SQLite only supports one concurrent writer even in WAL mode.
		// Limiting to a single open connection serialises all DB access and
		// prevents "database is locked" errors from concurrent goroutines.
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxIdleConns(10)
	}
	sqlDB.SetConnMaxLifetime(time.Duration(10) * time.Minute)

	return db, nil
}

// func migratePipelineComponentsContainerIDType(db *gorm.DB, driver string) error {
// 	columnTypes, err := db.Migrator().ColumnTypes(&types.Script{})
// 	if err == nil {
// 		for _, col := range columnTypes {
// 			if !strings.EqualFold(col.Name(), "container_id") {
// 				continue
// 			}
// 			dbType := strings.ToUpper(strings.TrimSpace(col.DatabaseTypeName()))
// 			if strings.Contains(dbType, "BIGINT") {
// 				return nil
// 			}
// 			break
// 		}
// 	}

// 	if driver == "mysql" {
// 		if err := db.Exec(`UPDATE pipeline_components SET container_id = NULL WHERE container_id IS NOT NULL AND TRIM(container_id) <> '' AND container_id NOT REGEXP '^[0-9]+$'`).Error; err != nil {
// 			return err
// 		}
// 		if err := db.Exec(`UPDATE pipeline_components SET container_id = NULL WHERE TRIM(container_id) = ''`).Error; err != nil {
// 			return err
// 		}
// 		if err := db.Exec(`ALTER TABLE pipeline_components MODIFY COLUMN container_id BIGINT NULL`).Error; err != nil {
// 			return err
// 		}
// 		return nil
// 	}

// 	if driver == "postgres" {
// 		if err := db.Exec(`UPDATE pipeline_components SET container_id = NULL WHERE container_id IS NOT NULL AND btrim(container_id) <> '' AND btrim(container_id) !~ '^[0-9]+$'`).Error; err != nil {
// 			return err
// 		}
// 		if err := db.Exec(`UPDATE pipeline_components SET container_id = NULL WHERE btrim(container_id) = ''`).Error; err != nil {
// 			return err
// 		}
// 		if err := db.Exec(`
// 			ALTER TABLE pipeline_components
// 			ALTER COLUMN container_id TYPE BIGINT USING NULLIF(btrim(container_id), '')::BIGINT
// 		`).Error; err != nil {
// 			return err
// 		}
// 		return nil
// 	}

// 	// SQLite has dynamic typing; AlterColumn keeps schema metadata aligned with model declaration.
// 	return db.Migrator().AlterColumn(&types.Script{}, "ContainerID")
// }
