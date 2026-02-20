package executor

import (
	"testing"
	"time"
)

func resetUserIDCache() {
	userIDCacheMu.Lock()
	userIDCache = make(map[string]userIDCacheEntry)
	userIDCacheMu.Unlock()
}

func TestCachedUserID_ReusesWithinTTL(t *testing.T) {
	resetUserIDCache()

	first := cachedUserID("claude", "claude-3-5-sonnet")
	second := cachedUserID("claude", "claude-3-5-sonnet")

	if first == "" {
		t.Fatal("expected generated user_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached user_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedUserID_ExpiresAfterTTL(t *testing.T) {
	resetUserIDCache()

	expiredID := cachedUserID("claude", "claude-3-5-haiku")
	userIDCacheMu.Lock()
	userIDCache["claude|claude-3-5-haiku"] = userIDCacheEntry{
		value:  expiredID,
		expire: time.Now().Add(-time.Minute),
	}
	userIDCacheMu.Unlock()

	newID := cachedUserID("claude", "claude-3-5-haiku")
	if newID == expiredID {
		t.Fatalf("expected expired user_id to be replaced, got %q", newID)
	}
	if newID == "" {
		t.Fatal("expected regenerated user_id to be non-empty")
	}
}

func TestCachedUserID_IsScopedByModel(t *testing.T) {
	resetUserIDCache()

	sonnet := cachedUserID("claude", "claude-3-5-sonnet")
	haiku := cachedUserID("claude", "claude-3-5-haiku")

	if sonnet == haiku {
		t.Fatalf("expected different models to have different user_ids, got %q", sonnet)
	}
}
