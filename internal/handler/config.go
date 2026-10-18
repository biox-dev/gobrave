package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/gin-gonic/gin"
)

// ConfigHandler 提供 config.yml 的可视化读写能力。
//
// 接口是「配置段无关」的：GET 返回所有已注册配置段的当前生效值用于表单回填，
// POST 按 section 名写入。新增配置段只需在 config 包注册，无需新增接口。
type ConfigHandler struct {
	cfg *config.Config
}

func NewConfigHandler(cfg *config.Config) *ConfigHandler {
	return &ConfigHandler{cfg: cfg}
}

type configFileResponse struct {
	// ConfigPath 是 config.yml 的绝对路径。
	ConfigPath string `json:"config_path"`
	// ConfigExists 表示 config.yml 是否已经存在，false 时 sections 里是代码中的默认配置。
	ConfigExists bool `json:"config_exists"`
	// Sections 以配置段 key 为索引，返回当前生效的配置段值。
	Sections map[string]any `json:"sections"`
}

type updateConfigSectionRequest struct {
	// Section 是配置段 key（config.yml 中的顶层键名）。
	Section string `json:"section" binding:"required"`
	// Value 是待保存的配置段内容，结构由各配置段自行定义。
	Value json.RawMessage `json:"value" binding:"required"`
}

type updateConfigSectionResponse struct {
	ConfigPath string `json:"config_path"`
	Section    string `json:"section"`
	// Value 是落盘并归一化后的配置段内容，前端可用它刷新表单。
	Value any `json:"value"`
}

// modelProviderOptionsResponse 是模型厂商 / 模型选项接口的响应。
//
// 前端据此渲染「厂商下拉框 → 模型下拉框 → API Key 输入框」，选择厂商与模型后
// 其余参数（base_url / 上下文窗口 / 最大输出 / 价格等）由后端预置补齐。
type modelProviderOptionsResponse struct {
	// Vendors 是内置厂商目录（按 UI 展示顺序）。
	Vendors []config.ModelVendorPreset `json:"vendors"`
	// Defaults 是预置模型的能力元信息（key 为模型名，值为不含凭据的默认配置）。
	// 前端不需要它也能工作：保存时后端会按厂商 + 模型自动补齐。
	Defaults map[string]config.ModelProviderConfig `json:"defaults"`
	// Providers 是当前生效的模型配置表（已按预置补齐，含已保存的 api_key）。
	Providers map[string]config.ModelProviderConfig `json:"providers"`
}

// GetModelProviderOptions godoc
// @Summary      获取模型厂商与模型选项
// @Description  返回内置模型厂商目录（含默认 base_url 与常见模型）、预置模型参数，以及当前生效的 agent.providers
// @Tags         系统配置
// @Produce      json
// @Success      200  {object}  modelProviderOptionsResponse
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /config/model/providers [get]
func (h *ConfigHandler) GetModelProviderOptions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	vendors := config.ModelVendors()

	// 预置模型能力元信息：把厂商目录里声明的常见模型逐个展开成默认配置。
	defaults := make(map[string]config.ModelProviderConfig)
	for _, vendor := range vendors {
		for _, model := range vendor.Models {
			key := strings.ToLower(strings.TrimSpace(model))
			if key == "" {
				continue
			}
			defaults[key] = config.DefaultModelProviderConfig(vendor.Type, key)
		}
	}

	providers := map[string]config.ModelProviderConfig{}
	if h.cfg != nil && h.cfg.Agent != nil {
		providers = h.cfg.Agent.Providers
	}

	c.JSON(http.StatusOK, modelProviderOptionsResponse{
		Vendors:   vendors,
		Defaults:  defaults,
		Providers: providers,
	})
}

// GetConfigFile godoc
// @Summary      获取 config.yml 配置
// @Description  返回 config.yml 的路径、文件是否存在，以及所有可编辑配置段的当前生效值（用于表单回填）
// @Tags         系统配置
// @Produce      json
// @Success      200  {object}  configFileResponse
// @Failure      401  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /config/file/get [get]
func (h *ConfigHandler) GetConfigFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	doc, err := config.LoadConfigFile()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to load config file").WithDetails(err.Error()))
		return
	}

	// config.yml 缺失时 h.cfg 里就是代码中的默认配置。
	sections := make(map[string]any, len(config.RegisteredSections()))
	for _, section := range config.RegisteredSections() {
		sections[section.Key] = section.Effective(h.cfg)
	}

	c.JSON(http.StatusOK, configFileResponse{
		ConfigPath:   doc.Path,
		ConfigExists: doc.Exists,
		Sections:     sections,
	})
}

// UpdateConfigSection godoc
// @Summary      更新一个配置段
// @Description  按 section 把配置写入 config.yml（文件不存在则创建），其余配置段保持不变
// @Tags         系统配置
// @Accept       json
// @Produce      json
// @Param        request  body      updateConfigSectionRequest  true  "配置段内容"
// @Success      200      {object}  updateConfigSectionResponse
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /config/file/section/update [post]
func (h *ConfigHandler) UpdateConfigSection(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req updateConfigSectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	section, ok := config.FindSection(req.Section)
	if !ok {
		c.Error(errors.NewValidationError("unsupported config section").WithDetails(req.Section))
		return
	}

	// 先解析校验，再落盘，最后才更新内存配置，保证内存与文件不会出现不一致。
	value, err := section.Decode(req.Value)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	path, err := config.SaveConfigSection(section.Key, value)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to save config file").WithDetails(err.Error()))
		return
	}

	if err := section.Apply(h.cfg, value); err != nil {
		c.Error(errors.NewInternalServerError("failed to apply config section").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, updateConfigSectionResponse{
		ConfigPath: path,
		Section:    section.Key,
		Value:      section.Effective(h.cfg),
	})
}
