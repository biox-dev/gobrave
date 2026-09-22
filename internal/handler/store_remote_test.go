package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// stubStoreURLService 记录 UpdateStoreURL 的调用参数，其余方法由内嵌接口占位（不会被调用）。
type stubStoreURLService struct {
	interfaces.StoreService
	calledID  int64
	calledURL string
	callCount int
}

func (s *stubStoreURLService) UpdateStoreURL(_ context.Context, id int64, rawURL string) error {
	s.callCount++
	s.calledID = id
	s.calledURL = rawURL
	return nil
}

// postPublishStoreRemote 直接挂载 handler（绕过认证中间件），返回响应记录器。
func postPublishStoreRemote(t *testing.T, svc interfaces.StoreService, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	h := &StoreHandler{storeService: svc}
	engine.POST("/api/v1/store/publish-remote", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		h.PublishStoreRemote(c)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/store/publish-remote", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

// 当前阶段「发布到远程」只把 url（trim 后）写回 store.url，并如实返回 published=false。
func TestPublishStoreRemoteUpdatesURL(t *testing.T) {
	svc := &stubStoreURLService{}
	recorder := postPublishStoreRemote(t, svc, `{"store_id":"12","url":"  https://github.com/owner/repo.git  "}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.callCount != 1 {
		t.Fatalf("UpdateStoreURL call count = %d, want 1", svc.callCount)
	}
	if svc.calledID != 12 {
		t.Fatalf("updated id = %d, want 12", svc.calledID)
	}
	if svc.calledURL != "https://github.com/owner/repo.git" {
		t.Fatalf("updated url = %q, want the trimmed url", svc.calledURL)
	}

	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["store_id"] != "12" {
		t.Fatalf("resp store_id = %v, want \"12\"", resp["store_id"])
	}
	if resp["published"] != false {
		t.Fatalf("resp published = %v, want false (remote push not implemented yet)", resp["published"])
	}
}

// 入参不合法（缺 store_id / 缺 url / 不是 <owner>/<repo> 形态）时返回 400，且不落库。
func TestPublishStoreRemoteRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing store_id", body: `{"url":"https://github.com/owner/repo.git"}`},
		{name: "zero store_id", body: `{"store_id":"0","url":"https://github.com/owner/repo.git"}`},
		{name: "missing url", body: `{"store_id":"12"}`},
		{name: "blank url", body: `{"store_id":"12","url":"   "}`},
		{name: "not a git url", body: `{"store_id":"12","url":"not-a-git-url"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubStoreURLService{}
			recorder := postPublishStoreRemote(t, svc, tc.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
			}
			if svc.callCount != 0 {
				t.Fatalf("UpdateStoreURL call count = %d, want 0", svc.callCount)
			}
		})
	}
}

// store_id 用数字而不是字符串提交时也要能解析（int64ID 的兼容性）。
func TestPublishStoreRemoteAcceptsNumericStoreID(t *testing.T) {
	svc := &stubStoreURLService{}
	recorder := postPublishStoreRemote(t, svc, `{"store_id":12,"url":"git@github.com:owner/repo.git"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.calledID != 12 {
		t.Fatalf("updated id = %d, want 12", svc.calledID)
	}
	if svc.calledURL != "git@github.com:owner/repo.git" {
		t.Fatalf("updated url = %q, want the ssh url", svc.calledURL)
	}
}
