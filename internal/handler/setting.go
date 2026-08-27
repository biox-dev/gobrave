package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/biox-dev/gobrave/internal/config"
)

type SettingHandler struct {
	cfg *config.Config
}

func NewSettingHandler(cfg *config.Config) *SettingHandler {
	return &SettingHandler{cfg: cfg}
}

type getSettingResponse struct {
	ContainerRuntime string `json:"container_runtime"`
}

// GetSetting godoc
// @Summary      获取系统设置
// @Description  返回当前系统配置的容器运行时
// @Tags         系统设置
// @Produce      json
// @Success      200  {object}  getSettingResponse
// @Security     Bearer
// @Router       /setting/get-setting [get]
func (h *SettingHandler) GetSetting(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	c.JSON(http.StatusOK, getSettingResponse{
		ContainerRuntime: config.ResolveContainerRuntime(h.cfg),
	})
}
