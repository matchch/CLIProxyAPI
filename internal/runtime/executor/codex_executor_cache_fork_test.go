package executor

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexResponseCacheFork(t *testing.T) {
	derived := func(key string) string {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+key)).String()
	}
	for _, tc := range []struct{ name, key, payload, want string }{
		{"fallback", "fixture-a", `{}`, derived("fixture-a")},
		{"trimmed fallback", " fixture-a ", `{}`, derived("fixture-a")},
		{"different client", "fixture-b", `{}`, derived("fixture-b")},
		{"explicit priority", "fixture-a", `{"prompt_cache_key":"explicit-key"}`, "explicit-key"},
		{"explicit empty", "fixture-a", `{"prompt_cache_key":""}`, ""},
		{"no key", "", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc, _ := gin.CreateTestContext(httptest.NewRecorder())
			gc.Set("userApiKey", tc.key)
			ctx := context.WithValue(context.Background(), "gin", gc)
			for i := 0; i < 2; i++ {
				payload := []byte(tc.payload)
				req, body, err := (&CodexExecutor{}).cacheHelper(ctx, sdktranslator.FormatOpenAIResponse, "https://example.test/responses", cliproxyexecutor.Request{Payload: payload}, []byte(`{}`), nil)
				if err != nil {
					t.Fatal(err)
				}
				_ = req.Body.Close()
				if got := ojson.GetBytes(body, "prompt_cache_key").String(); got != tc.want {
					t.Fatalf("body key = %q, want %q", got, tc.want)
				}
				for j := range payload {
					payload[j] = 'x'
				}
				if got := req.Header.Get("Session-Id"); got != tc.want {
					t.Fatalf("session key after source mutation = %q, want %q", got, tc.want)
				}
			}
		})
	}
}
