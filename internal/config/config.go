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
	BaseURL       string `json:"base_url"`                 // OpenAI 兼容端点，如 http://127.0.0.1:3002/v1
	APIKey        string `json:"api_key"`                  // Bearer token（可选）
	Model         string `json:"model"`                    // 模型名
	Thinking      bool   `json:"thinking,omitempty"`       // (deprecated) 旧版思考模式开关，由 ThinkingLevel 替代
	ThinkingLevel string `json:"thinking_level,omitempty"` // 思考等级：auto/off/low/medium/high（空=auto）
	ContextWindow int    `json:"context_window"`           // 上下文窗口（token）
	MaxOutput     int    `json:"max_output,omitempty"`     // 模型单次输出上限（token，0=未知，用于摘要预算等）
	MaxSteps      int    `json:"max_steps,omitempty"`      // 最大循环步数（0=默认 60）
	// Retry 重试策略（nil = 使用默认值：maxRetries=2, baseDelayMs=1000）
	RetryMaxRetries   int    `json:"retry_max_retries,omitempty"`   // LLM 失败最大重试次数（0=不重试）
	RetryBaseDelayMs  int    `json:"retry_base_delay_ms,omitempty"` // LLM 重试初始退避毫秒（默认 1000）
	SkillEvolution    *bool  `json:"skill_evolution"`               // 技能自动沉淀（nil=默认开启）
	SkillStatsEnabled *bool  `json:"skill_stats_enabled"`           // 技能使用统计+周报（nil=默认开启）
	SkillStatsTopN    int    `json:"skill_stats_top_n"`             // 周报建议禁用数（0=默认10）
	SkillInjectMode   string `json:"skill_inject_mode"`             // 技能注入: index(默认,缓存友好)|full(全量正文)
	UserColor         string `json:"user_color,omitempty"`          // TUI 用户消息颜色（默认 white）
	AiColor           string `json:"ai_color,omitempty"`            // TUI AI 回复颜色（默认 green）
	// UserAgent 自定义 User-Agent（空=使用 Go 默认，用于网关/厂商侧识别客户端）
	UserAgent string `json:"user_agent,omitempty"`
	// CacheMissNotice 是否显示缓存命中率显著下跌提示（默认 false = 关闭）。
	CacheMissNotice *bool `json:"cache_miss_notice,omitempty"`
	// DiagLog 诊断日志开关（落盘到 ~/.mizar/logs/，默认 nil = 开启）。
	// 用于排查「任务莫名中止/卡死/退出」类无法复现的问题；设 0 可关闭。
	DiagLog *bool `json:"diag_log,omitempty"`
	// MCPServers 外部 MCP server 列表（mcp_call 宿主函数用）。
	// 每项: name + (command/args | url)。覆盖内置 db_query 之外的长尾能力。
	MCPServers []engine.MCPServerConf `json:"mcp_servers,omitempty"`
	// Web Web UI 配置（-serve 模式下提供网页界面 + 登录鉴权）。
	Web *WebConfig `json:"web,omitempty"`
	// Providers 记住过的 LLM 供应商（可多条：家里直连 / 外部隧道代理 / 其它上游）。
	// BaseURL/APIKey/Model 始终是「当前生效」的值，本列表只是收藏夹 + 便捷切换。
	Providers []Provider `json:"providers,omitempty"`
	// ActiveProvider 当前生效的 provider 名字（空=未使用收藏机制）。
	ActiveProvider string `json:"active_provider,omitempty"`
}

// Provider 一个已记住的 LLM 供应商配置。
type Provider struct {
	Name          string `json:"name"`                     // 引用名（/provider use <name>）
	BaseURL       string `json:"base_url"`                 // OpenAI 兼容端点
	APIKey        string `json:"api_key,omitempty"`        // Bearer token
	Model         string `json:"model,omitempty"`          // 该 provider 偏好模型（空=沿用当前）
	ContextWindow int    `json:"context_window,omitempty"` // 该 provider 的上下文窗口（0=不改）
}

// FindProvider 按名字查找 provider（不区分大小写）。
func (c *Config) FindProvider(name string) *Provider {
	if c == nil || name == "" {
		return nil
	}
	for i := range c.Providers {
		if strings.EqualFold(c.Providers[i].Name, name) {
			return &c.Providers[i]
		}
	}
	return nil
}

// UpsertProvider 新增或覆盖同名 provider（返回归一化后的名字）。
func (c *Config) UpsertProvider(p Provider) string {
	if c == nil {
		return ""
	}
	if p.Name == "" {
		p.Name = deriveProviderName(p.BaseURL)
	}
	for i := range c.Providers {
		if strings.EqualFold(c.Providers[i].Name, p.Name) {
			c.Providers[i] = p
			return c.Providers[i].Name
		}
	}
	c.Providers = append(c.Providers, p)
	return p.Name
}

