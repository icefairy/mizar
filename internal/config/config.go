// Package config 提供 Mizar 初始化配置：首次运行向导 + 配置持久化 + 模型探测。
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mizar/internal/engine"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultContextWindow 未知模型时的回退窗口。
const DefaultContextWindow = 128000

// Config Mizar 运行配置。
type Config struct {
	BaseURL           string `json:"base_url"`                 // OpenAI 兼容端点，如 http://127.0.0.1:3002/v1
	APIKey            string `json:"api_key"`                  // Bearer token（可选）
	Model             string `json:"model"`                    // 模型名
	Thinking          bool   `json:"thinking,omitempty"`       // (deprecated) 旧版思考模式开关，由 ThinkingLevel 替代
	ThinkingLevel     string `json:"thinking_level,omitempty"` // 思考等级：auto/off/low/medium/high（空=auto）
	ContextWindow     int    `json:"context_window"`           // 上下文窗口（token）
	MaxSteps          int    `json:"max_steps,omitempty"`      // 最大循环步数（0=默认30）
	SkillEvolution    *bool  `json:"skill_evolution"`          // 技能自动沉淀（nil=默认开启）
	SkillStatsEnabled *bool  `json:"skill_stats_enabled"`      // 技能使用统计+周报（nil=默认开启）
	SkillStatsTopN    int    `json:"skill_stats_top_n"`        // 周报建议禁用数（0=默认10）
	SkillInjectMode   string `json:"skill_inject_mode"`        // 技能注入: index(默认,缓存友好)|full(全量正文)
	// MCPServers 外部 MCP server 列表（mcp_call 宿主函数用）。
	// 每项: name + (command/args | url)。覆盖内置 db_query 之外的长尾能力。
	MCPServers []engine.MCPServerConf `json:"mcp_servers,omitempty"`
}

// SkillInjectIsIndex 技能注入是否索引模式（默认 index；full = 全量正文）。
func (c *Config) SkillInjectIsIndex() bool {
	return c.SkillInjectMode != "full"
}

// SkillStatsOn 统计周报默认开启（nil 或 true 均开启，显式 false 关闭）。
func (c *Config) SkillStatsOn() bool {
	if c.SkillStatsEnabled == nil {
		return true
	}
	return *c.SkillStatsEnabled
}

// SkillStatsTop 返回建议禁用技能数（默认 10）。
func (c *Config) SkillStatsTop() int {
	if c.SkillStatsTopN <= 0 {
		return 10
	}
	return c.SkillStatsTopN
}

// SkillEvolutionEnabled 技能自动沉淀默认开启（nil 或 true 均开启，显式 false 关闭）。
func (c *Config) SkillEvolutionEnabled() bool {
	if c.SkillEvolution == nil {
		return true
	}
	return *c.SkillEvolution
}

// ThinkingStr 返回思考等级字符串（空串=auto）
func (c *Config) ThinkingStr() string {
	if c.ThinkingLevel == "" {
		return "auto"
	}
	return c.ThinkingLevel
}

// ThinkingOn 返回思考是否开启（兼容旧 bool 接口）
func (c *Config) ThinkingOn() bool {
	return c.ThinkingLevel == "low" || c.ThinkingLevel == "medium" || c.ThinkingLevel == "high"
}

// ConfigDir 返回配置目录（~/.mizar/），配置文件与全局 AGENTS.md 都放这里。
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mizar"
	}
	return filepath.Join(home, ".mizar")
}

// DefaultPath 默认配置文件路径（~/.mizar/config.json）。
func DefaultPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// Save 保存配置（自动建目录）。
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Load 读取配置；文件不存在返回空配置（不报错），损坏返回错误。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return c, nil
}

// ============================================================================
// 模型探测（wizard 用）
// ============================================================================

// ListModels 从 /v1/models 拉取模型列表。
func ListModels(baseURL, apiKey string) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接供应商失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("供应商返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// knownWindows 常见模型上下文窗口（token），未知模型回退 DefaultContextWindow。
var knownWindows = map[string]int{
	"deepseek-v4-flash": 1000000,
	"deepseek-v3":       128000,
	"deepseek-r1":       128000,
	"qwen3-35b":         131072,
	"qwen3-235b-a22b":   262144,
	"glm-4":             131072,
	"glm-z1-9b":         131072,
	"gpt-4o":            128000,
	"gpt-4o-mini":       128000,
	"claude-3-5-sonnet": 200000,
	"llama-3.1-70b":     131072,
}

// ProbeContextWindow 自动探测上下文窗口：
// 1. 优先读 /v1/models 元数据（部分网关返回 context_window / max_context）
// 2. 查预置表
// 3. 回退默认
func ProbeContextWindow(baseURL, model string) int {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err == nil {
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var out struct {
					Data []struct {
						ID            string `json:"id"`
						ContextWindow *int   `json:"context_window"`
						MaxContext    *int   `json:"max_context"`
						ContextLength *int   `json:"context_length"`
					} `json:"data"`
				}
				if json.NewDecoder(resp.Body).Decode(&out) == nil {
					for _, m := range out.Data {
						if m.ID == model {
							for _, p := range []*int{m.ContextWindow, m.MaxContext, m.ContextLength} {
								if p != nil && *p > 0 {
									return *p
								}
							}
						}
					}
				}
			}
		}
	}
	if w, ok := knownWindows[model]; ok {
		return w
	}
	return DefaultContextWindow
}

// ProbeThinking 探测模型是否支持思考模式：
// 发一个带 thinking 参数的极小请求，200 = 支持，400/404 = 不支持。
func ProbeThinking(baseURL, apiKey, model string) (bool, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 1,
		"thinking":   map[string]string{"type": "enabled"},
	})
	req, err := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("探测失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 200 {
		return true, nil
	}
	return false, nil
}
