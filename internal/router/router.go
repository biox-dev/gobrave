package router

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/biox-dev/gobrave/docs" // IGNORE
	frontendassets "github.com/biox-dev/gobrave/frontend"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/handler"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/dig"
)

type RouterParams struct {
	dig.In
	Config           *config.Config
	UserService      interfaces.UserService
	AuthHandler      *handler.AuthHandler
	ProjectHandler   *handler.ProjectHandler
	DataHandler      *handler.DataHandler
	StoreHandler     *handler.StoreHandler
	ContainerHandler *handler.ContainerHandler
	AnalysisHandler  *handler.AnalysisHandler
	WorkflowHandler  *handler.WorkflowHandler
	SettingHandler   *handler.SettingHandler
	SheetHandler     *handler.SheetHandler
	UploadHandler    *handler.UploadHandler
	FileHandler      *handler.FileHandler
	ProxyHandler     *handler.ProxyHandler
	RealtimeHandler  *handler.RealtimeHandler
	LLMHandler       *handler.LLMHandler
	AISummaryHandler *handler.AISummaryHandler
	AgentHandler     *handler.AgentHandler
}

func NewRouter(params RouterParams) *gin.Engine {
	r := gin.New()
	r.ContextWithFallback = true
	// CORS 中间件应放在最前面
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-API-Key", "X-Request-ID"},
		ExposeHeaders:    []string{"Content-Length", "Access-Control-Allow-Origin"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// 基础中间件（不需要认证）
	r.Use(middleware.RequestID())
	// r.Use(middleware.Language())
	// r.Use(middleware.Logger())
	r.Use(middleware.Recovery())
	r.Use(middleware.ErrorHandler())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Swagger API 文档（仅在非生产环境下启用）
	// 通过 GIN_MODE 环境变量判断：release 模式下禁用 Swagger
	if gin.Mode() != gin.ReleaseMode {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler,
			ginSwagger.DefaultModelsExpandDepth(-1), // 默认折叠 Models
			ginSwagger.DocExpansion("list"),         // 展开模式: "list"(展开标签), "full"(全部展开), "none"(全部折叠)
			ginSwagger.DeepLinking(true),            // 启用深度链接
			ginSwagger.PersistAuthorization(true),   // 持久化认证信息
		))
	}
	analysisAppsPrefix := config.ResolveAppsPathPrefix(params.Config)
	serveFrontendStatic(r, params.Config)
	handler.RegisterOnlyOfficeRoutes(r, params.ProxyHandler)

	r.Use(middleware.Auth(params.UserService, params.Config))
	handler.RegisterProjectDocsRoute(r, params.Config)

	serveStatic(r, params.Config)
	v1 := r.Group("/api/v1")
	{
		RegisterAuthRoutes(v1, params.AuthHandler)
		RegisterProjectRoutes(v1, params.ProjectHandler, params.UploadHandler)
		RegisterDataRoutes(v1, params.DataHandler)
		RegisterStoreRoutes(v1, params.StoreHandler)
		RegisterContainerRoutes(v1, params.ContainerHandler)
		RegisterAnalysisRoutes(v1, params.AnalysisHandler)
		RegisterWorkflowRoutes(v1, params.WorkflowHandler)
		RegisterSettingRoutes(v1, params.SettingHandler)
		RegisterSheetRoutes(v1, params.SheetHandler)
		RegisterFileRoutes(v1, params.FileHandler)
		RegisterRealtimeRoutes(v1, params.RealtimeHandler)
		RegisterLLMRoutes(v1, params.LLMHandler)
		RegisterAISummaryRoutes(v1, params.AISummaryHandler)
		RegisterAgentRoutes(v1, params.AgentHandler)
	}

	// r.Any("/brave-api", params.ProxyHandler.BraveAPIProxy)
	// r.Any("/brave-api/*proxyPath", params.ProxyHandler.BraveAPIProxy)
	// r.Any("/container", params.ProxyHandler.ContainerProxy)
	// r.Any("/container/*proxyPath", params.ProxyHandler.ContainerProxy)

	// r.Any("/apps", params.ProxyHandler.ContainerProxy)
	// r.Any("/apps/*proxyPath", params.ProxyHandler.ContainerProxy)
	r.Any(analysisAppsPrefix, params.ProxyHandler.AppSessionProxy)
	r.Any(analysisAppsPrefix+"/*proxyPath", params.ProxyHandler.AppSessionProxy)
	// if params.Config.Route.Registry == "gateway" {
	// 	r.Any(analysisAppsPrefix, params.ProxyHandler.AppSessionProxy)
	// 	r.Any(analysisAppsPrefix+"/*proxyPath", params.ProxyHandler.AppSessionProxy)
	// } else {
	// 	r.Any(analysisAppsPrefix, params.ProxyHandler.ContainerProxy)
	// 	r.Any(analysisAppsPrefix+"/*proxyPath", params.ProxyHandler.ContainerProxy)
	// }

	// r.Any("/apps", params.ProxyHandler.AppSessionProxy)
	// r.Any("/apps/*proxyPath", params.ProxyHandler.AppSessionProxy)

	// r.NoRoute(params.ProxyHandler.FallbackProxy)
	return r
}

