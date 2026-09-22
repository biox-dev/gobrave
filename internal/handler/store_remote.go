package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/gin-gonic/gin"
)

// publishStoreRemoteRequest 是「发布到远程」的入参。
//
// StoreID 是 store 表主键（int64，兼容 JSON 字符串与数字两种写法）。
// URL 是目标远程仓库地址（github / gitee），当前阶段只落到 store.url，
// 真正的 push 逻辑后续在这里补。
type publishStoreRemoteRequest struct {
	StoreID int64ID `json:"store_id"`
	URL     string  `json:"url"`
}

// PublishStoreRemote 把本地 store 发布到远程仓库（github / gitee）。
//
// 当前阶段只做两件事：
//   - 校验 store_id / url（url 复用下载侧的 git 地址校验，必须是 <owner>/<repo> 形态）；
//   - 把 url 单列写回 store.url。
//
// 之所以走 UpdateStoreURL 而不是 UpdateStore：后者用 map 调 Updates，未提交的字段
// 会被一并写成零值（见 repository 注释）。
//
// 真正的远程发布（打开 store 裸仓库、配置 remote、push 分支）待实现，
// 因此响应里的 published 目前恒为 false。
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
	if _, err := buildStorePathNameFromGitURL(remoteURL); err != nil {
		c.Error(errors.NewValidationError("invalid git url").WithDetails(err.Error()))
		return
	}

	storeID := int64(req.StoreID)
	if err := h.storeService.UpdateStoreURL(c.Request.Context(), storeID, remoteURL); err != nil {
		handleDataError(c, err, "failed to update store url")
		return
	}

	// TODO(remote-publish): 打开 store 裸仓库（utils.GetWorkflowOrScriptStoreDir + git.PlainOpen），
	// 把 remoteURL 写成 remote（按 host 命名，如 github / gitee），再 push 本地分支到远端；
	// 失败时把错误写回 store.status / store.log，并返回 500。
	c.JSON(http.StatusOK, gin.H{
		"message":   "store url updated",
		"store_id":  strconv.FormatInt(storeID, 10),
		"url":       remoteURL,
		"published": false,
	})
}
