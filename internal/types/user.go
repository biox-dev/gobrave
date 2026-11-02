package types

import (
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// MaxUserMenuTabs 是主区域标签页配置允许保存的最大数量，超出的按「最近使用」截断。
const MaxUserMenuTabs = 16

// MaxUserBottomTabs 是主区域底部面板（终端 / 日志等）允许保存的最大标签数。
const MaxUserBottomTabs = 8

// 底部面板高度的允许区间（px）。前端拖拽分隔条时会实时更新。
const (
	MinUserBottomPanelHeight = 120
	MaxUserBottomPanelHeight = 800
)

// UserMenuTab 是主区域一个标签页的状态。
//
// URL 是标签页对应的路由地址（pathname + search），前端直接 navigate(URL) 即可切换；
// Title 是该标签页最近一次登记的显示名（如分析节点名），可为空 —— 为空时前端回退到静态标题规则。
type UserMenuTab struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// UserBottomTab 是底部面板的一个标签页。
//
// Kind 决定前端用哪个渲染器（terminal / log / …），Params 是该渲染器自己的参数
// （终端为 {template_id}），因此新增底部面板类型不需要改后端表结构。
type UserBottomTab struct {
	Key    string         `json:"key"`
	Kind   string         `json:"kind"`
	Title  string         `json:"title,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// UserBottomPanel 是主区域底部面板的完整状态。
//
// Open = 面板是否展开（收起时只保留标签栏）；Active = 当前激活标签的 Key；
// Height = 展开时的高度（px）。三个状态与标签列表一起落库，刷新后可原样恢复。
type UserBottomPanel struct {
	Open   bool            `json:"open"`
	Active string          `json:"active,omitempty"`
	Height int             `json:"height,omitempty"`
	Tabs   []UserBottomTab `json:"tabs,omitempty"`
}

// UserMenuConfig 是用户的菜单配置（主区域标签页 + 底部面板），以 JSON 形式落库。
//
// 用「有序数组」而不是 `urls + titles 两张表」：顺序即标签栏顺序，标题跟着自己的 URL 走，
// 序列化 / 回填都是一次读写。
type UserMenuConfig struct {
	Tabs []UserMenuTab `json:"tabs,omitempty"`
	// Bottom 为空表示底部面板没有打开的标签（前端据此不渲染面板）。
	Bottom *UserBottomPanel `json:"bottom,omitempty"`
}

// Normalize 清洗整份菜单配置（标签页 + 底部面板）。
// 调用方（handler）在写库前调用，避免把脏数据或超长字符串落库。
func (c *UserMenuConfig) Normalize() {
	if c == nil {
		return
	}
	c.NormalizeTabs()
	c.NormalizeBottom()
}

// NormalizeTabs 清洗主区域标签页列表：去空白、去重（保留首次出现）、丢弃根路由，并按上限截断尾部。
func (c *UserMenuConfig) NormalizeTabs() {
	if c == nil {
		return
	}
	seen := make(map[string]struct{}, len(c.Tabs))
	tabs := make([]UserMenuTab, 0, len(c.Tabs))
	for _, tab := range c.Tabs {
		url := strings.TrimSpace(tab.URL)
		if url == "" || url == "/" {
			continue
		}
		if _, exists := seen[url]; exists {
			continue
		}
		seen[url] = struct{}{}
		tabs = append(tabs, UserMenuTab{
			URL:   truncateRunes(url, 512),
			Title: truncateRunes(strings.TrimSpace(tab.Title), 200),
		})
	}
	if len(tabs) > MaxUserMenuTabs {
		tabs = tabs[len(tabs)-MaxUserMenuTabs:]
	}
	c.Tabs = tabs
}

// NormalizeBottom 清洗底部面板：去空白、去重（按 Key）、丢弃缺少 Kind 的项、按上限截断尾部，
// 并保证 Active 指向一个真实存在的标签；没有标签时把 Bottom 置空，避免前端渲染空面板。
func (c *UserMenuConfig) NormalizeBottom() {
	if c == nil || c.Bottom == nil {
		return
	}
	bottom := c.Bottom
	seen := make(map[string]struct{}, len(bottom.Tabs))
	tabs := make([]UserBottomTab, 0, len(bottom.Tabs))
	for _, tab := range bottom.Tabs {
		key := strings.TrimSpace(tab.Key)
		kind := strings.TrimSpace(tab.Kind)
		if key == "" || kind == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		params := tab.Params
		if len(params) == 0 {
			params = nil
		}
		tabs = append(tabs, UserBottomTab{
			Key:    truncateRunes(key, 200),
			Kind:   truncateRunes(kind, 64),
			Title:  truncateRunes(strings.TrimSpace(tab.Title), 200),
			Params: params,
		})
	}
	if len(tabs) > MaxUserBottomTabs {
		tabs = tabs[len(tabs)-MaxUserBottomTabs:]
	}
	if len(tabs) == 0 {
		c.Bottom = nil
		return
	}

	height := bottom.Height
	if height < MinUserBottomPanelHeight {
		height = MinUserBottomPanelHeight
	}
	if height > MaxUserBottomPanelHeight {
		height = MaxUserBottomPanelHeight
	}
	bottom.Height = height

	active := strings.TrimSpace(bottom.Active)
	if active == "" {
		active = tabs[0].Key
	} else {
		found := false
		for _, tab := range tabs {
			if tab.Key == active {
				found = true
				break
			}
		}
		if !found {
			active = tabs[0].Key
		}
	}
	bottom.Active = active
	bottom.Tabs = tabs
}

// truncateRunes 按字符（不是字节）截断，避免切坏 UTF-8。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// User represents a user in the system
type User struct {
	// Unique identifier of the user
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	// Username of the user
	Username string `json:"username"   gorm:"type:varchar(100);uniqueIndex;not null"`
	// Email address of the user
	Email string `json:"email"      gorm:"type:varchar(255);uniqueIndex;not null"`
	// Hashed password of the user
	PasswordHash string `json:"-"          gorm:"type:varchar(255);not null"`
	// Avatar URL of the user
	Avatar string `json:"avatar"     gorm:"type:varchar(500)"`
	// AgentConfig 用户的 Agent 配置（Profile + 权限策略），以 JSON 形式落库。
	AgentConfig UserAgentConfig `json:"agent_config" gorm:"serializer:json"`
	// MenuConfig 用户的菜单配置：主区域标签页 + 底部面板（终端 / 日志），以 JSON 形式落库。
	MenuConfig UserMenuConfig `json:"menu_config" gorm:"serializer:json"`
	// Tenant ID that the user belongs to
	// TenantID uint64 `json:"tenant_id"  gorm:"index"`
	// Whether the user is active
	IsActive bool `json:"is_active"  gorm:"default:true"`
	// Whether the user can access all tenants (cross-tenant access)
	CanAccessAllTenants bool `json:"can_access_all_tenants" gorm:"default:false"`
	// Creation time of the user
	CreatedAt time.Time `json:"created_at"`
	// Last updated time of the user
	UpdatedAt time.Time `json:"updated_at"`
	// Deletion time of the user
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// BeforeCreate assigns a snowflake ID when the user is created without one.
func (u *User) BeforeCreate(_ *gorm.DB) error {
	if u.ID == 0 {
		u.ID = utils.GenerateID()
	}
	return nil
}

// AuthToken represents an authentication token
type AuthToken struct {
	// Unique identifier of the token
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	// User ID that owns this token
	UserID int64 `json:"user_id,string" gorm:"type:bigint;index;not null"`
	// Token value (JWT or other format)
	Token string `json:"token"      gorm:"type:text;not null"`
	// Token type (access_token, refresh_token)
	TokenType string `json:"token_type" gorm:"type:varchar(50);not null"`
	// Token expiration time
	ExpiresAt time.Time `json:"expires_at"`
	// Whether the token is revoked
	IsRevoked bool `json:"is_revoked" gorm:"default:false"`
	// Creation time of the token
	CreatedAt time.Time `json:"created_at"`
	// Last updated time of the token
	UpdatedAt time.Time `json:"updated_at"`

	// Association relationship
	User *User `json:"user,omitempty" gorm:"foreignKey:UserID"`
}

// BeforeCreate assigns a snowflake ID when the token is created without one.
func (t *AuthToken) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// LoginRequest represents a login request
type LoginRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type OIDCAuthURLResponse struct {
	Success             bool   `json:"success"`
	ProviderDisplayName string `json:"provider_display_name,omitempty"`
	AuthorizationURL    string `json:"authorization_url,omitempty"`
	State               string `json:"state,omitempty"`
}

type OIDCConfigResponse struct {
	Success             bool   `json:"success"`
	Enabled             bool   `json:"enabled"`
	ProviderDisplayName string `json:"provider_display_name,omitempty"`
}

type OIDCCallbackResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	User         *User  `json:"user,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IsNewUser    bool   `json:"is_new_user,omitempty"`
}

type OIDCUserInfo struct {
	Subject  string                 `json:"subject,omitempty"`
	Username string                 `json:"username,omitempty"`
	Email    string                 `json:"email,omitempty"`
	Claims   map[string]interface{} `json:"claims,omitempty"`
}

// RegisterRequest represents a registration request
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=2,max=50"`
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

// LoginResponse represents a login response
type LoginResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	User         *User  `json:"user,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// RegisterResponse represents a registration response
type RegisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	User    *User  `json:"user,omitempty"`
}

// UserInfo represents user information for API responses
type UserInfo struct {
	ID          int64           `json:"id,string"`
	Username    string          `json:"username"`
	Email       string          `json:"email"`
	Avatar      string          `json:"avatar"`
	AgentConfig UserAgentConfig `json:"agent_config"`
	// MenuConfig 是主区域标签页 + 底部面板配置（刷新后前端据此回填）。
	MenuConfig UserMenuConfig `json:"menu_config"`
	// AgentProfile 是 agent_config.ProfileID 对应的 Profile 摘要（DTO，按需填充，可能为空）。
	AgentProfile        *AgentProfileInfo `json:"agent_profile,omitempty"`
	TenantID            uint64            `json:"tenant_id"`
	IsActive            bool              `json:"is_active"`
	CanAccessAllTenants bool              `json:"can_access_all_tenants"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

// AgentProfileInfo 是随用户信息返回的 Agent Profile 摘要（DTO）。
//
// 只暴露前端展示 / 选择所需的字段；Profile 的完整内容（系统提示词、技能等）
// 仍通过 /agent/profile 接口按 ID 获取。
type AgentProfileInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	IsDefault   bool   `json:"is_default"`
	IsBuiltin   bool   `json:"is_builtin"`
}

// ToUserInfo converts User to UserInfo (without sensitive data)
func (u *User) ToUserInfo() *UserInfo {
	return &UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		Avatar:      u.Avatar,
		AgentConfig: u.AgentConfig,
		MenuConfig:  u.MenuConfig,
		// TenantID:            u.TenantID,
		IsActive:            u.IsActive,
		CanAccessAllTenants: u.CanAccessAllTenants,
		CreatedAt:           u.CreatedAt,
		UpdatedAt:           u.UpdatedAt,
	}
}
