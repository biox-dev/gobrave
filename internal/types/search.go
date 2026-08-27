package types

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Pagination represents the pagination parameters
type Pagination struct {
	// Page
	Page int `form:"page"      json:"page"      binding:"omitempty,min=1"`
	// Page size
	PageSize int `form:"page_size" json:"page_size" binding:"omitempty,min=1,max=1000"`
}

// GetPage gets the page number, default is 1
func (p *Pagination) GetPage() int {
	if p.Page < 1 {
		return 1
	}
	return p.Page
}

// GetPageSize gets the page size, default is 20
func (p *Pagination) GetPageSize() int {
	if p.PageSize < 1 {
		return 20
	}
	// if p.PageSize > 10 {
	// 	return 10
	// }
	return p.PageSize
}

// Offset gets the offset for database query
func (p *Pagination) Offset() int {
	return (p.GetPage() - 1) * p.GetPageSize()
}

// Limit gets the limit for database query
func (p *Pagination) Limit() int {
	return p.GetPageSize()
}

// PageResult represents the pagination query result
type PageResult struct {
	Total    int64       `json:"total"`     // Total number of records
	Page     int         `json:"page"`      // Current page number
	PageSize int         `json:"page_size"` // Page size
	Data     interface{} `json:"data"`      // Data
}

// NewPageResult creates a new pagination result
func NewPageResult(total int64, page *Pagination, data interface{}) *PageResult {
	return &PageResult{
		Total:    total,
		Page:     page.GetPage(),
		PageSize: page.GetPageSize(),
		Data:     data,
	}
}

// CursorPagination represents lazy-loading cursor pagination parameters.
type CursorPagination struct {
	// Cursor for next page, empty means first page.
	Cursor string `form:"cursor" json:"cursor" binding:"omitempty"`
	// Sort field, for example: published_at, created_at, updated_at.
	SortBy string `form:"sort_by" json:"sort_by" binding:"omitempty"`
	// Page size.
	PageSize int `form:"page_size" json:"page_size" binding:"omitempty,min=1,max=1000"`
}

// GetPageSize gets the page size, default is 20.
func (p *CursorPagination) GetPageSize() int {
	if p == nil || p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 1000 {
		return 1000
	}
	return p.PageSize
}

// GetSortBy gets the sort field, default is published_at.
func (p *CursorPagination) GetSortBy() string {
	if p == nil || p.SortBy == "" {
		return "created_at"
	}
	return p.SortBy
}

// ArticleCursor is the cursor payload for article pagination.
// It uses sort_by + value + id to build a stable ordering cursor.
type ArticleCursor struct {
	SortBy string `json:"sort_by"`
	// Value stores the current sort field's value as string.
	Value string `json:"value"`
	// ID is the tiebreaker for stable ordering.
	ID int64 `json:"id"`
}

// EncodeArticleCursor encodes an article cursor to an opaque string.
func EncodeArticleCursor(c ArticleCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeArticleCursor decodes an opaque cursor string.
func DecodeArticleCursor(cursor string) (ArticleCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return ArticleCursor{}, fmt.Errorf("invalid cursor")
	}

	var payload ArticleCursor
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return ArticleCursor{}, fmt.Errorf("invalid cursor")
	}

	if payload.SortBy == "" || payload.Value == "" || payload.ID <= 0 {
		return ArticleCursor{}, fmt.Errorf("invalid cursor")
	}

	return payload, nil
}

// CursorPageResult represents cursor pagination result.
type CursorPageResult struct {
	Data       interface{} `json:"data"`
	PageSize   int         `json:"page_size"`
	NextCursor string      `json:"next_cursor,omitempty"`
	HasMore    bool        `json:"has_more"`
	SortBy     string      `json:"sort_by,omitempty"`
}

// NewCursorPageResult creates a new cursor pagination result.
func NewCursorPageResult(page *CursorPagination, data interface{}, nextCursor string, hasMore bool) *CursorPageResult {
	pageSize := 20
	if page != nil {
		pageSize = page.GetPageSize()
	}

	return &CursorPageResult{
		Data:       data,
		PageSize:   pageSize,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		SortBy:     page.GetSortBy(),
	}
}
