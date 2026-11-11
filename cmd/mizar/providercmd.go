package main

import (
	"fmt"
	"log"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/config"
	"mizar/internal/llm"
)

// ============================================================================
// /provider 命令：多供应商收藏夹 + 切换
//
// 场景：家里直连 + 外面走 Cloudflare Tunnel / 其它上游渠道，各自 baseURL 不同。
// 把这些都记住，随时 /provider use <name> 切换，不必每次重新粘贴 URL/Key。
//
// 配置落盘到 ~/.mizar/config.json：
//   providers: [{name,base_url,api_key,model,context_window}, ...]
//   active_provider: "<当前生效的名字>"
// 同时 BaseURL/APIKey/Model/ContextWindow 仍保持「当前生效值」，与旧版完全兼容。
// ============================================================================

// maskKey 把 API Key 脱敏为 sk-xx...yy。
func maskKey(k string) string {
	if k == "" {
		return "<未设置>"
	}
	r := []rune(k)
	if len(r) <= 6 {
		return "***"
	}
	return string(r[:3]) + "..." + string(r[len(r)-2:])
}

// applyProvider 把某条 provider 应用到运行中的 client + agent 压缩器。
func applyProvider(client *llm.OpenAI, a *agent.Agent, ctxWindow *int, p config.Provider) {
	client.SetEndpoint(p.BaseURL, p.APIKey)
	if p.Model != "" {
		client.SetModel(p.Model)
	}
	if p.ContextWindow > 0 && ctxWindow != nil {
		*ctxWindow = p.ContextWindow
		if a != nil && a.Compactor != nil {
			a.Compactor.ContextWindow = p.ContextWindow
		}
	}
}

// saveProviderConfig 将当前 client 状态 + providers 收藏夹落盘。
// active 为当前生效 provider 名（空=不在收藏夹中）。
func saveProviderConfig(client *llm.OpenAI, ctxWindow *int, active string) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		cfg = &config.Config{}
	}
	cfg.BaseURL = client.BaseURL
	cfg.APIKey = client.APIKey
	cfg.Model = client.Model
	if ctxWindow != nil {
		cfg.ContextWindow = *ctxWindow
	}
	cfg.ThinkingLevel = client.ThinkingEnabled()
	cfg.UserAgent = client.UserAgent()
	if active != "" {
		cfg.ActiveProvider = active
	}
	return config.Save(config.DefaultPath(), cfg)
}

// providerListText 生成 /provider 列表文本。
func providerListText(client *llm.OpenAI, cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("当前生效供应商: %s\n", client.BaseURL))
	sb.WriteString(fmt.Sprintf("  模型 model:     %s\n", client.Model))
	sb.WriteString(fmt.Sprintf("  API Key:        %s\n", maskKey(client.APIKey)))
	if cfg.ActiveProvider != "" {
		sb.WriteString(fmt.Sprintf("  收藏夹选中:     %s\n", cfg.ActiveProvider))
	}
	sb.WriteString("\n已记住的供应商:\n")
	if len(cfg.Providers) == 0 {
		sb.WriteString("  （空，用 /provider add <名称> <baseURL> [apiKey] 添加）\n")
	}
	for _, p := range cfg.Providers {
		mark := " "
		if strings.EqualFold(p.Name, cfg.ActiveProvider) {
			mark = "*"
		}
		line := fmt.Sprintf("  %s %-14s %s  key=%s", mark, p.Name, p.BaseURL, maskKey(p.APIKey))
		if p.Model != "" {
			line += "  model=" + p.Model
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(`
用法:
  /provider add <名称> <baseURL> [apiKey] [model]   添加/覆盖一个供应商
  /provider use <名称>                             切换到该供应商
  /provider del <名称>                             删除
  /provider list                                   列出（同 /provider）
  /provider <baseURL> [apiKey]                     临时切换（并自动收藏）
  /model <name>                                    切换模型（写入当前 provider）`)
	return sb.String()
}

// syncActiveProviderModel 在 /model 切换后，把模型名同步进当前生效的 provider 条目。
func syncActiveProviderModel(model string) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil || cfg.ActiveProvider == "" {
		return
	}
	if p := cfg.FindProvider(cfg.ActiveProvider); p != nil {
		p.Model = model
		if err := config.Save(config.DefaultPath(), cfg); err != nil {
			log.Printf("/provider 同步模型失败: %v", err)
		}
	}
}