func RegisterAuthRoutes(r *gin.RouterGroup, handler *handler.AuthHandler) {
	r.POST("/auth/register", handler.Register)
	r.POST("/auth/login", handler.Login)
	// r.POST("/auth/auto-setup", handler.AutoSetup)
	// r.GET("/auth/oidc/config", handler.GetOIDCConfig)
	// r.GET("/auth/oidc/url", handler.GetOIDCAuthorizationURL)
	// r.GET("/auth/oidc/callback", handler.OIDCRedirectCallback)
	r.POST("/auth/refresh", handler.RefreshToken)
	// r.GET("/auth/validate", handler.ValidateToken)
	r.POST("/auth/logout", handler.Logout)
	r.GET("/auth/me", handler.GetCurrentUser)
	r.POST("/auth/profile", handler.UpdateProfile)
	// r.POST("/auth/change-password", handler.ChangePassword)
}

func RegisterProjectRoutes(r *gin.RouterGroup, handler *handler.ProjectHandler, uploadHandler *handler.UploadHandler) {
	r.GET("/project/list-project", handler.ListProject)
	r.GET("/project/active-project", handler.GetActiveProject)
	r.POST("/project/create-project", handler.CreateProject)
	r.POST("/project/add-user-project", handler.AddUserProject)
	r.POST("/project/update-project-sharing", handler.UpdateProjectSharing)
	r.POST("/project/add-project-to-user", handler.AddProjectToUser)
	r.POST("/project/delete-user-project", handler.DeleteUserProject)
	r.POST("/project/activate-project", handler.ActivateProject)
	r.POST("/project/add-project-report", handler.AddProjectReport)
	r.POST("/project/update-project-report", handler.UpdateProjectReport)
	r.POST("/project/delete-project-report", handler.DeleteProjectReport)
	r.GET("/project/list-project-report", handler.ListProjectReport)
	r.POST("/project/list-project-report-page", handler.PageProjectReport)
	r.GET("/project/project-report-detail", handler.GetProjectReportDetail)
	r.POST("/project/upload-image", uploadHandler.UploadImage)

	r.POST("/project/add-literature", handler.AddLiterature)
	r.POST("/project/update-literature", handler.UpdateLiterature)
	r.POST("/project/delete-literature", handler.DeleteLiterature)
	r.GET("/project/literature-detail", handler.GetLiteratureDetail)
	r.GET("/project/list-literature", handler.ListLiterature)
	r.POST("/project/list-literature-page", handler.PageLiterature)
	r.POST("/project/bind-literature", handler.BindLiterature)
	r.POST("/project/unbind-literature", handler.UnbindLiterature)
	r.POST("/project/list-literature-pool-page", handler.PageLiteraturePool)
}

func RegisterSheetRoutes(r *gin.RouterGroup, handler *handler.SheetHandler) {
	r.GET("/sheet/workbook", handler.ReadWorkbook)
	r.POST("/sheet/workbook/save", handler.WriteWorkbook)
	r.GET("/sheet/workbook/by-file-id", handler.ReadWorkbookByFileID)
	r.POST("/sheet/workbook/save/by-file-id", handler.WriteWorkbookByFileID)
}

