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

func cachedUserID(apiKey string) string {
	if apiKey == "" {
		return generateFakeUserID()
	}

	userIDCacheCleanupOnce.Do(startUserIDCacheCleanup)

	now := time.Now()

	userIDCacheMu.Lock()
	entry := userIDCache[apiKey]
	if entry.value == "" || entry.expire.Before(now) || !isValidUserID(entry.value) {
		entry.value = generateFakeUserID()
	}
	entry.expire = now.Add(userIDTTL)
	userIDCache[apiKey] = entry
	userIDCacheMu.Unlock()
	return entry.value
}
