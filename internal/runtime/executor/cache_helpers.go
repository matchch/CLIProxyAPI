package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheMap stores prompt cache IDs keyed by model+user_id.
// Protected by codexCacheMu. Entries expire after 1 hour.
var (
	codexCacheMap = make(map[string]codexCache)
	codexCacheMu  sync.RWMutex
)

// codexCacheCleanupInterval controls how often expired entries are purged.
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce ensures the background cleanup goroutine starts only once.
var codexCacheCleanupOnce sync.Once

// startCodexCacheCleanup launches a background goroutine that periodically
// removes expired entries from codexCacheMap to prevent memory leaks.
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
		}
	}()
}

// purgeExpiredCodexCache removes entries that have expired.
func purgeExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

// getCodexCache retrieves a cached entry, returning ok=false if not found or expired.
func getCodexCache(key string) (codexCache, bool) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.RLock()
	cache, ok := codexCacheMap[key]
	codexCacheMu.RUnlock()
	if !ok || cache.Expire.Before(time.Now()) {
		return codexCache{}, false
	}
	return cache, true
}

// setCodexCache stores a cache entry.
func setCodexCache(key string, cache codexCache) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
}

func stableCodexPromptCacheKey(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+identity)).String()
}

func resolveCodexPromptCache(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, req cliproxyexecutor.Request) codexCache {
	var cache codexCache

	switch from {
	case "claude":
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			if cached, ok := getCodexCache(key); ok {
				cache = cached
			} else {
				cache = codexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				setCodexCache(key, cache)
			}
		}
	case "openai-response":
		if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
			break
		}
		if apiKey := strings.TrimSpace(apiKeyFromContext(ctx)); apiKey != "" {
			cache.ID = stableCodexPromptCacheKey(apiKey)
			break
		}
		if auth != nil {
			if _, account := auth.AccountInfo(); strings.TrimSpace(account) != "" {
				cache.ID = stableCodexPromptCacheKey(account)
				break
			}
			if idx := strings.TrimSpace(auth.EnsureIndex()); idx != "" {
				cache.ID = stableCodexPromptCacheKey(idx)
				break
			}
			if id := strings.TrimSpace(auth.ID); id != "" {
				cache.ID = stableCodexPromptCacheKey(id)
				break
			}
		}
		cache.ID = uuid.New().String()
	case "openai":
		if apiKey := strings.TrimSpace(apiKeyFromContext(ctx)); apiKey != "" {
			cache.ID = stableCodexPromptCacheKey(apiKey)
		}
	}

	return cache
}