// newProviderCommand 构造 /provider 命令（支持 add/del/use/list 子命令）。
func newProviderCommand(client *llm.OpenAI, a *agent.Agent, ctxWindow *int) agent.Command {
	return agent.Command{
		Name:        "provider",
		Description: "管理 LLM 供应商：/provider list | add <名> <url> [key] [model] | use <名> | del <名>",
		Timeout:     0,
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				cfg = &config.Config{}
			}
			fields := strings.Fields(args)
			// 无参数：列出
			if len(fields) == 0 {
				return providerListText(client, cfg), nil
			}
			sub := strings.ToLower(fields[0])
			rest := fields[1:]

			switch sub {
			case "list", "ls":
				return providerListText(client, cfg), nil

			case "add":
				// /provider add <name> <baseURL> [apiKey] [model]
				if len(rest) < 2 {
					return "", fmt.Errorf("用法: /provider add <名称> <baseURL> [apiKey] [model]")
				}
				p := config.Provider{
					Name:    rest[0],
					BaseURL: strings.TrimSuffix(rest[1], "/"),
				}
				if len(rest) >= 3 {
					p.APIKey = rest[2]
				}
				if len(rest) >= 4 {
					p.Model = rest[3]
				}
				// 未指定模型时沿用当前模型，避免切换后空模型
				if p.Model == "" {
					p.Model = client.Model
				}
				name := cfg.UpsertProvider(p)
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %w", err)
				}
				return fmt.Sprintf("✓ 已添加供应商 %q\n  用 /provider use %s 切换过去", name, name), nil

			case "del", "rm", "remove":
				if len(rest) < 1 {
					return "", fmt.Errorf("用法: /provider del <名称>")
				}
				if !cfg.RemoveProvider(rest[0]) {
					return "", fmt.Errorf("未找到供应商 %q", rest[0])
				}
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %w", err)
				}
				return fmt.Sprintf("✓ 已删除供应商 %q", rest[0]), nil

			case "use", "switch":
				if len(rest) < 1 {
					return "", fmt.Errorf("用法: /provider use <名称>")
				}
				p := cfg.FindProvider(rest[0])
				if p == nil {
					return "", fmt.Errorf("未找到供应商 %q（/provider list 查看已有）", rest[0])
				}
				old := client.BaseURL
				applyProvider(client, a, ctxWindow, *p)
				cfg.ActiveProvider = p.Name
				cfg.BaseURL = client.BaseURL
				cfg.APIKey = client.APIKey
				cfg.Model = client.Model
				if ctxWindow != nil {
					cfg.ContextWindow = *ctxWindow
				}
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("已切换但保存失败: %w", err)
				}
				return fmt.Sprintf("✓ 已切换到供应商 %q: %s → %s\n  模型: %s", p.Name, old, client.BaseURL, client.Model), nil
			}

			// 兼容旧行为：/provider <baseURL> [apiKey] —— 切换并自动收藏
			baseURL := strings.TrimSuffix(fields[0], "/")
			if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
				return "", fmt.Errorf("未知子命令 %q（可用: list/add/use/del）", fields[0])
			}
			old := client.BaseURL
			apiKey := client.APIKey
			if len(fields) >= 2 {
				apiKey = fields[1]
			}
			client.SetEndpoint(baseURL, apiKey)
			name := cfg.UpsertProvider(config.Provider{
				Name:    config.DeriveProviderName(baseURL),
				BaseURL: client.BaseURL,
				APIKey:  client.APIKey,
				Model:   client.Model,
			})
			cfg.ActiveProvider = name
			cfg.BaseURL = client.BaseURL
			cfg.APIKey = client.APIKey
			cfg.Model = client.Model
			if ctxWindow != nil {
				cfg.ContextWindow = *ctxWindow
			}
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				log.Printf("/provider 持久化失败: %v", err)
			}
			msg := fmt.Sprintf("✓ 供应商已切换: %s → %s\n✓ 已收藏为 %q（下次 /provider use %s）", old, client.BaseURL, name, name)
			if len(fields) >= 2 {
				msg += "\n✓ API Key 已更新"
			}
			return msg, nil
		},
	}
}