func RegisterFileRoutes(r *gin.RouterGroup, handler *handler.FileHandler) {
	r.GET("/file/list-project-dir", handler.ListProjectDir)
	r.GET("/file/search", handler.Search)
	r.GET("/file/download", handler.Download)
	r.POST("/file/upload", handler.Upload)
	r.POST("/file/create-dir", handler.CreateDir)
	r.POST("/file/create-file", handler.CreateFile)
	r.POST("/file/move", handler.Move)
	r.POST("/file/delete", handler.Delete)
}

func RegisterDataRoutes(r *gin.RouterGroup, handler *handler.DataHandler) {
	r.POST("/data/dataset/create", handler.CreateDataset)
	r.GET("/data/dataset/get", handler.GetDataset)
	r.POST("/data/dataset/update", handler.UpdateDataset)
	r.POST("/data/dataset/delete", handler.DeleteDataset)
	r.GET("/data/dataset/list", handler.ListDataset)
	r.POST("/data/dataset/list-by-project-page", handler.PageDatasetByProjectID)

	r.POST("/data/project-dataset/create", handler.CreateProjectDataset)
	r.GET("/data/project-dataset/get", handler.GetProjectDataset)
	r.POST("/data/project-dataset/update", handler.UpdateProjectDataset)
	r.POST("/data/project-dataset/delete", handler.DeleteProjectDataset)
	r.GET("/data/project-dataset/list", handler.ListProjectDataset)

	r.POST("/data/file/create", handler.CreateFile)
	r.GET("/data/file/get", handler.GetFile)
	r.POST("/data/file/update", handler.UpdateFile)
	r.POST("/data/file/delete", handler.DeleteFile)
	r.GET("/data/file/list", handler.ListFile)
	r.GET("/data/file/list-by-project", handler.ListFileByProjectID)
	r.POST("/data/file/list-by-project-page", handler.PageFileByProjectID)
	r.GET("/data/file/list-by-project-group", handler.ListFileByProjectIDGroupByRole)

	r.POST("/data/dataset-file/create", handler.CreateDatasetFile)
	r.POST("/data/dataset-file/add-file", handler.AddFileToDataset)
	r.GET("/data/dataset-file/get", handler.GetDatasetFile)
	r.POST("/data/dataset-file/update", handler.UpdateDatasetFile)
	r.POST("/data/dataset-file/delete", handler.DeleteDatasetFile)
	r.GET("/data/dataset-file/list", handler.ListDatasetFile)

	r.POST("/data/sample/create", handler.CreateSample)
	r.GET("/data/sample/get", handler.GetSample)
	r.POST("/data/sample/update", handler.UpdateSample)
	r.POST("/data/sample/delete", handler.DeleteSample)
	r.GET("/data/sample/list", handler.ListSample)
	r.GET("/data/sample/list-by-project", handler.ListSampleByProjectID)
	r.POST("/data/sample/list-by-project-page", handler.PageSampleByProjectID)

	r.POST("/data/sample-file/create", handler.CreateSampleFile)
	r.GET("/data/sample-file/get", handler.GetSampleFile)
	r.POST("/data/sample-file/update", handler.UpdateSampleFile)
	r.POST("/data/sample-file/delete", handler.DeleteSampleFile)
	r.GET("/data/sample-file/list", handler.ListSampleFile)

	r.POST("/data/dataset-sample/create", handler.CreateDatasetSample)
	r.GET("/data/dataset-sample/get", handler.GetDatasetSample)
	r.POST("/data/dataset-sample/update", handler.UpdateDatasetSample)
	r.POST("/data/dataset-sample/delete", handler.DeleteDatasetSample)
	r.GET("/data/dataset-sample/list", handler.ListDatasetSample)
}

