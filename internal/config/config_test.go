package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigSaveLoad 配置持久化往返。
func TestConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := &Config{
		BaseURL:      "http://127.0.0.1:3002/v1",
		APIKey:       "sk-test",
		Model:        "deepseek-v4-flash",
		ThinkingLevel: "high",
		ContextWindow: 1000000,
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != c.BaseURL || got.Model != c.Model || got.ThinkingLevel != "high" || got.ContextWindow != 1000000 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

// TestLoadMissing 文件不存在返回默认配置（不报错）。
func TestLoadMissing(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("default model should be empty: %+v", got)
	}
}

// TestLoadInvalid 损坏文件报错。
func TestLoadInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte("{not json"), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("corrupt config should error")
	}
}

// TestListModels 从 /v1/models 拉取模型列表。
func TestListModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "deepseek-v4-flash", "object": "model"},
				{"id": "qwen3-35b", "object": "model"},
			},
		})
	}))
	defer ts.Close()
	models, err := ListModels(ts.URL+"/v1", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "deepseek-v4-flash" || models[1] != "qwen3-35b" {
		t.Fatalf("models = %v", models)
	}
}

// TestProbeContextWindow 自动探测上下文窗口：
// 优先读 models 元数据，其次查预置表，最后回退默认。
func TestProbeContextWindow(t *testing.T) {
	// 1. 模型带 context_window 元数据
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "big-model", "context_window": 524288},
			},
		})
	}))
	defer ts.Close()
	if got := ProbeContextWindow(ts.URL+"/v1", "big-model"); got != 524288 {
		t.Fatalf("meta context = %d, want 524288", got)
	}

	// 2. 预置表命中
	if got := ProbeContextWindow("http://127.0.0.1:1/v1", "deepseek-v4-flash"); got != 1000000 {
		t.Fatalf("known model context = %d, want 1000000", got)
	}

	// 3. 未知模型回退默认
	if got := ProbeContextWindow("http://127.0.0.1:1/v1", "unknown-model"); got != DefaultContextWindow {
		t.Fatalf("fallback context = %d, want %d", got, DefaultContextWindow)
	}
}

// TestProbeThinking 思考模式探测：带 thinking 参数请求成功 = 支持。
func TestProbeThinking(t *testing.T) {
	// 支持：返回正常 completion
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "hi"}}},
		})
	}))
	defer ts.Close()
	if ok, err := ProbeThinking(ts.URL+"/v1", "sk-test", "test-model"); err != nil || !ok {
		t.Fatalf("thinking probe = %v, %v; want true, nil", ok, err)
	}

	// 不支持：400
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "thinking not supported"}})
	}))
	defer ts2.Close()
	if ok, err := ProbeThinking(ts2.URL+"/v1", "sk-test", "test-model"); err != nil || ok {
		t.Fatalf("thinking probe = %v, %v; want false, nil", ok, err)
	}
}
