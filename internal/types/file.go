package types

import (
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Role 是文件 / assay 的角色，决定其在分析表单 analysis_result 里发布到哪个 key
// （前端按 dataMap[role] 取值）。它是枚举而不是自由文本：
//
//	go_file.role      文件角色
//	go_assay.role     assay 角色
//
// 表单 item 的 resolver.accept_formats 按同一套取值与之匹配。
type Role string

const (
	// RoleDefault 是未声明角色时的默认取值，前端按 dataMap["DEFAULT"] 读取。
	RoleDefault Role = "DEFAULT"
	// RoleTable 是表格型产物（如统计表）。
	RoleTable Role = "TABLE"
	// RoleDatabase 是数据库型产物（如 sqlite）。
	RoleDatabase Role = "DATABASE"
)

// File is a physical file. A file belongs to at most one Assay (Assay -> File is
// 1:N), so the binding lives directly on this row instead of a join table:
//
//	Project -> DatasetAssay -> Assay -> File
//
// Files that are only attached to a dataset (never produced by an assay) keep
// AssayID == 0, which is why the column stays nullable.
type File struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	FileName string `json:"file_name" gorm:"type:varchar(255)"`

	Path string `json:"path" gorm:"type:text;not null"`

	Format string `json:"format" gorm:"type:varchar(64)"`

	// AssayID owns the file (int64 PK of go_assay). 0 means the file is not
	// owned by any assay.
	AssayID int64 `json:"assay_id,string" gorm:"type:bigint;index"`

	// FileKey is the file's key inside its owning assay, e.g. FASTQ_R1 or BAM.
	// Analysis form inputs use it to map the file onto the matching key of
	// their resolver.accept_formats (see buildParseAnalysisResult).
	FileKey string `json:"file_key" gorm:"type:varchar(64)"`
	// Role 是文件在分析表单 analysis_result 中发布到哪个 key（dataMap[role]），
	// 取值见 Role*。DAG 节点产物的角色取自其 output_patterns 条目（未声明时为
	// RoleDefault，见 dag.DefaultNodeOutputRole）。
	Role           Role  `json:"role" gorm:"type:varchar(64);index"`
	AnalysisNodeID int64 `json:"analysis_node_id" gorm:"type:bigint;index"`

	Size int64 `json:"size"`

	MD5 string `json:"md5" gorm:"type:varchar(64);index"`

	Storage string `json:"storage" gorm:"type:varchar(32);default:LOCAL"`

	Description string `json:"description" gorm:"type:longtext"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (File) TableName() string {
	return "go_file"
}
func (t *File) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// NormalizeRole 把原始字符串归一化为合法 Role；空值或未知取值返回 ""。
// 大小写与首尾空白不敏感（"table" → RoleTable）。
func NormalizeRole(raw string) Role {
	switch role := Role(strings.ToUpper(strings.TrimSpace(raw))); role {
	case RoleDefault, RoleTable, RoleDatabase:
		return role
	default:
		return ""
	}
}

// NormalizedRole 返回文件的角色，未设置时回退到 RoleDefault。
func (f *File) NormalizedRole() Role {
	if f == nil {
		return RoleDefault
	}
	if role := NormalizeRole(string(f.Role)); role != "" {
		return role
	}
	return RoleDefault
}

// NormalizeRoles 把一组原始角色归一化：丢弃空值与未知取值，按首次出现顺序去重。
// 返回空切片表示「没有声明角色」，调用方据此决定不过滤（与 utils.AcceptFormats 一致）。
func NormalizeRoles(raw []string) []Role {
	seen := make(map[Role]struct{}, len(raw))
	roles := make([]Role, 0, len(raw))
	for _, one := range raw {
		role := NormalizeRole(one)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles
}

// RoleStrings 把 Role 切片还原为底层字符串切片，便于写库 / 拼装通用结构。
func RoleStrings(roles []Role) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, string(role))
	}
	return out
}

// RoleOption 是 Roles() 的响应条目，供前端渲染角色下拉框（value 即写库值）。
type RoleOption struct {
	Label string `json:"label"`
	Value Role   `json:"value"`
}

// Roles 返回全部合法角色，顺序即前端下拉的推荐展示顺序。新增 Role 常量时
// 只需在这里补一行，前端无需再维护一份写死的选项列表。
func Roles() []RoleOption {
	return []RoleOption{
		{Label: string(RoleDefault), Value: RoleDefault},
		{Label: string(RoleTable), Value: RoleTable},
		{Label: string(RoleDatabase), Value: RoleDatabase},
	}
}

// DatasetFile is the pure join row binding a File to a Dataset. The file's role
// lives on the file itself (go_file.role), not here, so this row only carries the
// two foreign keys and the creation timestamp.
type DatasetFile struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	FileID int64 `json:"file_id,string" gorm:"index;not null"`

	CreatedAt time.Time `json:"created_at"`
}

func (DatasetFile) TableName() string {
	return "go_dataset_file"
}

func (t *DatasetFile) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type AddFileToDatasetRequest struct {
	DatasetID      int64  `json:"dataset_id,string" binding:"required"`
	ProjectID      int64  `json:"-"`
	AnalysisNodeID int64  `json:"analysis_node_id,string,omitempty"` // optional, if provided, the file will be linked to the analysis node
	Path           string `json:"path" binding:"required"`
	Role           Role   `json:"role"` // 取值见 Role*；零值按 RoleDefault 处理
	FileName       string `json:"file_name" gorm:"type:varchar(255)"`
	IsCopy         bool   `json:"is_copy"`   // if true, copy the file to the dataset, otherwise just link it
	IsPrefix       bool   `json:"is_prefix"` // if true, the path is a prefix, and all files under the prefix will be added to the dataset
	// data analysis external
	Source string `json:"source"`
}

type AddFileToDatasetResponse struct {
	File        *File        `json:"file"`
	DatasetFile *DatasetFile `json:"dataset_file"`
}

type FileWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	// FileID mirrors the file's own primary key (go_file.id). It is kept so list
	// responses still carry a "file_id" the frontend can pass straight to the
	// by-id sheet endpoints; the entity no longer has a separate business id.
	FileID int64 `json:"file_id,string"`

	FileName string `json:"file_name"`

	Path string `json:"path"`

	Format string `json:"format"`

	AnalysisNodeID int64 `json:"analysis_node_id,string,omitempty"`

	Size int64 `json:"size"`

	MD5 string `json:"md5"`

	Storage string `json:"storage"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`

	Role Role `json:"role"`
}

type FileByProjectRoleGroup struct {
	Role Role `json:"role"`

	Items []*FileWithDatasetInfo `json:"items"`
}

// NormalizedRole 返回文件的角色，未设置时回退到 RoleDefault。
func (f *FileWithDatasetInfo) NormalizedRole() Role {
	if f == nil || f.Role == "" {
		return RoleDefault
	}
	return f.Role
}

// DatasetDirFile describes one physical file discovered under a dataset's
// workspace directory (utils.GetDatasetDir). It is the read model of
// /data/dataset/dir-files and the input of the batch directory import.
type DatasetDirFile struct {
	// Name is the file's base name.
	Name string `json:"name"`
	// RelativePath is the path relative to the dataset directory, using "/" as
	// the separator so the value is stable across platforms.
	RelativePath string `json:"relative_path"`
	// Path is the absolute path stored on go_file.path when the file is
	// imported.
	Path string `json:"path"`
	// Format is the lower-cased extension without the leading dot.
	Format string `json:"format"`
	Size   int64  `json:"size"`
	// ModTime is the physical file's modification time.
	ModTime string `json:"mod_time"`
	// Added reports whether a file with this path is already bound to the
	// dataset (go_file.path + go_dataset_file.dataset_id).
	Added bool `json:"added"`
}

// DatasetDirFilesResponse is the payload of /data/dataset/dir-files: the
// dataset workspace directory path plus every file found in it.
type DatasetDirFilesResponse struct {
	Path  string            `json:"path"`
	Files []*DatasetDirFile `json:"files"`
}

// ImportDatasetDirFilesResult summarizes a batch import of the dataset
// directory: one entry per discovered file is either imported or skipped.
type ImportDatasetDirFilesResult struct {
	Total    int      `json:"total"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}
