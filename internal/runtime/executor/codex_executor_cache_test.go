package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func contextWithClientAPIKey(apiKey string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", apiKey)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func expectedCodexPromptCacheKey(identity string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+identity)).String()
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_StablePromptCacheKeyFromAPIKey(t *testing.T) {
	ctx := contextWithClientAPIKey("test-api-key")
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}
	url := "https://example.com/responses"

	httpReq, _, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	expectedKey := expectedCodexPromptCacheKey("test-api-key")
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedKey)
	}
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != expectedKey {
		t.Fatalf("Conversation_id = %q, want %q", gotConversation, expectedKey)
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != expectedKey {
		t.Fatalf("Session_id = %q, want %q", gotSession, expectedKey)
	}

	httpReq2, _, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error (second call): %v", err)
	}
	body2, errRead2 := io.ReadAll(httpReq2.Body)
	if errRead2 != nil {
		t.Fatalf("read request body (second call): %v", errRead2)
	}
	gotKey2 := gjson.GetBytes(body2, "prompt_cache_key").String()
	if gotKey2 != expectedKey {
		t.Fatalf("prompt_cache_key (second call) = %q, want %q", gotKey2, expectedKey)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponses_SynthesizesPromptCacheKeyFromClientIdentity(t *testing.T) {
	ctx := contextWithClientAPIKey("test-api-key")
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}
	url := "https://example.com/responses"

	httpReq, translatedBody, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	expectedKey := expectedCodexPromptCacheKey("test-api-key")
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", got, expectedKey)
	}
	if got := gjson.GetBytes(translatedBody, "prompt_cache_key").String(); got != expectedKey {
		t.Fatalf("translated prompt_cache_key = %q, want %q", got, expectedKey)
	}
	if got := httpReq.Header.Get("Conversation_id"); got != expectedKey {
		t.Fatalf("Conversation_id = %q, want %q", got, expectedKey)
	}
	if got := httpReq.Header.Get("Session_id"); got != expectedKey {
		t.Fatalf("Session_id = %q, want %q", got, expectedKey)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponses_PreservesProvidedPromptCacheKey(t *testing.T) {
	ctx := contextWithClientAPIKey("test-api-key")
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","prompt_cache_key":"client-key"}`),
	}
	url := "https://example.com/responses"

	httpReq, translatedBody, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "client-key")
	}
	if got := gjson.GetBytes(translatedBody, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("translated prompt_cache_key = %q, want %q", got, "client-key")
	}
	if got := httpReq.Header.Get("Conversation_id"); got != "client-key" {
		t.Fatalf("Conversation_id = %q, want %q", got, "client-key")
	}
	if got := httpReq.Header.Get("Session_id"); got != "client-key" {
		t.Fatalf("Session_id = %q, want %q", got, "client-key")
	}
}
