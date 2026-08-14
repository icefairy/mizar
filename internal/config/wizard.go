// 首次运行向导（wizard）：交互式配置供应商/模型/思考/上下文。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// errScanEOF 输入流结束（测试/EOF 用）。
var errScanEOF = errors.New("EOF")

// parseWizardCmd 解析 "/cmd args" 行；非命令返回空。
func parseWizardCmd(line string) (name, args string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return "", ""
	}
	fields := strings.Fields(trimmed)
	name = strings.TrimPrefix(fields[0], "/")
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	}
	return name, args
}

// wizard 交互式向导（依赖注入便于测试）。
type wizard struct {
	cfg           *Config
	scan          func() (string, error)
	save          func() error
	listModels    func(baseURL, apiKey string) ([]string, error)
	probeThinking func(baseURL, apiKey, model string) (bool, error)
	probeContext  func(baseURL, model string) int
}

// Run 运行向导，返回最终输出摘要。
func (w *wizard) Run() (string, error) {
	var out strings.Builder
	p := func(format string, a ...any) { out.WriteString(fmt.Sprintf(format, a...)) }
	p("Mizar (开阳) 初始化向导\n输入命令配置，/help 查看全部命令。\n\n")

	for {
		fmt.Print("mizar> ")
		line, err := w.scan()
		if err != nil {
			return out.String(), nil // EOF 静默退出
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, args := parseWizardCmd(line)
		switch name {
		case "help":
			p("%s\n", w.helpText())
		case "provider":
			w.cmdProvider(p, args)
		case "model":
			w.cmdModel(p, args)
		case "think":
			w.cmdThink(p, args)
		case "context":
			w.cmdContext(p, args)
		case "save":
			if err := w.save(); err != nil {
				p("保存失败: %v\n", err)
				continue
			}
			p("配置已保存 ✓\n当前配置：%s\n", w.cfg.String())
			return out.String(), nil
		case "quit", "exit":
			p("退出向导（未保存）\n")
			return out.String(), nil
		default:
			if name == "" {
				// 非命令输入：提示
				p("未知输入（不是命令）。输入 /help 查看命令列表。\n")
			} else {
				p("未知命令 /%s。输入 /help 查看命令列表。\n", name)
			}
		}
	}
}

func (w *wizard) helpText() string {
	return `命令列表：
  /provider [url]  设置供应商（OpenAI 兼容端点），可选直接带 URL
  /model [name]    列出/选择当前供应商可用模型
  /think [on|off]  切换思考模式（空=自动探测是否支持）
  /context [N|auto] 设置上下文窗口 token（auto=自动探测）
  /save            保存配置到 ~/.mizar/config.json
  /quit            退出（不保存）
  /help            显示本帮助`
}

// cmdProvider 添加/切换供应商。
func (w *wizard) cmdProvider(p func(string, ...any), args string) {
	baseURL := strings.TrimSpace(args)
	if baseURL == "" {
		p("输入供应商地址（OpenAI 兼容端点，如 http://127.0.0.1:3002/v1）：\n")
		v, err := w.scan()
		if err != nil {
			return
		}
		baseURL = strings.TrimSpace(v)
	}
	if baseURL == "" {
		p("供应商地址不能为空。\n")
		return
	}
	if !strings.HasPrefix(baseURL, "http") {
		baseURL = "http://" + baseURL
	}
	w.cfg.BaseURL = baseURL

	p("输入 API Key（没有直接回车）：\n")
	key, err := w.scan()
	if err == nil {
		w.cfg.APIKey = strings.TrimSpace(key)
	}

	// 验证连接
	models, err := w.listModels(w.cfg.BaseURL, w.cfg.APIKey)
	if err != nil {
		p("连接失败: %v\n（仍会保存该供应商，可用 /model 重试）\n", err)
		return
	}
	p("✓ 供应商连接成功，发现 %d 个模型：\n", len(models))
	for _, m := range models {
		p("  - %s\n", m)
	}
}

// cmdModel 列出并选择模型。
func (w *wizard) cmdModel(p func(string, ...any), args string) {
	if w.cfg.BaseURL == "" {
		p("请先设置供应商：/provider <url>\n")
		return
	}
	models, err := w.listModels(w.cfg.BaseURL, w.cfg.APIKey)
	if err != nil {
		p("获取模型列表失败: %v\n", err)
		return
	}
	if len(models) == 0 {
		p("供应商未返回任何模型。\n")
		return
	}
	if args != "" {
		// 直接指定
		for _, m := range models {
			if m == args {
				w.cfg.Model = m
				p("✓ 已选择模型: %s\n", m)
				return
			}
		}
		p("模型 %q 不在列表，可用的有：\n", args)
	}
	// 交互选择
	p("可用模型（输入序号选择，或直接输入模型名）：\n")
	for i, m := range models {
		p("  [%d] %s\n", i+1, m)
	}
	line, err := w.scan()
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(models) {
		w.cfg.Model = models[n-1]
	} else {
		for _, m := range models {
			if m == line {
				w.cfg.Model = m
				break
			}
		}
	}
	if w.cfg.Model == "" {
		p("未选择有效模型。\n")
		return
	}
	p("✓ 已选择模型: %s\n", w.cfg.Model)
}

// cmdThink 思考模式：on/off 或自动探测。
func (w *wizard) cmdThink(p func(string, ...any), args string) {
	arg := strings.ToLower(strings.TrimSpace(args))
	switch arg {
	case "on":
		w.cfg.ThinkingLevel = "medium"
		p("✓ 思考模式已开启\n")
	case "off":
		w.cfg.ThinkingLevel = "off"
		p("✓ 思考模式已关闭\n")
	case "":
		if w.cfg.BaseURL == "" || w.cfg.Model == "" {
			p("需要先配置供应商和模型才能自动探测。手动设置：/think on|off\n")
			return
		}
		ok, err := w.probeThinking(w.cfg.BaseURL, w.cfg.APIKey, w.cfg.Model)
		if err != nil {
			p("探测失败: %v（手动设置 /think on|off）\n", err)
			return
		}
		if ok {
			w.cfg.ThinkingLevel = "medium"
			p("✓ 探测结果：模型支持思考模式，已开启\n")
		} else {
			p("探测结果：模型不支持思考模式，已关闭\n")
		}
	default:
		p("用法：/think [on|off]（空=自动探测）\n")
	}
}

// cmdContext 上下文窗口：数字或 auto。
func (w *wizard) cmdContext(p func(string, ...any), args string) {
	arg := strings.TrimSpace(args)
	if arg == "" {
		p("输入上下文窗口 token 数（或 auto 自动探测）：\n")
		v, err := w.scan()
		if err != nil {
			return
		}
		arg = strings.TrimSpace(v)
	}
	if strings.EqualFold(arg, "auto") {
		if w.cfg.BaseURL == "" || w.cfg.Model == "" {
			p("需要先配置供应商和模型才能自动探测。手动输入数字：/context 128000\n")
			return
		}
		w.cfg.ContextWindow = w.probeContext(w.cfg.BaseURL, w.cfg.Model)
		p("✓ 已自动探测上下文窗口: %d\n", w.cfg.ContextWindow)
		return
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		p("无效数字。用法：/context [数字|auto]\n")
		return
	}
	w.cfg.ContextWindow = n
	p("✓ 上下文窗口已设置: %d\n", n)
}

// String 展示当前配置。
func (c *Config) String() string {
	return fmt.Sprintf("provider=%s model=%s thinking=%v context=%d",
		c.BaseURL, c.Model, c.Thinking, c.ContextWindow)
}

// RunWizard 启动交互式向导（标准输入）。
func RunWizard(savePath string) (*Config, error) {
	cfg, _ := Load(savePath)
	reader := bufio.NewReader(os.Stdin)
	w := &wizard{
		cfg: cfg,
		scan: func() (string, error) {
			line, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			return strings.TrimRight(line, "\r\n"), nil
		},
		save:          func() error { return Save(savePath, cfg) },
		listModels:    ListModels,
		probeThinking: ProbeThinking,
		probeContext:  ProbeContextWindow,
	}
	out, err := w.Run()
	fmt.Print(out)
	return cfg, err
}
