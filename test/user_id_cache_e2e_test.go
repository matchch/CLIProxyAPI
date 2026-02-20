package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// findAvailablePort finds an available port by opening a listener and immediately closing it
func findAvailablePort(t *testing.T) int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	// Small delay to ensure port is released
	time.Sleep(100 * time.Millisecond)
	return port
}


// TestUserIDCacheE2E performs a real end-to-end test:
// 1. Compiles the binary
// 2. Creates a config file
// 3. Starts a fake upstream Claude API server
// 4. Starts the CLIProxyAPI server binary
// 5. Makes requests through the proxy to different models
// 6. Verifies that the same user_id is sent to upstream for the same api_key
func TestUserIDCacheE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Step 1: Create a fake upstream Claude API server
	var capturedUserIDs []string
	var capturedModels []string
	fakeClaudeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userID := gjson.GetBytes(body, "metadata.user_id").String()
		model := gjson.GetBytes(body, "model").String()
		capturedUserIDs = append(capturedUserIDs, userID)
		capturedModels = append(capturedModels, model)
		t.Logf("Fake Claude API received: model=%s, user_id=%s", model, userID)

		// Return a valid Claude response
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"id":      "msg_test",
			"type":    "message",
			"model":   model,
			"role":    "assistant",
			"content": []map[string]interface{}{{"type": "text", "text": "test response"}},
			"usage":   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakeClaudeServer.Close()
	t.Logf("Fake Claude API server started at: %s", fakeClaudeServer.URL)

	// Step 2: Compile the binary
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "cliproxyapi")
	t.Logf("Building binary at: %s", binaryPath)

	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/server")
	buildCmd.Dir = filepath.Join("..", "") // Go to repo root
	buildOutput, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v\nOutput: %s", err, buildOutput)
	}
	t.Logf("Binary compiled successfully")

	// Step 3: Create a configuration file with a dynamic port
	// Find an available port
	testPort := findAvailablePort(t)

	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create an auth file for Claude credentials
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatalf("Failed to create auth dir: %v", err)
	}

	claudeAuthFile := filepath.Join(authDir, "claude-test.json")
	claudeAuth := map[string]interface{}{
		"id":       "test-claude-id",
		"label":    "Test Claude",
		"provider": "claude",
		"attributes": map[string]interface{}{
			"api_key":  "test-claude-key-123",
			"base_url": fakeClaudeServer.URL,
		},
	}
	claudeAuthData, _ := json.MarshalIndent(claudeAuth, "", "  ")
	if err := os.WriteFile(claudeAuthFile, claudeAuthData, 0644); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}
	t.Logf("Auth file created at: %s", claudeAuthFile)

	config := map[string]interface{}{
		"host":                       "127.0.0.1",
		"port":                       testPort,
		"debug":                      true,
		"usage-statistics-enabled":   false,
		"commercial-mode":            false,
		"logging-to-file":            false,
		"auth-dir":                   authDir,
		"api-keys":                   []string{"test-proxy-key"},
		"force-model-prefix":         false,
		"request-retry":              0,
		"passthrough-headers":        false,
		"ws-auth":                    false,
		"nonstream-keepalive-interval": 0,
		"remote-management": map[string]interface{}{
			"allow-remote":           false,
			"secret-key":             "",
			"disable-control-panel":  true,
		},
	}

	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}
	t.Logf("Config file created at: %s", configPath)

	// Step 4: Start the server binary
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, binaryPath, "-config", configPath)
	serverCmd.Dir = tmpDir

	// Capture stdout/stderr
	var stdout, stderr bytes.Buffer
	serverCmd.Stdout = &stdout
	serverCmd.Stderr = &stderr

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	// Wait for server to start
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", testPort)
	deadline := time.Now().Add(15 * time.Second)
	serverStarted := false
	for time.Now().Before(deadline) {
		// Try to connect to the server
		resp, err := http.Get(proxyURL + "/v1/models")
		if err == nil {
			resp.Body.Close()
			serverStarted = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !serverStarted {
		t.Fatalf("Server did not start within timeout. Output:\n%s\nStderr:\n%s", stdout.String(), stderr.String())
	}

	// Give the server extra time to fully load auth files
	time.Sleep(1 * time.Second)
	t.Logf("Proxy server started at: %s", proxyURL)

	// Step 5: Make requests to different models through the proxy
	models := []string{"claude-3-5-sonnet", "claude-3-5-haiku"}
	client := &http.Client{Timeout: 10 * time.Second}

	for _, model := range models {
		t.Logf("Sending request for model: %s", model)

		payload := map[string]interface{}{
			"model": model,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "hello"},
			},
			"max_tokens": 100,
			"stream":     false,
		}

		payloadBytes, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", proxyURL+"/v1/messages", bytes.NewReader(payloadBytes))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-proxy-key") // Use the configured API key
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed for model %s: %v\nServer output:\n%s", model, err, stdout.String())
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Request failed with status %d for model %s: %s\nServer output:\n%s", resp.StatusCode, model, body, stdout.String())
		}

		t.Logf("Request succeeded for model: %s", model)
		time.Sleep(100 * time.Millisecond) // Small delay between requests
	}

	// Step 6: Verify that the same user_id was used for both models
	if len(capturedUserIDs) != 2 {
		t.Fatalf("Expected 2 captured user_ids, got %d", len(capturedUserIDs))
	}

	t.Logf("Captured user_id[0] (model=%s): %s", capturedModels[0], capturedUserIDs[0])
	t.Logf("Captured user_id[1] (model=%s): %s", capturedModels[1], capturedUserIDs[1])

	if capturedUserIDs[0] == "" || capturedUserIDs[1] == "" {
		t.Fatal("Expected user_ids to be populated in upstream requests")
	}

	if capturedUserIDs[0] != capturedUserIDs[1] {
		t.Fatalf("Expected same user_id for both models, got:\n  %s (model=%s)\n  %s (model=%s)",
			capturedUserIDs[0], capturedModels[0],
			capturedUserIDs[1], capturedModels[1])
	}

	// Verify user_id format
	if !strings.HasPrefix(capturedUserIDs[0], "user_") {
		t.Fatalf("user_id has invalid format: %s", capturedUserIDs[0])
	}

	t.Logf("✓ End-to-end test PASSED: Same user_id (%s) was sent to upstream Claude API for both models", capturedUserIDs[0])
	t.Logf("✓ Test used compiled binary, config file, and real HTTP communication")
}
