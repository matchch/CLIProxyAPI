package management

import (
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

type forceRefreshResult struct {
	ID        string         `json:"id"`
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager unavailable"})
		return
	}

	var req forceRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" { // allow empty body
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	ctx := c.Request.Context()
	providerFilter := strings.ToLower(strings.TrimSpace(req.Provider))
	authID := strings.TrimSpace(req.AuthID)
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id is required"})
		return
	}

	auth, ok := h.authManager.GetByID(authID)
	if !ok || auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if providerFilter != "" && !strings.EqualFold(providerFilter, auth.Provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider mismatch"})
		return
	}

	now := time.Now()
	result := forceRefreshResult{ID: auth.ID, Provider: auth.Provider}
	exec, okExec := h.authManager.Executor(strings.TrimSpace(auth.Provider))
	if !okExec || exec == nil {
		result.Error = "executor not registered"
		c.JSON(http.StatusBadRequest, gin.H{"results": []forceRefreshResult{result}})
		return
	}

	updated, errRefresh := exec.Refresh(ctx, auth.Clone())
	if errRefresh != nil {
		result.Error = errRefresh.Error()
		c.JSON(http.StatusBadRequest, gin.H{"results": []forceRefreshResult{result}})
		return
	}
	if updated == nil {
		updated = auth.Clone()
	}
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	if _, errUpdate := h.authManager.Update(ctx, updated); errUpdate != nil {
		result.Error = errUpdate.Error()
		c.JSON(http.StatusBadRequest, gin.H{"results": []forceRefreshResult{result}})
		return
	}
	result.Refreshed = true
	result.Auth = updated.Clone()

	c.JSON(http.StatusOK, gin.H{"results": []forceRefreshResult{result}})
}