func RegisterContainerRoutes(r *gin.RouterGroup, handler *handler.ContainerHandler) {
	r.POST("/container/image/create", handler.CreateContainerImage)
	r.GET("/container/image/get", handler.GetContainerImage)
	r.POST("/container/image/update", handler.UpdateContainerImage)
	r.POST("/container/image/delete", handler.DeleteContainerImage)
	r.GET("/container/image/list", handler.ListContainerImage)
	r.POST("/container/image/list-by-page", handler.PageContainerImage)

	r.POST("/container/template/create", handler.CreateContainerTemplate)
	r.GET("/container/template/get", handler.GetContainerTemplate)
	r.POST("/container/template/update", handler.UpdateContainerTemplate)
	r.POST("/container/template/delete", handler.DeleteContainerTemplate)
	r.GET("/container/template/list", handler.ListContainerTemplate)
	r.POST("/container/template/list-by-page", handler.PageContainerTemplate)
	r.POST("/container/template/export", handler.ExportContainerTemplate)
	r.POST("/container/template/import", handler.ImportContainerTemplate)

	r.POST("/container/app-session/create", handler.CreateAppSession)
	r.POST("/container/app-session/create-by-analysis-node", handler.CreateAppSessionByAnalysisNode)
	r.POST("/container/app-session/start", handler.StartAppSession)
	r.POST("/container/app-session/stop", handler.StopAppSession)
	r.POST("/container/app-session/delete", handler.DeleteAppSession)
	r.GET("/container/app-session/get", handler.GetAppSession)
	r.GET("/container/app-session/list", handler.ListAppSession)
	r.POST("/container/app-session/list-by-page", handler.PageAppSession)
	r.POST("/container/app-session/recreate-container", handler.RecreateAppSessionContainer)

	r.POST("/container/instance/list-by-page", handler.PageContainerInstance)
	// r.POST("/container/event/list-by-page", handler.PageContainerEvent)
	r.POST("/container/outbox/list-by-page", handler.PageOutboxEvent)
	r.GET("/container/runtime/monitoring/list", handler.ListRuntimeMonitoringSnapshot)
	r.GET("/container/queue/status", handler.GetQueueStatus)
}

func RegisterStoreRoutes(r *gin.RouterGroup, handler *handler.StoreHandler) {
	r.POST("/store/create", handler.CreateStore)
	r.POST("/store/download", handler.DownloadStore)
	r.POST("/store/redownload", handler.ReDownloadStore)
	r.GET("/store/get", handler.GetStore)
	r.GET("/store/get-by-store-id", handler.GetStoreByStoreID)
	r.POST("/store/update", handler.UpdateStore)
	r.POST("/store/delete", handler.DeleteStore)
	r.GET("/store/list", handler.ListStore)
	r.POST("/store/list-by-page", handler.PageStore)
}

func RegisterWorkflowRoutes(r *gin.RouterGroup, handler *handler.WorkflowHandler) {
	r.POST("/workflow/save-script", handler.SaveScript)
	r.POST("/workflow/save-workflow", handler.SaveWorkflow)
	r.POST("/workflow/delete/:workflowId", handler.DeleteWorkflow)
	r.POST("/workflow/publish-workflow", handler.PublishWorkflow)
	r.POST("/workflow/publish-script", handler.PublishScript)
	r.POST("/workflow/install-workflow/:storeId", handler.InstallWorkflow)
	r.POST("/workflow/install-script/:storeId", handler.InstallScript)
	r.GET("/workflow/:workflowId/get-workflow", handler.GetWorkflowById)
	r.POST("/workflow/:workflowId/generate-workflow-json", handler.GenerateWorkflowJSON)
	r.GET("/find-script/:id", handler.FindScript)
	r.GET("/script/:scriptId/get-script", handler.GetScriptById)
	r.POST("/script/delete/:scriptId", handler.DeleteScript)
	r.POST("/workflow/page-script", handler.PageScript)
	r.POST("/workflow/page-workflow", handler.PageWorkflow)
	r.GET("/tools/get-workflow-vis/:workflowId", handler.GetWorkflowVis)
	r.GET("/workflow/tools/get-from-json/:workflowId", handler.GetFromJSONByWorlflow)
	r.GET("/workflow/:workflowId/form", handler.GetWorkflowForm)
	r.GET("/script/:scriptId/form", handler.GetScriptForm)
	r.GET("/script/:scriptId/content", handler.GetScriptContent)
}

func RegisterSettingRoutes(r *gin.RouterGroup, handler *handler.SettingHandler) {
	r.GET("/setting/get-setting", handler.GetSetting)
}

