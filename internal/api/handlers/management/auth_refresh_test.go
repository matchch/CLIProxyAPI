package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		Results []struct {
			ID        string          `json:"id"`
			Provider  string          `json:"provider"`
			Refreshed bool            `json:"refreshed"`
			Error     string          `json:"error"`
			Auth      json.RawMessage `json:"auth"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	result := resp.Results[0]
	if !result.Refreshed || result.Error != "" {
		t.Fatalf("expected refresh success, got refreshed=%v error=%q", result.Refreshed, result.Error)
	}
	var returnedAuth coreauth.Auth
	if err := json.Unmarshal(result.Auth, &returnedAuth); err != nil {
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
}
