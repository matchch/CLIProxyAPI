package executor

import (
	"sync"
	"time"
)

type userIDCacheEntry struct {
	value  string
	expire time.Time
}

var (
	userIDCache            = make(map[string]userIDCacheEntry)
	userIDCacheMu          sync.RWMutex
	userIDCacheCleanupOnce sync.Once
)

const (
	userIDTTL                = time.Hour
	userIDCacheCleanupPeriod = 15 * time.Minute
)

func startUserIDCacheCleanup() {
	go func() {
		ticker := time.NewTicker(userIDCacheCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredUserIDs()
		}
	}()
}

func purgeExpiredUserIDs() {
	now := time.Now()
	userIDCacheMu.Lock()
	for key, entry := range userIDCache {
		if entry.expire.Before(now) {
			delete(userIDCache, key)
		}
	}
	userIDCacheMu.Unlock()
}

func cachedUserID(provider, model string) string {
	if provider == "" || model == "" {
		return generateFakeUserID()
	}

	userIDCacheCleanupOnce.Do(startUserIDCacheCleanup)

	key := provider + "|" + model
	now := time.Now()

	userIDCacheMu.RLock()
	entry, ok := userIDCache[key]
	userIDCacheMu.RUnlock()
	if ok && entry.expire.After(now) && entry.value != "" && isValidUserID(entry.value) {
		return entry.value
	}

	newID := generateFakeUserID()
	userIDCacheMu.Lock()
	userIDCache[key] = userIDCacheEntry{value: newID, expire: now.Add(userIDTTL)}
	userIDCacheMu.Unlock()
	return newID
}
