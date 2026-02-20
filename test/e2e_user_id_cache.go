// Standalone end-to-end test for Claude user_id caching
// This test does not use the Go testing framework (go test)
// Run with: go run test/e2e_user_id_cache.go
//
// This test verifies that the same user_id is used across different Claude models
// when using the same API key, ensuring proper caching behavior.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type testResult struct {
	pass    bool
	message string
}

func main() {
	fmt.Println("=== Standalone E2E Test for Claude user_id Caching ===")
	fmt.Println()

	result := runE2ETest()
	if result.pass {
		fmt.Printf("✅ TEST PASSED: %s\n", result.message)
		os.Exit(0)
	} else {
		fmt.Printf("❌ TEST FAILED: %s\n", result.message)
		os.Exit(1)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func runE2ETest() testResult {
	// Step 1: Start fake Claude API server to capture user_id
	fmt.Println("Step 1: Starting fake Claude API server...")
	var capturedUserIDs []string
	var capturedModels []string
	fakeServer := http.Server{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			userID := gjson.GetBytes(body, "metadata.user_id").String()
			model := gjson.GetBytes(body, "model").String()
			capturedUserIDs = append(capturedUserIDs, userID)
			capturedModels = append(capturedModels, model)
			fmt.Printf("  Fake server received: model=%s, user_id=%s\n", model, userID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"` + model + `","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		}),
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fakeServer.Addr)
	if err != nil {
		return testResult{false, fmt.Sprintf("Failed to start fake server: %v", err)}
	}
	fakeServerURL := fmt.Sprintf("http://%s", listener.Addr().String())
	go fakeServer.Serve(listener)
	defer fakeServer.Shutdown(context.Background())
	fmt.Printf("  Fake server started at: %s\n", fakeServerURL)
	fmt.Println()

	// Step 2: Compile server binary
	fmt.Println("Step 2: Compiling server binary...")
	repoRoot, err := findRepoRoot()
	if err != nil {
		return testResult{false, fmt.Sprintf("Failed to find repo root: %v", err)}
	}

	tmpDir, err := os.MkdirTemp("", "e2e-test-")
	if err != nil {
		return testResult{false, fmt.Sprintf("Failed to create temp dir: %v", err)}
	}
	defer os.RemoveAll(tmpDir)

	serverBinary := filepath.Join(tmpDir, "cliproxyapi")
	cmd := exec.Command("go", "build", "-o", serverBinary, "./cmd/server")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return testResult{false, fmt.Sprintf("Failed to compile server: %v\n%s", err, output)}
	}
	fmt.Printf("  Binary compiled: %s\n", serverBinary)
	fmt.Println()

	// Step 3: Create configuration with model mappings
	fmt.Println("Step 3: Creating configuration...")
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		return testResult{false, fmt.Sprintf("Failed to create auth dir: %v", err)}
	}

	// Create config.yaml with claude-api-key and model mappings
	// Use a random high port to avoid conflicts
	proxyPort := 18317 + (time.Now().Unix() % 1000)
	configContent := fmt.Sprintf(`host: "127.0.0.1"
port: %d
debug: true
auth-dir: "%s"
api-keys:
  - "test-proxy-key"
claude-api-key:
  - api-key: "test-key-123"
    base-url: "%s"
    models:
      - name: "claude-3-5-sonnet-20241022"
        alias: "claude-3-5-sonnet"
      - name: "claude-3-5-haiku-20241022"
        alias: "claude-3-5-haiku"
`, proxyPort, authDir, fakeServerURL)

	configFile := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		return testResult{false, fmt.Sprintf("Failed to write config: %v", err)}
	}
	fmt.Printf("  Config file created: %s\n", configFile)
	fmt.Println()

	// Step 4: Start proxy server
	fmt.Println("Step 4: Starting proxy server...")
	serverCmd := exec.Command(serverBinary, "-config", configFile)
	var serverOut bytes.Buffer
	serverCmd.Stdout = &serverOut
	serverCmd.Stderr = &serverOut

	if err := serverCmd.Start(); err != nil {
		return testResult{false, fmt.Sprintf("Failed to start server: %v", err)}
	}
	defer serverCmd.Process.Kill()

	// Wait for server to be ready
	maxWait := 10 // seconds
	serverReady := false
	for i := 0; i < maxWait*10; i++ {
		time.Sleep(100 * time.Millisecond)
		output := serverOut.String()

		// Check if server has finished starting
		if strings.Contains(output, "API server started successfully on:") ||
			strings.Contains(output, "full client load complete") {
			serverReady = true
			break
		}

		// Check for errors
		if strings.Contains(output, "exited with error") || strings.Contains(output, "FATAL") {
			return testResult{false, fmt.Sprintf("Server failed to start:\n%s", output)}
		}
	}

	if !serverReady {
		return testResult{false, fmt.Sprintf("Server did not start within %d seconds\nServer output: %s", maxWait, serverOut.String())}
	}

	// Give it a moment to fully initialize
	time.Sleep(500 * time.Millisecond)

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
	fmt.Printf("  Proxy server started at: %s\n", proxyURL)
	fmt.Println()

	// Step 5: Make requests to verify user_id caching
	fmt.Println("Step 5: Making requests to verify user_id caching...")
	models := []string{"claude-3-5-sonnet", "claude-3-5-haiku"}
	client := &http.Client{Timeout: 10 * time.Second}

	for _, model := range models {
		fmt.Printf("  Sending request for model: %s\n", model)
		payload := map[string]interface{}{
			"model": model,
			"messages": []map[string]interface{}{
				{"role": "user", "content": []map[string]string{{"type": "text", "text": "hi"}}},
			},
			"max_tokens": 10,
		}
		payloadBytes, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", proxyURL+"/v1/messages", bytes.NewReader(payloadBytes))
		if err != nil {
			return testResult{false, fmt.Sprintf("Failed to create request: %v", err)}
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-proxy-key")
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := client.Do(req)
		if err != nil {
			return testResult{false, fmt.Sprintf("Request failed for %s: %v\nServer output: %s", model, err, serverOut.String())}
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return testResult{false, fmt.Sprintf("Request failed with status %d for %s: %s\nServer output: %s", resp.StatusCode, model, string(respBody), serverOut.String())}
		}
	}
	fmt.Println()

	// Step 6: Verify results
	fmt.Println("Step 6: Verifying results...")
	if len(capturedUserIDs) != 2 {
		return testResult{false, fmt.Sprintf("Expected 2 requests, got %d", len(capturedUserIDs))}
	}

	if capturedUserIDs[0] == "" || capturedUserIDs[1] == "" {
		return testResult{false, "Expected user_id to be populated"}
	}

	fmt.Printf("  user_id[0] (model=%s): %s\n", capturedModels[0], capturedUserIDs[0])
	fmt.Printf("  user_id[1] (model=%s): %s\n", capturedModels[1], capturedUserIDs[1])

	if capturedUserIDs[0] != capturedUserIDs[1] {
		return testResult{false, fmt.Sprintf("Expected same user_id across models, got %q and %q", capturedUserIDs[0], capturedUserIDs[1])}
	}

	// Verify user_id format: user_{64-hex}_account__session_{uuid}
	userIDPattern := `^user_[a-f0-9]{64}_account__session_[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	if !strings.HasPrefix(capturedUserIDs[0], "user_") || !strings.Contains(capturedUserIDs[0], "_account__session_") {
		return testResult{false, fmt.Sprintf("Invalid user_id format: %s (expected pattern: %s)", capturedUserIDs[0], userIDPattern)}
	}

	return testResult{true, fmt.Sprintf("Same user_id (%s) was used for both models", capturedUserIDs[0])}
}