func RegisterAnalysisRoutes(r *gin.RouterGroup, handler *handler.AnalysisHandler) {
	r.POST("/workflow/publish-to-doc/:workflowId", handler.PublishToDocByWorkflowID)
	r.POST("/analysis/publish-to-doc/:analsyisId", handler.PublishToDocByAnalysisID)
	r.POST("/analysis-node/:analysisNodeId/publish-to-doc", handler.PublishToDocByAnalysisNodeID)
	r.POST("/script/:scriptId/publish-to-doc", handler.PublishScriptAnalysisNodeToDoc)
	r.POST("/project-report/:reportId/publish-to-doc", handler.PublishProjectReportToDoc)
	r.POST("/analysis/parse-params", handler.ParseParams)
	r.GET("/analysis/:analysisNodeId/node-params", handler.AnalysisNodeParams)
	r.GET("/analysis/:analysisNodeId/node-logs", handler.AnalysisNodeLogs)
	r.POST("/analysis/controller", handler.SaveAnalysisController)
	r.POST("/analysis/controllerV2", handler.SaveAnalysisControllerV2)
	r.POST("/analysis/controller-script", handler.SaveAnalysisNodeControllerWithScript)
	r.POST("/analysis/:analysisNodeId/run-node", handler.RunAnalysisNode)
	r.POST("/script/:scriptId/run-analysis-nodes", handler.RunAnalysisNodeByScriptID)
	r.POST("/analysis/controllerV3", handler.SaveAnalysisControllerV3)
	r.POST("/fast-api/analysis-controller", handler.SaveAnalysisController)
	r.POST("/fast-api/analysis-controllerV2", handler.SaveAnalysisControllerV2)
	r.POST("/fast-api/analysis-controllerV3", handler.SaveAnalysisControllerV3)
	r.POST("/analysis/stop/:analysisId", handler.StopAnalysis)
	r.POST("/analysis/node/stop/:analysisNodeId", handler.StopAnalysisNode)
	r.POST("/analysis/node/delete/:analysisNodeId", handler.DeleteAnalysisNode)
	r.POST("/analysis/delete/:analysisId", handler.DeleteAnalysis)
	r.POST("/analysis/toggle-report/:analysisId", handler.ToggleAnalysisReport)
	r.POST("/analysis/edit-params-v2/:analysisId", handler.EditParamsV2)
	r.POST("/analysis/edit-node-params/:analysisNodeId", handler.EditNodeParams)
	r.POST("/list-analysis-tree", handler.ListAnalysisTree)
	r.POST("/analysis/list-by-project-page", handler.PageAnalysisByProject)
	r.POST("/analysis/node/list-by-project-page", handler.PageAnalysisNodeByProject)
	r.GET("/analysis/visualization-node-file/:analysisNodeId", handler.VisualizationNodeFile)
	r.GET("/analysis/visualization-node-tree/:analysisId", handler.VisualizationNodeTree)
}

func RegisterRealtimeRoutes(r *gin.RouterGroup, handler *handler.RealtimeHandler) {
	r.GET("/realtime/connect", handler.Connect)
	r.GET("/realtime/ws", handler.ConnectWS)
	r.GET("/realtime/sse", handler.ConnectSSE)
	r.POST("/realtime/push", handler.Push)
	r.GET("/realtime/stats", handler.Stats)
}

func RegisterAISummaryRoutes(r *gin.RouterGroup, handler *handler.AISummaryHandler) {
	r.POST("/ai-summary/create", handler.CreateAISummary)
	r.POST("/ai-summary/regenerate", handler.RegenerateAISummary)
	r.GET("/ai-summary/get", handler.GetAISummary)
	r.GET("/ai-summary/list", handler.ListAISummary)
	r.GET("/ai-summary/input", handler.GetAISummaryInput)
	r.POST("/ai-summary/delete", handler.DeleteAISummary)
}

