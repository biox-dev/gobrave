package handler

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// publishStoreRemoteRequest 是「发布到远程」的入参。
//
// StoreID 是 store 表主键（int64，兼容 JSON 字符串与数字两种写法）。
// URL 是目标远程仓库地址（github / gitee / gitlab ...），会被写成 store 裸仓库的
// 一个 git remote（按主机名命名，如 github / gitee），不再写入数据库。
type publishStoreRemoteRequest struct {
	StoreID int64ID `json:"store_id"`
	URL     string  `json:"url"`
}

// PublishStoreRemote 把 store 发布到远程仓库（github / gitee 等，可配置多个）。
//
// 当前阶段做三件事：
//   - 校验 store_id / url（url 复用下载侧的 git 地址校验，必须是 <owner>/<repo> 形态）；
//   - 打开 store 裸仓库，把 url 写成 remote（按主机名命名 github / gitee / gitlab ...，
//     同名冲突时追加序号），同一个 url 已配置过则跳过添加；
//   - 返回仓库当前的 remote 列表，便于前端刷新展示。
//
// 之所以写 git remote 而不是数据库列：地址只有一份真实来源（仓库配置），
// 数据库副本极易与实际仓库状态漂移；store 表已删除 url 列。
//
// 真正的远程发布（push 本地分支到这些 remote）待实现，因此响应里的 published 目前恒为 false；
// 「url 已存在则跳过添加、直接 push」的分支也已经就位（added=false），只等 push 落地。
func (h *StoreHandler) PublishStoreRemote(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req publishStoreRemoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.StoreID == 0 {
		c.Error(errors.NewValidationError("store_id is required"))
		return
	}

	remoteURL := strings.TrimSpace(req.URL)
	if remoteURL == "" {
		c.Error(errors.NewValidationError("url is required"))
		return
	}
	// 这里只用它做「地址形态必须是 <owner>/<repo>」的校验，不参与 store 目录命名。
	if _, err := repoPathFromGitURL(remoteURL); err != nil {
		c.Error(errors.NewValidationError("invalid git url").WithDetails(err.Error()))
		return
	}

	storeID := int64(req.StoreID)
	store, err := h.storeService.GetStoreByID(c.Request.Context(), storeID)
	if err != nil {
		handleDataError(c, err, "failed to get store")
		return
	}

	// store 目录是一个裸仓库，remote 配置就写在仓库自身的 config 里。
	storeDir := resolveStoreDir(h.cfg, store)
	if storeDir == "" {
		c.Error(errors.NewInternalServerError("store path is not configured"))
		return
	}
	if _, statErr := os.Stat(storeDir); statErr != nil {
		if os.IsNotExist(statErr) {
			// 还没有发布过（目录/裸仓库不存在），先发布组件再配置远程。
			c.Error(errors.NewConflictError("store git repository not found, publish the component first"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to inspect store directory").WithDetails(statErr.Error()))
		return
	}

	remote, added, err := utils.EnsureGitRemote(storeDir, remoteURL)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to configure store remote").WithDetails(err.Error()))
		return
	}

	// TODO(remote-push): 对仓库上配置的 remote（或本次的 remote）执行 push，
	// 把 store 裸仓库的 HEAD 推到远端；失败时返回 500 并把错误写回 store.log。
	// added=false 表示该 url 已经配置过，本次跳过添加、直接进入 push 流程。

	message := "remote already configured, skipping add"
	if added {
		message = "remote added to store repository"
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      message,
		"store_id":     strconv.FormatInt(storeID, 10),
		"url":          remoteURL,
		"remote":       remote.Name,
		"remote_added": added,
		"remotes":      utils.ReadGitRemotes(storeDir),
		"published":    false,
	})
}
