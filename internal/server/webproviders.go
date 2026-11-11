package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/config"
)

// ============================================================================
// Web UI：多供应商收藏夹 + 状态栏统计
//
//   GET    /web/api/providers                 列出收藏夹 + 当前生效
//   POST   /web/api/providers {action:add|use|del, name, base_url, api_key, model}
//   GET    /web/api/status[?id=<会话id>]       状态栏数据（tokens/上下文/缓存/速度）
//
// 收藏夹与 TUI 共用 ~/.mizar/config.json 的 providers 字段，两端互相同步。
// ============================================================================

// providerView 返回给前端的供应商信息（api_key 脱敏）。
type providerView struct {
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	APIKeyMask    string `json:"api_key_mask"`
	Model         string `json:"model,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
}

func maskAPIKey(k string) string {
	if k == "" {
		return ""
	}
	r := []rune(k)
	if len(r) <= 6 {
		return "***"
	}
	return string(r[:3]) + "..." + string(r[len(r)-2:])
}

// handleWebProviders GET(列表) / POST(增删切换) /web/api/providers
func (s *Server) handleWebProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		cfg = &config.Config{}
	}
	switch r.Method {
	case http.MethodGet:
		writeProviderList(w, s, cfg)
	case http.MethodPost:
		var body struct {
			Action    string `json:"action"`
			Name      string `json:"name"`
			BaseURL   string `json:"base_url"`
			APIKey    string `json:"api_key"`
			Model     string `json:"model"`
			ReloadCfg bool   `json:"reload"` // action=use 时可指定从磁盘重载
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid json")
			return
		}
		switch strings.ToLower(body.Action) {
		case "add", "update":
			if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.BaseURL) == "" {
				errJSON(w, http.StatusBadRequest, "name 与 base_url 必填")
				return
			}
			p := config.Provider{Name: strings.TrimSpace(body.Name), BaseURL: strings.TrimSuffix(strings.TrimSpace(body.BaseURL), "/")}
			// api_key 留空 = 复用同名已有 key（避免前端只改 URL 时把 key 清空）
			if body.APIKey != "" {
				p.APIKey = body.APIKey
			} else if old := cfg.FindProvider(p.Name); old != nil {
				p.APIKey = old.APIKey
			}
			p.Model = strings.TrimSpace(body.Model)
			if p.Model == "" {
				p.Model = s.agent.Model()
			}
			cfg.UpsertProvider(p)
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				errJSON(w, http.StatusInternalServerError, "保存失败: "+err.Error())
				return
			}
		case "del", "delete", "rm":
			if !cfg.RemoveProvider(body.Name) {
				errJSON(w, http.StatusNotFound, "未找到供应商 "+body.Name)
				return
			}
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				errJSON(w, http.StatusInternalServerError, "保存失败: "+err.Error())
				return
			}
		case "use", "switch":
			p := cfg.FindProvider(body.Name)
			if p == nil {
				errJSON(w, http.StatusNotFound, "未找到供应商 "+body.Name)
				return
			}
			s.applyProviderConfig(*p)
			cfg.ActiveProvider = p.Name
			cfg.BaseURL = p.BaseURL
			cfg.APIKey = p.APIKey
			if p.Model != "" {
				cfg.Model = p.Model
			}
			if p.ContextWindow > 0 {
				cfg.ContextWindow = p.ContextWindow
			}
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				errJSON(w, http.StatusInternalServerError, "已切换但保存失败: "+err.Error())
				return
			}
		default:
			errJSON(w, http.StatusBadRequest, "未知 action: "+body.Action)
			return
		}
		writeProviderList(w, s, cfg)
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// writeProviderList 输出当前供应商列表。
func writeProviderList(w http.ResponseWriter, s *Server, cfg *config.Config) {
	list := make([]providerView, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		list = append(list, providerView{
			Name: p.Name, BaseURL: p.BaseURL, APIKeyMask: maskAPIKey(p.APIKey),
			Model: p.Model, ContextWindow: p.ContextWindow,
		})
	}
	baseURL, _ := s.agent.LLMInfo()
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": list,
		"active":    cfg.ActiveProvider,
		"current":   map[string]any{"base_url": baseURL, "model": s.agent.Model()},
	})
}

// applyProviderConfig 把供应商配置应用到运行中的 LLM 与压缩器。
func (s *Server) applyProviderConfig(p config.Provider) {
	if s.agent == nil {
		return
	}
	type endpointSetter interface {
		SetEndpoint(baseURL, apiKey string)
	}
	if es, ok := s.agent.LLM.(endpointSetter); ok {
		es.SetEndpoint(p.BaseURL, p.APIKey)
	}
	if p.Model != "" {
		type modelSwitcher interface{ SetModel(string) }
		if ms, ok := s.agent.LLM.(modelSwitcher); ok {
			ms.SetModel(p.Model)
		}
	}
	if p.ContextWindow > 0 && s.agent.Compactor != nil {
		s.agent.Compactor.ContextWindow = p.ContextWindow
	}
	// 模型切换后速度统计重置
	s.stats.resetModel()
}

// handleWebStatus GET /web/api/status[?id=会话id]
// 返回 Web UI 状态栏所需数据（对齐 TUI 状态栏：tokens / 上下文 / 缓存 / tok-s / 模型 / 思考）。
func (s *Server) handleWebStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.stats.snapshot()

	thinking := "auto"
	if t, ok := s.agent.LLM.(interface{ ThinkingEnabled() string }); ok {
		if v := t.ThinkingEnabled(); v != "" {
			thinking = v
		}
	}

	// 上下文占用：估算当前会话消息 tokens / (窗口 - 预留)
	ctxPct := 0.0
	ctxEst := 0
	ctxLimit := 0
	if s.agent.Compactor != nil {
		ctxLimit = s.agent.Compactor.ContextWindow - s.agent.Compactor.ReserveTokens
	}
	if id := r.URL.Query().Get("id"); id != "" && ctxLimit > 0 {
		if c := s.web.store.get(id); c != nil {
			msgs := make([]agent.Message, 0, len(c.Messages))
			for _, m := range c.Messages {
				msgs = append(msgs, agent.Message{Role: m.Role, Content: m.Content})
			}
			ctxEst = agent.EstimateMessages(msgs)
			ctxPct = float64(ctxEst) / float64(ctxLimit) * 100
		}
	}

	cacheHit := 0.0
	haveCache := snap.CumCached > 0 && snap.CumPrompt > 0
	if haveCache {
		cacheHit = float64(snap.CumCached) / float64(snap.CumPrompt) * 100
	}

	// 工作目录 / git 分支 / MCP 状态（对齐 TUI 项目行）
	workDir := s.agent.WorkDir
	git := collectGitInfo(workDir)
	mcpConf, mcpConn := 0, 0
	if s.cfg.MCPStats != nil {
		mcpConf, mcpConn = s.cfg.MCPStats()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project": map[string]any{
			"dir":      shortDirPath(workDir),
			"dir_full": workDir,
			"is_git":   git.IsGit,
			"branch":   git.Branch,
			"dirty":    git.Dirty,
			"mcp_conf": mcpConf,
			"mcp_conn": mcpConn,
		},
		"tokens": map[string]any{
			"total":      snap.CumTotal,
			"in":         snap.CumPrompt,
			"out":        snap.CumCompletion,
			"cached":     snap.CumCached,
			"cache_hit":  cacheHit,
			"have_cache": haveCache,
		},
		"context":  map[string]any{"est": ctxEst, "limit": ctxLimit, "pct": ctxPct},
		"speed":    snap.ModelTokPerSec,
		"model":    s.agent.Model(),
		"thinking": thinking,
		"running":  s.agent.Running(),
	})
}