func RegisterLLMRoutes(r *gin.RouterGroup, handler *handler.LLMHandler) {
	r.POST("/llm/session/create", handler.CreateLLMSession)
	r.GET("/llm/session/get", handler.GetLLMSession)
	r.POST("/llm/session/update", handler.UpdateLLMSession)
	r.POST("/llm/session/delete", handler.DeleteLLMSession)
	r.GET("/llm/session/list", handler.ListLLMSession)

	r.POST("/llm/conversation/create", handler.CreateLLMConversation)
	r.GET("/llm/conversation/get", handler.GetLLMConversation)
	r.POST("/llm/conversation/update", handler.UpdateLLMConversation)
	r.POST("/llm/conversation/delete", handler.DeleteLLMConversation)
	r.GET("/llm/conversation/list", handler.ListLLMConversation)

	// r.POST("/llm/copilot-cli/chat", handler.CopilotChat)
}

func RegisterAgentRoutes(r *gin.RouterGroup, handler *handler.AgentHandler) {
	r.POST("/agent/chat", handler.Chat)
	r.POST("/agent/env/describe", handler.DescribeEnv)
	r.POST("/agent/task/create", handler.CreateTask)
	r.GET("/agent/task/get", handler.GetTask)
	r.GET("/agent/task/request", handler.GetTaskRequest)
	r.GET("/agent/task/events", handler.GetTaskEvents)
	r.GET("/agent/task/permissions", handler.GetPendingPermissions)
	r.POST("/agent/task/cancel", handler.CancelTask)
	r.POST("/agent/permission/approve", handler.ApprovePermission)
	r.POST("/agent/permission/deny", handler.DenyPermission)

	r.POST("/agent/task/page", handler.PageTasks)
	r.POST("/agent/permission/page", handler.PagePermissions)
	r.POST("/agent/event/page", handler.PageEvents)

	r.POST("/agent/conversation/page", handler.PageConversations)
	r.GET("/agent/conversation/get", handler.GetConversation)

	r.GET("/agent/skill/list", handler.ListSkills)
	r.GET("/agent/project-context", handler.ProjectContext)

	r.GET("/agent/profile/list", handler.ListProfiles)
	r.POST("/agent/profile/save", handler.SaveProfile)
	r.POST("/agent/profile/delete", handler.DeleteProfile)

	r.POST("/agent/memory/save", handler.SaveMemory)
	r.GET("/agent/memory/get", handler.GetMemory)
	r.POST("/agent/memory/delete", handler.DeleteMemory)
	r.POST("/agent/memory/page", handler.PageMemory)
	r.POST("/agent/memory/retrieve", handler.RetrieveMemory)
}

// serveStatic maps local image resources under /images.
func serveStatic(r *gin.Engine, cfg *config.Config) {
	// configuredDir := ""
	// baseDir := ""
	// if cfg != nil && cfg.Storage != nil {
	// 	configuredDir = cfg.Storage.ImageDir
	// 	baseDir = cfg.Storage.BaseDir
	// }

	// imageDir, err := utils.ResolveConfiguredPath(configuredDir, "images")
	imageDir := utils.ResolveImageDir(cfg.Storage.BaseDir)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return
	}

	logger.Infof(context.Background(), "[Router] Serving image files from %s at /images", imageDir)
	r.StaticFS("/images", http.Dir(imageDir))

	baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
	if baseDir == "" {
		return
	}

	dataDir, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, "data"))
	if err != nil {
		logger.Warnf(context.Background(), "[Router] invalid base_dir/data path: base_dir=%s err=%v", baseDir, err)
		return
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		logger.Warnf(context.Background(), "[Router]  create dir failed: data_dir=%s err=%v", dataDir, err)
		return
	}

	logger.Infof(context.Background(), "[Router] Serving data files from %s at /images-data", dataDir)
	r.StaticFS("/images-data", http.Dir(dataDir))
	logger.Infof(context.Background(), "[Router] Serving data files form %s at /data-analysis", dataDir)
	r.StaticFS("/data-analysis", http.Dir(dataDir))
	r.StaticFS("/data-project", http.Dir(dataDir))

	analysisDir, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, "analysis"))
	if err != nil {
		logger.Warnf(context.Background(), "[Router] Skip serving /images-analysis: invalid base_dir/analysis path: base_dir=%s err=%v", baseDir, err)
		return
	}
	if err := os.MkdirAll(analysisDir, 0o755); err != nil {
		logger.Warnf(context.Background(), "[Router] Skip serving /images-analysis: create dir failed: analysis_dir=%s err=%v", analysisDir, err)
		return
	}

	logger.Infof(context.Background(), "[Router] Serving analysis files from %s at /images-analysis", analysisDir)
	r.StaticFS("/images-analysis", http.Dir(analysisDir))

}