// RemoveProvider 删除 provider，返回是否存在。
func (c *Config) RemoveProvider(name string) bool {
	if c == nil {
		return false
	}
	for i := range c.Providers {
		if strings.EqualFold(c.Providers[i].Name, name) {
			c.Providers = append(c.Providers[:i], c.Providers[i+1:]...)
			if strings.EqualFold(c.ActiveProvider, name) {
				c.ActiveProvider = ""
			}
			return true
		}
	}
	return false
}

// DeriveProviderName 从 baseURL 推导 provider 名（host 去掉常见前缀，失败用 custom）。
func DeriveProviderName(baseURL string) string { return deriveProviderName(baseURL) }

func deriveProviderName(baseURL string) string {
	s := strings.TrimSpace(baseURL)
	if s == "" {
		return "custom"
	}
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	// 去掉端口
	if i := strings.LastIndex(s, ":"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "custom"
	}
	return s
}

// WebConfig Web UI 配置。
type WebConfig struct {
	Enabled  bool   `json:"enabled"`            // 是否启用 Web UI（-serve 下默认启用）
	Username string `json:"username,omitempty"` // 登录用户名（空=admin）
	Password string `json:"password,omitempty"` // 登录密码（空=首次启动随机生成并写回配置）
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
//
// 向后兼容 + 自动升级：旧版配置只有单个 BaseURL/APIKey/Model，新版引入
// providers 收藏夹。Load 时若发现「有 base_url 但没有 providers」，自动把当前
// 配置登记为一条 provider（名字由 host 推导）并设为 active，然后原地写回文件；
// 写回前先把原文件备份为 <path>.bak（仅当备份不存在时），保证旧版仍可回退使用。
// 新版读取旧文件完全兼容（缺字段按零值处理），旧版读取新版文件也只是忽略新增字段。
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
	if c.upgradeLegacy() {
		// 备份一次原文件（不覆盖已有备份，避免把用户最早的格式丢掉）
		bak := path + ".bak"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			_ = os.WriteFile(bak, data, 0o600)
		}
		if err := Save(path, c); err != nil {
			// 升级写回失败不阻断使用：内存里的配置已经可用
			return c, nil
		}
	}
	return c, nil
}

// upgradeLegacy 把「单供应商」旧配置迁移到 providers 收藏夹。
// 返回是否发生了迁移（true = 需要写回文件）。
//
// 规则：
//   - 有 providers → 不动（已升级过；active 为空时按 base_url 匹配补上）
//   - 无 providers 且有 base_url → 用 base_url 推导名字，登记为第一条 provider
//   - 无 providers 且无 base_url → 不动（尚未配置，等 wizard/用户填）
func (c *Config) upgradeLegacy() bool {
	if c == nil {
		return false
	}
	if len(c.Providers) > 0 {
		// 已有收藏夹：尽量把 active 补齐（老文件可能只有 providers 没有 active）
		if c.ActiveProvider == "" {
			for _, p := range c.Providers {
				if strings.EqualFold(p.BaseURL, c.BaseURL) {
					c.ActiveProvider = p.Name
					return true
				}
			}
			if c.BaseURL != "" {
				c.ActiveProvider = c.Providers[0].Name
				return true
			}
		}
		return false
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return false
	}
	name := deriveProviderName(c.BaseURL)
	c.Providers = []Provider{{
		Name:          name,
		BaseURL:       c.BaseURL,
		APIKey:        c.APIKey,
		Model:         c.Model,
		ContextWindow: c.ContextWindow,
	}}
	c.ActiveProvider = name
	return true
}

// ============================================================================
// 模型探测（wizard 用）
// ============================================================================

// ListModels 从 /v1/models 拉取模型列表。
func ListModels(baseURL, apiKey string) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	// 模型清单 URL：优先 OpenAI 规范路径 /v1/models。
	// 用户习惯把 baseURL 配成 http://host/v1（含 /v1）或 http://host（不带）。
	b := strings.TrimSuffix(baseURL, "/")
	candidates := []string{b + "/models"}
	if !strings.HasSuffix(b, "/v1") {
		candidates = append([]string{b + "/v1/models"}, candidates...) // /v1/models 优先
	}
	var lastErr error
	for _, u := range candidates {
		models, err := fetchModels(client, u, apiKey)
		if err == nil {
			return models, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// fetchModels 请求单个模型清单 URL。
func fetchModels(client *http.Client, url, apiKey string) ([]string, error) {
	req, err := http.NewRequest("GET", url, nil)
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
