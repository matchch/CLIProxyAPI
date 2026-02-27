package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestCheckRefreshesLimitsConcurrency(t *testing.T) {
	manager := NewManager(nil, nil, NoopHook{})
	exec := &refreshLimiterExecutor{
		provider: "codex",
		delay:    50 * time.Millisecond,
	}
	manager.RegisterExecutor(exec)

	refreshCount := refreshMaxConcurrency*2 + 1
	exec.wg.Add(refreshCount)

	for i := 0; i < refreshCount; i++ {
		if _, err := manager.Register(context.Background(), &Auth{
			ID:       fmt.Sprintf("auth-%d", i),
			Provider: exec.provider,
			Metadata: map[string]any{"refresh_interval_seconds": 1},
		}); err != nil {
			t.Fatalf("failed to register auth: %v", err)
		}
	}

	manager.checkRefreshes(context.Background())

	done := make(chan struct{})
	go func() {
		exec.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("refreshes did not finish in time")
	}

	if max := exec.MaxConcurrent(); max > refreshMaxConcurrency {
		t.Fatalf("expected at most %d concurrent refreshes, got %d", refreshMaxConcurrency, max)
	}
}

type refreshLimiterExecutor struct {
	provider string
	delay    time.Duration

	wg sync.WaitGroup
	mu sync.Mutex

	current int
	max     int
}

func (r *refreshLimiterExecutor) Identifier() string { return r.provider }

func (r *refreshLimiterExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (r *refreshLimiterExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (r *refreshLimiterExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	r.mu.Lock()
	r.current++
	if r.current > r.max {
		r.max = r.current
	}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.current--
		r.mu.Unlock()
		r.wg.Done()
	}()

	select {
	case <-ctx.Done():
		return auth, ctx.Err()
	case <-time.After(r.delay):
		return auth, nil
	}
}

func (r *refreshLimiterExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (r *refreshLimiterExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (r *refreshLimiterExecutor) MaxConcurrent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max
}