func serveFrontendStatic(r *gin.Engine, cfg *config.Config) {
	if embeddedRoot, ok := frontendassets.EmbeddedFrontendFS(); ok {
		webFS, err := fs.Sub(embeddedRoot, "web")
		if err == nil && mountFrontendStatic(r, cfg, webFS, "embedded:web") {
			return
		}
		if err != nil {
			logger.Warnf(context.Background(), "[Router] Failed to load embedded frontend subfs: %v", err)
		}
	}

	// 优先使用外部的 web 目录，不存在时回退到 frontend/web。
	for _, candidate := range []string{"web", filepath.Join("frontend", "web")} {
		absDir, err := utils.ResolveExternalPath(candidate)
		if err != nil {
			continue
		}
		if mountFrontendStatic(r, cfg, os.DirFS(absDir), absDir) {
			return
		}
	}
}

func mountFrontendStatic(r *gin.Engine, cfg *config.Config, frontendFS fs.FS, source string) bool {
	if _, err := fs.Stat(frontendFS, "index.html"); err != nil {
		return false
	}

	logger.Infof(context.Background(), "[Router] Serving frontend static files from %s", source)
	fileServer := http.FileServer(http.FS(frontendFS))
	analysisAppsPrefix := config.ResolveAppsPathPrefix(cfg)

	r.Use(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if path == "/api" ||
			strings.HasPrefix(path, "/api/") ||
			path == "/health" ||
			strings.HasPrefix(path, "/health/") ||
			path == "/swagger" ||
			strings.HasPrefix(path, "/swagger/") ||
			path == "/docs" ||
			strings.HasPrefix(path, "/docs/") ||
			path == "/audio" ||
			strings.HasPrefix(path, "/audio/") ||
			path == "/images" ||
			strings.HasPrefix(path, "/images/") ||
			path == "/images-data" ||
			strings.HasPrefix(path, "/images-data/") ||
			path == "/brave-api" ||
			strings.HasPrefix(path, "/brave-api/") ||
			path == "/container" ||
			strings.HasPrefix(path, "/container/") ||
			path == "/apps" ||
			strings.HasPrefix(path, "/apps/") ||
			path == analysisAppsPrefix ||
			strings.HasPrefix(path, analysisAppsPrefix+"/") ||
			path == "/onlyoffice" ||
			strings.HasPrefix(path, "/onlyoffice/") ||
			path == "/go-onlyoffice" ||
			strings.HasPrefix(path, "/go-onlyoffice/") {
			c.Next()
			return
		}

		requestedPath := strings.TrimPrefix(path, "/")
		if requestedPath != "" {
			if info, err := fs.Stat(frontendFS, requestedPath); err == nil && !info.IsDir() {
				setFrontendCacheHeaders(c.Writer, path)
				fileServer.ServeHTTP(c.Writer, c.Request)
				c.Abort()
				return
			}
		}

		if requestedPath == "" || strings.HasSuffix(path, "/") {
			setFrontendCacheHeaders(c.Writer, path)
			fileServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		// BrowserRouter fallback: route-like paths should return index.html,
		// but missing static assets (e.g. *.js, *.css) should stay 404.
		if ext := filepath.Ext(path); ext != "" {
			c.Next()
			return
		}

		setFrontendCacheHeaders(c.Writer, "/index.html")
		req := c.Request.Clone(c.Request.Context())
		req.URL.Path = "/index.html"
		fileServer.ServeHTTP(c.Writer, req)
		c.Abort()
	})

	return true
}

// setFrontendCacheHeaders sets Cache-Control headers for frontend static resources.
// Vite 构建产物中 /assets/* 的文件名带 hash，可长期缓存；其余（index.html、config.js、favicon 等）
// 每次都需 revalidate，避免前端升级后用户看到旧版本。
func setFrontendCacheHeaders(w http.ResponseWriter, path string) {
	if strings.HasPrefix(path, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
}
