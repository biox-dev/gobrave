package handler

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/container_runtime/provision"
	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/gin-gonic/gin"
)

// provisionState 保存安装向导的进行中状态与最近一次结果。
//
// 下载 / 安装是耗时操作，接口返回 202 后由前端轮询 status 读取进度。
// 这里刻意用进程内状态而不是 SSE：安装是一次性的低频操作，
// 轮询的复杂度远低于维护长连接，也避免刷新页面就丢进度的问题。
type provisionState struct {
	mu       sync.RWMutex
	running  bool
	stage    string
	percent  int
	message  string
	lastErr  string
	finished bool
}

func (s *provisionState) setProgress(p provision.Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stage = p.Stage
	s.percent = p.Percent
	s.message = p.Message
}

func (s *provisionState) start() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	s.finished = false
	s.lastErr = ""
	s.stage = ""
	s.percent = 0
	s.message = ""
	return true
}

func (s *provisionState) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.finished = true
	if err != nil {
		s.lastErr = err.Error()
		s.message = err.Error()
		s.percent = 0
	}
}

func (s *provisionState) snapshot() gin.H {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return gin.H{
		"running":  s.running,
		"finished": s.finished,
		"stage":    s.stage,
		"percent":  s.percent,
		"message":  s.message,
		"error":    s.lastErr,
	}
}

// provisionState 是全局单例：安装向导同一时刻只允许一个操作在跑。
var globalProvisionState = &provisionState{}

// GetRuntimeProvisionStatus godoc
// @Summary      容器运行时安装状态
// @Description  返回自管容器运行时的安装/运行状态（Windows 上是 WSL 发行版，Linux 上是本机 dockerd）。
// @Description  未启用预置时 state 恒为 not_installed 并给出 detail 说明。
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  provision.RuntimeStatus
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/provision/status [get]
func (h *ContainerHandler) GetRuntimeProvisionStatus(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	mgr := h.provisionManager
	if mgr == nil || mgr.Inner() == nil {
		c.JSON(http.StatusOK, provision.RuntimeStatus{
			State:  provision.StateNotInstalled,
			Detail: "container runtime provisioning is disabled (set container.docker.provision.enabled)",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), runtimeHealthCheckTimeout)
	defer cancel()

	status, err := mgr.Inner().Status(ctx)
	if err != nil {
		c.Error(apperrors.NewInternalServerError("failed to get runtime status").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   status,
		"progress": globalProvisionState.snapshot(),
	})
}

// DownloadRuntimeProvision godoc
// @Summary      下载容器运行时包
// @Description  异步下载 rootfs 包（支持断点续传），进度通过 status 接口轮询。
// @Tags         容器管理
// @Produce      json
// @Success      202  {object}  map[string]string
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/provision/download [post]
func (h *ContainerHandler) DownloadRuntimeProvision(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	inner := h.provisionInner()
	if inner == nil {
		c.Error(apperrors.NewBadRequestError("container runtime provisioning is disabled"))
		return
	}
	if !globalProvisionState.start() {
		c.Error(apperrors.NewBadRequestError("another provisioning operation is running"))
		return
	}

	go func() {
		err := inner.Download(context.WithoutCancel(c.Request.Context()), globalProvisionState.setProgress)
		globalProvisionState.finish(err) // 安装成功后立即刷新 Manager 缓存的 endpoint：DockerRuntime 在启动时
		// 已经用旧地址（或空）创建过 client，这里让后续 status 能拿到真实地址，
		// 由前端引导用户重启应用或触发 start 使 runtime 使用新地址。
		if err == nil {
			_, _ = h.provisionManager.Start(context.WithoutCancel(c.Request.Context()))
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

// InstallRuntimeProvision godoc
// @Summary      安装容器运行时
// @Description  异步导入发行版并启动 dockerd（要求已下载），进度通过 status 接口轮询。
// @Tags         容器管理
// @Produce      json
// @Success      202  {object}  map[string]string
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/provision/install [post]
func (h *ContainerHandler) InstallRuntimeProvision(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	inner := h.provisionInner()
	if inner == nil {
		c.Error(apperrors.NewBadRequestError("container runtime provisioning is disabled"))
		return
	}
	if !globalProvisionState.start() {
		c.Error(apperrors.NewBadRequestError("another provisioning operation is running"))
		return
	}

	go func() {
		err := inner.Install(context.WithoutCancel(c.Request.Context()), globalProvisionState.setProgress)
		globalProvisionState.finish(err)
	}()

	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

type uninstallProvisionRequest struct {
	// Confirm 必须为 true：卸载会删除发行版与其中的所有容器 / 镜像。
	Confirm bool `json:"confirm"`
}

// UninstallRuntimeProvision godoc
// @Summary      卸载容器运行时
// @Description  注销发行版并删除数据目录（同步执行）。必须显式传 confirm=true。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]bool
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/provision/uninstall [post]
func (h *ContainerHandler) UninstallRuntimeProvision(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	inner := h.provisionInner()
	if inner == nil {
		c.Error(apperrors.NewBadRequestError("container runtime provisioning is disabled"))
		return
	}

	var req uninstallProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if !req.Confirm {
		c.Error(apperrors.NewBadRequestError("uninstall requires confirm=true"))
		return
	}
	if !globalProvisionState.start() {
		c.Error(apperrors.NewBadRequestError("another provisioning operation is running"))
		return
	}

	err := inner.Uninstall(c.Request.Context())
	globalProvisionState.finish(err)
	if err != nil {
		if errors.Is(err, provision.ErrUnsupported) {
			c.Error(apperrors.NewBadRequestError("uninstall is not supported on this platform"))
			return
		}
		c.Error(apperrors.NewInternalServerError("failed to uninstall container runtime").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"uninstalled": true})
}

// StartRuntimeProvision godoc
// @Summary      启动容器运行时
// @Description  手动触发一次启动自检（与进程启动时的行为一致）：已安装则确保 daemon 运行并返回 endpoint，
// @Description  未安装返回 409，由前端引导至安装向导。
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  map[string]string
// @Failure      401  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/provision/start [post]
func (h *ContainerHandler) StartRuntimeProvision(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	if h.provisionManager == nil {
		c.Error(apperrors.NewBadRequestError("container runtime provisioning is disabled"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), runtimeStartTimeout)
	defer cancel()

	endpoint, err := h.provisionManager.Start(ctx)
	if err != nil {
		if errors.Is(err, provision.ErrNotInstalled) {
			c.JSON(http.StatusConflict, gin.H{
				"installed": false,
				"error":     "container runtime is not installed",
			})
			return
		}
		c.Error(apperrors.NewInternalServerError("failed to start container runtime").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"installed": true, "endpoint": endpoint})
}

// runtimeStartTimeout 是手动启动接口的超时上限；dockerd 冷启动可能较慢。
const runtimeStartTimeout = 60 * time.Second

// provisionInner 返回底层 Provisioner；未启用时返回 nil。
func (h *ContainerHandler) provisionInner() provision.Provisioner {
	if h.provisionManager == nil {
		return nil
	}
	return h.provisionManager.Inner()
}
