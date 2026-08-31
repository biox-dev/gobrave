package container

import (
	"context"
	"fmt"
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
	"github.com/biox-dev/gobrave/internal/agent/skill"
	skillbuiltin "github.com/biox-dev/gobrave/internal/agent/skill/builtin"
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
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/event"
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

// buildSkillRegistry 构建 Agent 技能注册表。
//
// 注册框架内置技能（echo / get_weather 等），并从 $BaseDir/.skills 目录加载用户
// 自定义技能（SKILL.md）合并进注册表；目录不存在或加载失败时静默跳过。
// 独立为 Provider 后，可供 Agent Client 与技能查看 API 共用同一份注册表。
func buildSkillRegistry(cfg *config.Config) *skill.Registry {
	reg := skill.NewRegistryWith(skillbuiltin.All()...)

	if cfg != nil && cfg.Storage != nil && strings.TrimSpace(cfg.Storage.BaseDir) != "" {
		skillsDir := filepath.Join(cfg.Storage.BaseDir, ".skills")
		if loaded, err := skill.NewLoader().LoadDir(skillsDir); err == nil {
			for _, s := range loaded {
				reg.Register(s)
			}
		}
	}

	return reg
}

// buildAgentClient 根据配置构建 Agent 调用门面。
// 默认 Provider 与 Options 从 config.agent 读取；未配置时兜底为 agent.DefaultProvider（mock）。
func buildAgentClient(cfg *config.Config, registry *agent.Registry, skills *skill.Registry) *agent.Client {
	defaultProvider := agent.DefaultProvider
	opts := agent.Options{}

	if cfg != nil && cfg.Agent != nil {
		if p := strings.ToLower(strings.TrimSpace(cfg.Agent.Default)); p != "" {
			defaultProvider = p
		}
		if pc, ok := cfg.Agent.Providers[defaultProvider]; ok {
			opts = agent.Options{
				Model:       pc.Model,
				BaseURL:     pc.BaseURL,
				APIKey:      pc.APIKey,
				BearerToken: pc.BearerToken,
				WorkingDir:  pc.WorkingDir,
				Extra:       pc.Extra,
			}
		}
	}

	// 注册框架内置工具（get_weather 等），供 Provider 的 tool-call 链路使用。
	// 后续新增内置工具时在 builtin.All() 中追加即可，无需改动此处。
	opts.Tools = tool.NewRegistryWith(builtin.All()...)

	// 技能注册表由 buildSkillRegistry 统一构建（内置 + 用户自定义），
	// 供 Provider 的 skill-call 链路使用。
	opts.Skills = skills

	return agent.NewClient(registry, defaultProvider, opts)
}

type eventHandlerGroupIn struct {
	dig.In
	Handlers []event.Handler `group:"event_handlers"`
}

func BuildContainer(container *dig.Container) *dig.Container {
	ctx := context.Background()
	logger.Debugf(ctx, "[Container] Starting container initialization...")

	logger.Debugf(ctx, "[Container] Registering core infrastructure...")
	must(container.Provide(config.LoadConfig))
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
		switch config.ResolveContainerRuntime(cfg) {
		case "docker":
			rt := dockerruntime.NewDockerRuntime()
			reg.Register(rt.Name(), rt)
			return nil
		case "k8s", "k3s":
			kcfg := &config.KubernetesRuntimeConfig{Namespace: "default"}
			if cfg != nil && cfg.Container != nil && cfg.Container.Kubernetes != nil {
				kcfg = cfg.Container.Kubernetes
			}

			rt, err := kubernetesruntime.NewKubernetesRuntime(kubernetesruntime.KubernetesRuntimeConfig{
				RuntimeName: config.ResolveContainerRuntime(cfg),
				Namespace:   kcfg.Namespace,
				Kubeconfig:  kcfg.Kubeconfig,
				InCluster:   kcfg.InCluster,
			})
			if err != nil {
				return err
			}
			reg.Register(rt.Name(), rt)
			return nil
		default:
			return fmt.Errorf("unsupported container runtime: %s", config.ResolveContainerRuntime(cfg))
		}
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
	must(container.Provide(prepare.NewRunScriptBuilders))
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
	must(container.Provide(func(cfg *config.Config, registry *agent.Registry, skills *skill.Registry) *agent.Client {
		return buildAgentClient(cfg, registry, skills)
	}))
	// Provide AgentService：编排层（任务/权限/事件/恢复）。
	// Repository 切换到数据库实现，使任务 / 权限 / 事件状态跨进程重启持久化。
	must(container.Provide(func(db *gorm.DB) agent.TaskRepository {
		return agent.NewGormTaskRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.PermissionRepository {
		return agent.NewGormPermissionRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.EventRepository {
		return agent.NewGormEventRepository(db)
	}))
	must(container.Provide(func(db *gorm.DB) agent.MemoryRepository {
		return agent.NewGormMemoryRepository(db)
	}))
	// 项目上下文提供者：把当前项目已完成的分析节点注入 Agent 的 SystemPrompt。
	must(container.Provide(manager.NewAgentProjectContextProvider))
	must(container.Provide(func(
		client *agent.Client,
		tasks agent.TaskRepository,
		perms agent.PermissionRepository,
		events agent.EventRepository,
		mems agent.MemoryRepository,
		projCtx *manager.AgentProjectContextProvider,
	) *agent.AgentService {

		return agent.NewService(agent.ServiceConfig{
			Client:  client,
			Tasks:   tasks,
			Perms:   agent.NewPermissionManager(perms),
			Events:  events,
			Memory:  agent.NewMemoryManager(agent.MemoryConfig{Repo: mems}), // Extractor: agent.MockMemoryExtractor(),
			Project: projCtx,
		})
	}))
	// Provide ConversationService：多轮对话编排层（复用 AgentService 的任务状态机）。
	// 使用数据库 Repository，使会话历史跨进程重启持久化。
	must(container.Provide(func(svc *agent.AgentService, db *gorm.DB) *agent.ConversationService {
		return agent.NewConversationService(svc, agent.NewGormConversationRepository(db))
	}))
	must(container.Provide(manager.NewAISummaryContentProvider))
	must(container.Provide(manager.NewAISummaryWorker))
	must(container.Provide(func(w *manager.AISummaryWorker) event.Handler {
		return w
	}, dig.Group("event_handlers")))
	must(container.Provide(func(cfg *config.Config, db *gorm.DB) (route.RouteRegistry, error) {
		if cfg == nil || cfg.Route == nil {
			return route.NewGatewayRegistry(db)
		}

		registryName := strings.ToLower(strings.TrimSpace(cfg.Route.Registry))
		switch registryName {
		case "", "gateway":
			return route.NewGatewayRegistry(db)
		case "traefik":
			traefikCfg := cfg.Route.Traefik
			if traefikCfg == nil {
				return nil, fmt.Errorf("route.traefik config is required when route.registry=traefik")
			}

			reg, err := route.NewTraefikRegistry(route.TraefikRegistryConfig{
				Provider:   traefikCfg.Provider,
				BaseURL:    traefikCfg.BaseURL,
				UpsertPath: traefikCfg.UpsertPath,
				DeletePath: traefikCfg.DeletePath,
				AuthToken:  traefikCfg.AuthToken,
				Timeout:    time.Duration(traefikCfg.TimeoutSecond) * time.Second,
				FilePath:   traefikCfg.FilePath,
			})
			if err != nil {
				return nil, err
			}
			return reg, nil
		case "k8s-ingress", "k8s_ingress", "k8singress":
			ingressCfg := cfg.Route.K8sIngress
			if ingressCfg == nil {
				ingressCfg = &config.K8sIngressRouteConfig{}
			}

			namespace := strings.TrimSpace(ingressCfg.Namespace)
			if namespace == "" && cfg.Container != nil && cfg.Container.Kubernetes != nil {
				namespace = strings.TrimSpace(cfg.Container.Kubernetes.Namespace)
			}
			if namespace == "" {
				namespace = "default"
			}

			kubeconfig := strings.TrimSpace(ingressCfg.Kubeconfig)
			inCluster := ingressCfg.InCluster
			if cfg.Container != nil && cfg.Container.Kubernetes != nil {
				if kubeconfig == "" {
					kubeconfig = strings.TrimSpace(cfg.Container.Kubernetes.Kubeconfig)
				}
				if !inCluster {
					inCluster = cfg.Container.Kubernetes.InCluster
				}
			}

			reg, err := route.NewK8sIngressRegistry(route.K8sIngressRegistryConfig{
				Namespace:        namespace,
				Kubeconfig:       kubeconfig,
				InCluster:        inCluster,
				IngressClassName: ingressCfg.IngressClassName,
				Host:             ingressCfg.Host,
				PathType:         ingressCfg.PathType,
				Annotations:      ingressCfg.Annotations,
			})
			if err != nil {
				return nil, err
			}
			return reg, nil
		default:
			return nil, fmt.Errorf("unsupported route registry: %s", cfg.Route.Registry)
		}
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

	must(container.Provide(dag.NewNodeDispatcher))

	must(container.Provide(service.NewDagOrchestrator))
	must(container.Provide(service.NewNodeOrchestrator))
	must(container.Provide(service.NewDynamicDagOrchestratorV2))
	must(container.Provide(service.NewDataflowDagOrchestratorV3))
	must(container.Provide(service.NewWorkflowService))
	must(container.Provide(service.NewContainerService))
	must(container.Provide(service.NewLLMService))
	must(container.Provide(service.NewAISummaryService))
	must(container.Provide(service.NewSheetFileService))
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
	must(container.Provide(handler.NewSettingHandler))
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

	// Startup DAG recovery
	must(container.Invoke(func(cfg *config.Config, orchestrator interfaces.DagOrchestrator) {
		enabled := true
		if cfg != nil && cfg.Container != nil {
			enabled = cfg.Container.RecoverRunningDagOnStart
		}
		if !enabled {
			logger.Infof(context.Background(), "[Container] startup running DAG recovery disabled by config")
			return
		}

		recovered, err := orchestrator.RecoverRunningAnalyses(context.Background())
		if err != nil {
			logger.Warnf(context.Background(), "[Container] startup running DAG recovery failed: %v", err)
		} else {
			logger.Infof(context.Background(), "[Container] startup running DAG recovery completed, recovered=%d", recovered)
		}

		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				recovered, err := orchestrator.RecoverRunningAnalyses(context.Background())
				if err != nil {
					logger.Warnf(context.Background(), "[Container] periodic running DAG recovery failed: %v", err)
					continue
				}
				if recovered > 0 {
					logger.Infof(context.Background(), "[Container] periodic running DAG recovery completed, recovered=%d", recovered)
				}
			}
		}()
	}))
	must(container.Invoke(manager.RunOutboxDispatcher))

	// The worker is subscribed to the event bus via dig.Group("event_handlers")
	// and acts as a pure consumer/executor for outbox request events.

	return container
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
	db, err := gorm.Open(dialector, &gorm.Config{
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
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
		&types.Literature{},
		&types.ProjectLiterature{},
		&types.Dataset{},
		&types.ProjectDataset{},
		&types.File{},
		&types.DatasetFile{},
		&types.Sample{},
		&types.SampleFile{},
		&types.DatasetSample{},
		&types.Store{},
		&types.Script{},
		&types.Workflow{},
		&types.Analysis{},
		&types.AnalysisNode{},
		&types.AnalysisEdge{},
		&types.AuthToken{},
		&types.ContainerImage{},
		&types.ContainerTemplate{},
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
		&agent.AgentEvent{},
		&agent.Conversation{},
		&agent.ConversationMessage{},
		&agent.Memory{},
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
