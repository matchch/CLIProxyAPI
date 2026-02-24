package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type refreshOnlyExecutor struct {
	provider string
	count    int
}

func (e *refreshOnlyExecutor) Identifier() string { return e.provider }
func (e *refreshOnlyExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshOnlyExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: make(chan cliproxyexecutor.StreamChunk)}, nil
}
func (e *refreshOnlyExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	_ = ctx
	e.count++
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["refreshed"] = true
	updated.Metadata["refreshed_at"] = time.Now().Format(time.RFC3339)
	return updated, nil
}
func (e *refreshOnlyExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshOnlyExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	_ = ctx
	_ = auth
	_ = req
	return nil, nil
}

type failingRefreshExecutor struct {
	refreshOnlyExecutor
	err error
}

func (e *failingRefreshExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	_ = ctx
	return nil, e.err
}

type mismatchedRefreshExecutor struct {
	refreshOnlyExecutor
	overrideID       string
	overrideProvider string
}

func (e *mismatchedRefreshExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	_ = ctx
	updated := auth.Clone()
	if e.overrideID != "" {
		updated.ID = e.overrideID
	}
	if e.overrideProvider != "" {
		updated.Provider = e.overrideProvider
	}
	return updated, nil
}

func TestPostForceRefreshTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshOnlyExecutor{provider: "codex"}
	manager.RegisterExecutor(exec)

	auth := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "old"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := &Handler{authManager: manager}

	router := gin.New()
	router.POST("/force", h.PostForceRefreshTokens)

	body := []byte(`{"auth_id":"codex-auth"}`)
	req := httptest.NewRequest(http.MethodPost, "/force", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		AuthID    string          `json:"auth_id"`
		Provider  string          `json:"provider"`
		Refreshed bool            `json:"refreshed"`
		Error     string          `json:"error"`
		Auth      json.RawMessage `json:"auth"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.AuthID != "codex-auth" {
		t.Fatalf("expected auth_id codex-auth, got %q", resp.AuthID)
	}
	if !resp.Refreshed || resp.Error != "" {
		t.Fatalf("expected refresh success, got refreshed=%v error=%q", resp.Refreshed, resp.Error)
	}
	var returnedAuth coreauth.Auth
	if err := json.Unmarshal(resp.Auth, &returnedAuth); err != nil {
		t.Fatalf("unmarshal auth: %v", err)
	}
	if returnedAuth.Metadata == nil || returnedAuth.Metadata["refreshed"] != true {
		t.Fatalf("expected refreshed metadata flag")
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth stored")
	}
	if updated.Metadata == nil || updated.Metadata["refreshed"] != true {
		t.Fatalf("expected manager to store refreshed metadata")
	}
	if updated.LastRefreshedAt.IsZero() || updated.UpdatedAt.IsZero() {
		t.Fatalf("expected timestamps to be set")
	}
	if exec.count != 1 {
		t.Fatalf("expected refresh called once, got %d", exec.count)
	}
}

func TestPostForceRefreshTokens_RequiresAuthID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	h := &Handler{authManager: manager}

	router := gin.New()
	router.POST("/force", h.PostForceRefreshTokens)

	req := httptest.NewRequest(http.MethodPost, "/force", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when auth_id missing, got %d", w.Code)
	}
	resp := parseForceRefreshResponse(t, w.Body.Bytes())
	if resp.Error != "auth_id is required" || resp.Refreshed {
		t.Fatalf("expected auth_id required error, got refreshed=%v err=%q", resp.Refreshed, resp.Error)
	}
}

func TestPostForceRefreshTokens_Errors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	makeHandler := func(manager *coreauth.Manager) (*Handler, *gin.Engine) {
		h := &Handler{authManager: manager}
		router := gin.New()
		router.POST("/force", h.PostForceRefreshTokens)
		return h, router
	}

	baseAuth := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
	}

	tests := []struct {
		name          string
		manager       func() *coreauth.Manager
		body          string
		expectedCode  int
		expectedError string
	}{
		{
			name: "auth not found",
			manager: func() *coreauth.Manager {
				return coreauth.NewManager(nil, nil, nil)
			},
			body:          `{"auth_id":"missing"}`,
			expectedCode:  http.StatusNotFound,
			expectedError: "auth not found",
		},
		{
			name: "provider mismatch",
			manager: func() *coreauth.Manager {
				m := coreauth.NewManager(nil, nil, nil)
				m.Register(context.Background(), baseAuth)
				m.RegisterExecutor(&refreshOnlyExecutor{provider: "codex"})
				return m
			},
			body:          `{"auth_id":"codex-auth","provider":"other"}`,
			expectedCode:  http.StatusBadRequest,
			expectedError: "provider mismatch",
		},
		{
			name: "executor missing",
			manager: func() *coreauth.Manager {
				m := coreauth.NewManager(nil, nil, nil)
				m.Register(context.Background(), baseAuth)
				return m
			},
			body:          `{"auth_id":"codex-auth"}`,
			expectedCode:  http.StatusBadRequest,
			expectedError: "executor not registered",
		},
		{
			name: "refresh failure",
			manager: func() *coreauth.Manager {
				m := coreauth.NewManager(nil, nil, nil)
				m.Register(context.Background(), baseAuth)
				m.RegisterExecutor(&failingRefreshExecutor{
					refreshOnlyExecutor: refreshOnlyExecutor{provider: "codex"},
					err:                 errors.New("refresh failed"),
				})
				return m
			},
			body:          `{"auth_id":"codex-auth"}`,
			expectedCode:  http.StatusBadRequest,
			expectedError: "refresh failed",
		},
		{
			name: "mismatched id from executor",
			manager: func() *coreauth.Manager {
				m := coreauth.NewManager(nil, nil, nil)
				m.Register(context.Background(), baseAuth)
				m.RegisterExecutor(&mismatchedRefreshExecutor{
					refreshOnlyExecutor: refreshOnlyExecutor{provider: "codex"},
					overrideID:          "other-id",
				})
				return m
			},
			body:          `{"auth_id":"codex-auth"}`,
			expectedCode:  http.StatusInternalServerError,
			expectedError: "executor returned mismatched auth id",
		},
		{
			name: "mismatched provider from executor",
			manager: func() *coreauth.Manager {
				m := coreauth.NewManager(nil, nil, nil)
				m.Register(context.Background(), baseAuth)
				m.RegisterExecutor(&mismatchedRefreshExecutor{
					refreshOnlyExecutor: refreshOnlyExecutor{provider: "codex"},
					overrideProvider:    "other",
				})
				return m
			},
			body:          `{"auth_id":"codex-auth"}`,
			expectedCode:  http.StatusInternalServerError,
			expectedError: "executor returned mismatched provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, router := makeHandler(tt.manager())
			req := httptest.NewRequest(http.MethodPost, "/force", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectedCode {
				t.Fatalf("expected status %d, got %d", tt.expectedCode, w.Code)
			}
			result := parseForceRefreshResponse(t, w.Body.Bytes())
			if tt.expectedError == "" && result.Error != "" {
				t.Fatalf("expected no error, got %q", result.Error)
			}
			if tt.expectedError != "" && !strings.Contains(result.Error, tt.expectedError) {
				t.Fatalf("expected error containing %q, got %q", tt.expectedError, result.Error)
			}
			if result.Refreshed {
				t.Fatalf("expected refreshed=false for %s", tt.name)
			}
		})
	}
}

func parseForceRefreshResponse(t *testing.T, body []byte) forceRefreshResponse {
	t.Helper()
	var resp forceRefreshResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}
