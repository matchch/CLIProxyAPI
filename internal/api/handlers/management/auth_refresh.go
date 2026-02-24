package management

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type forceRefreshRequest struct {
	AuthID   string `json:"auth_id"`
	Provider string `json:"provider"`
}

type forceRefreshResponse struct {
	AuthID    string         `json:"auth_id"`
	Provider  string         `json:"provider"`
	Refreshed bool           `json:"refreshed"`
	Error     string         `json:"error,omitempty"`
	Auth      *coreauth.Auth `json:"auth,omitempty"`
}

// PostForceRefreshTokens forces token refresh for a single credential and returns refreshed data.
//
// Endpoint:
//
//	POST /v0/management/auth-files/force-refresh
//
// Body (required):
//   - auth_id: the auth id to refresh
//   - provider (optional): validate provider before refresh
func (h *Handler) PostForceRefreshTokens(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, forceRefreshResponse{Error: "auth manager unavailable"})
		return
	}

	var req forceRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) { // allow empty body
		c.JSON(http.StatusBadRequest, forceRefreshResponse{Error: "invalid body"})
		return
	}

	ctx := c.Request.Context()
	providerFilter := strings.ToLower(strings.TrimSpace(req.Provider))
	authID := strings.TrimSpace(req.AuthID)
	result := forceRefreshResponse{AuthID: authID, Provider: providerFilter}
	if authID == "" {
		result.Error = "auth_id is required"
		c.JSON(http.StatusBadRequest, result)
		return
	}

	auth, ok := h.authManager.GetByID(authID)
	if !ok || auth == nil {
		result.Error = "auth not found"
		c.JSON(http.StatusNotFound, result)
		return
	}
	result.AuthID = auth.ID
	result.Provider = auth.Provider
	if providerFilter != "" && !strings.EqualFold(providerFilter, auth.Provider) {
		result.Error = "provider mismatch"
		c.JSON(http.StatusBadRequest, result)
		return
	}

	exec, okExec := h.authManager.Executor(strings.TrimSpace(auth.Provider))
	if !okExec || exec == nil {
		result.Error = "executor not registered"
		c.JSON(http.StatusBadRequest, result)
		return
	}

	updated, errRefresh := exec.Refresh(ctx, auth.Clone())
	if errRefresh != nil {
		result.Error = errRefresh.Error()
		c.JSON(http.StatusBadRequest, result)
		return
	}
	if updated == nil {
		updated = auth.Clone()
	}
	if updated.ID == "" {
		updated.ID = auth.ID
	}
	if updated.ID != auth.ID {
		result.Error = "executor returned mismatched auth id"
		c.JSON(http.StatusInternalServerError, result)
		return
	}
	if updated.Provider == "" {
		updated.Provider = auth.Provider
	}
	if !strings.EqualFold(updated.Provider, auth.Provider) {
		result.Error = "executor returned mismatched provider"
		c.JSON(http.StatusInternalServerError, result)
		return
	}
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	now := time.Now()
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	persisted, errUpdate := h.authManager.Update(ctx, updated)
	if errUpdate != nil {
		result.Error = errUpdate.Error()
		c.JSON(http.StatusInternalServerError, result)
		return
	}
	if persisted == nil {
		result.Error = "failed to persist refreshed auth"
		c.JSON(http.StatusInternalServerError, result)
		return
	}
	result.Refreshed = true
	result.Auth = persisted.Clone()
	result.AuthID = persisted.ID
	result.Provider = persisted.Provider

	c.JSON(http.StatusOK, result)
}
