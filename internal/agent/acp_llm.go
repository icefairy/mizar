// acp_llm.go —— ACP CLI 子代理适配层
//
// delegate_task 的 model 字段填 "acp:<cli>"（如 acp:codebuddy）时，
// 子任务不走 mizar agent 循环，而是把任务直接交给本机官方 CLI 的
// ACP 会话：CLI 自带工具（Bash/Read/Edit…），独立完成 goal 后
// 返回最终文本作为子代理 Result。
//
// 为什么不做成 agent.LLM 接口适配器：mizar 的 agent 循环期望 LLM
// 返回控制 JSON（工具调用协议），而官方 CLI 的工具协议是它自己的
// （Bash/Read/…），两套协议强行桥接只会互相扯皮。「CLI 独立执行 +
// 结果回传」才是对的集成层——各自用各自的工具协议。
//
// 实测协议（codebuddy 2.180.0）：
//   serve 探活 → acp/connect → initialize → session/new(mcpServers:[])
//   → session/prompt → 流式 session/update 收集 agent_message_chunk。
package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ACPCliConfig 一个 ACP CLI 的连接参数（delegate 时从配置文件读）。
type ACPCliConfig struct {
	Name         string `json:"name"`
	ServePort    int    `json:"serve_port"`
	HealthPath   string `json:"health_path"`
	HealthHeader string `json:"health_header"`
	PwLogFiles   string `json:"pw_log_files"` // 分号分隔
	PwRegex      string `json:"pw_regex"`
	ExtraHeader  string `json:"extra_header"` // 形如 "X-Foo: 1"；分号分隔可多个
	LoginHint    string `json:"login_hint"`
}

// acpCLIRegistry 默认注册表 + 用户覆盖（~/.mizar/config.json 的 acp_clis）。
// codebuddy 内置（实测过），其余环境在配置文件登记。
var acpCLIRegistry = map[string]ACPCliConfig{
	"codebuddy": {
		Name:         "CodeBuddy CLI",
		ServePort:    3011,
		HealthPath:   "/api/v1/auth/status",
		HealthHeader: "x-codebuddy-request: 1",
		PwLogFiles:  homeDir() + "/.mizar/logs/codebuddy-serve.log;/tmp/codebuddy-serve.log;/var/log/codebuddy-serve.log",
		PwRegex:      `Password(?:\x1b\[[0-9;]*m|\s)+([A-Za-z0-9_\-]+)`,
		ExtraHeader:  "x-codebuddy-request: 1",
		LoginHint:    "本机执行 codebuddy /login（或从 Windows 侧同步 auth 文件）",
	},
}

// ACPCliNames 返回已登记的 CLI 名（delegate_task 描述用）。
func ACPCliNames() []string {
	loadACPUserConfig()
	names := make([]string, 0, len(acpCLIRegistry))
	for k := range acpCLIRegistry {
		names = append(names, k)
	}
	return names
}

func loadACPUserConfig() {
	b, err := os.ReadFile(homeDir() + "/.mizar/config.json")
	if err != nil {
		return
	}
	var cfg struct {
		ACPCLIs map[string]ACPCliConfig `json:"acp_clis"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return
	}
	for k, v := range cfg.ACPCLIs {
		if v.ServePort <= 0 {
			continue
		}
		c := acpCLIRegistry[k]
		if v.HealthPath != "" {
			c.HealthPath = v.HealthPath
		}
		if v.HealthHeader != "" {
			c.HealthHeader = v.HealthHeader
		}
		if v.PwLogFiles != "" {
			c.PwLogFiles = v.PwLogFiles
		}
		if v.PwRegex != "" {
			c.PwRegex = v.PwRegex
		}
		if v.LoginHint != "" {
			c.LoginHint = v.LoginHint
		}
		c.ServePort = v.ServePort
		if v.ExtraHeader != "" {
			c.ExtraHeader = v.ExtraHeader
		}
		acpCLIRegistry[k] = c
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/root"
}

// RunACPSubagent 把一个子任务交给官方 CLI 执行。
// 返回 CLI 的最终文本回复。dep 是进度回调（nil 安全）。
func RunACPSubagent(cli, goal, contextMsg, workDir string, timeoutSecs int, dep func(string)) (string, error) {
	loadACPUserConfig()
	cfg, ok := acpCLIRegistry[cli]
	if !ok {
		return "", fmt.Errorf("acp: 未登记的 CLI %q", cli)
	}
	if timeoutSecs <= 0 {
		timeoutSecs = 300
	}
	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)

	if dep != nil {
		dep("acp: 探活 " + cli + " serve")
	}
	// serve 探活；不通则尝试 watchdog/直拉（与 acp_cli.ts 插件同一套策略）
	if !acpProbe(cfg, 3*time.Second) {
		if dep != nil {
			dep("acp: serve 未跑，尝试拉起")
		}
		_ = exec.Command("sh", "-c",
			"cd "+acpWorkDir(workDir)+" && /data/codes/mizar/extensions/acp_watchdog start >/dev/null 2>&1; true").Run()
		for i := 0; i < 5; i++ {
			if acpProbe(cfg, 3*time.Second) {
				break
			}
			time.Sleep(3 * time.Second)
		}
		if !acpProbe(cfg, 3*time.Second) {
			return "", fmt.Errorf("acp: serve 无法就绪（端口 %d）", cfg.ServePort)
		}
	}

	// 密码
	pw, err := acpPassword(cli, cfg)
	if err != nil {
		return "", err
	}

	// codebuddy --serve 认证协议（2026-11 实测）：
	// 1. POST /api/v1/auth/login {password} → Set-Cookie gateway_session=...
	// 2. 之后所有请求带该 Cookie + 头 x-codebuddy-request（任意值）
	// 3. /api/v1/acp/connect → {connectionId, sessionToken}
	// cookie 进程内+磁盘（~/.mizar/）双缓存，401 时自动重登。
	client := &http.Client{Timeout: 0} // 无硬超时，靠 deadline 手动管
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.ServePort)

	// acpLogin 走 login 换新 cookie 并写缓存。
	acpLogin := func() (string, error) {
		loginReq, err := http.NewRequest("POST", base+"/api/v1/auth/login", bytes.NewReader([]byte(`{"password":"`+pw+`"}`)))
		if err != nil {
			return "", err
		}
		loginReq.Header.Set("Content-Type", "application/json")
		loginResp, err := client.Do(loginReq)
		if err != nil {
			return "", fmt.Errorf("acp login: %w", err)
		}
		cookie := ""
		for _, c := range loginResp.Cookies() {
			if c.Name == "gateway_session" {
				cookie = c.Name + "=" + c.Value
			}
		}
		loginResp.Body.Close()
		if cookie == "" {
			return "", fmt.Errorf("acp: 登录成功但未拿到 gateway_session cookie（%s）", cfg.LoginHint)
		}
		acpSaveCookie(cfg.ServePort, cli, cookie)
		return cookie, nil
	}

	cookie := acpLoadCookie(cfg.ServePort, cli)
	if cookie == "" {
		if dep != nil {
			dep("acp: login（首次，cookie 已缓存到 ~/.mizar/）")
		}
		var err error
		if cookie, err = acpLogin(); err != nil {
			return "", err
		}
	}

	post := func(path, body string, extra map[string]string, timeout time.Duration) ([]byte, error) {
		req, err := http.NewRequest("POST", base+path, bytes.NewReader([]byte(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Cookie", cookie)
		req.Header.Set("x-codebuddy-request", "1")
		for _, h := range strings.Split(cfg.ExtraHeader, ";") {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if i := strings.Index(h, ":"); i > 0 {
				req.Header.Set(strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:]))
			}
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		if timeout > 0 {
			c := *client
			c.Timeout = timeout
			resp, err := c.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	}

	if dep != nil {
		dep("acp: connect + initialize + session/new")
	}
	// connect
	cb, err := post("/api/v1/acp/connect", "{}", nil, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("acp connect: %w", err)
	}
	var conn struct {
		ConnectionID string `json:"connectionId"`
		SessionToken string `json:"sessionToken"`
	}
	if json.Unmarshal(cb, &conn) != nil || conn.ConnectionID == "" {
		return "", fmt.Errorf("acp connect 响应异常: %.200s", string(cb))
	}
	extra := map[string]string{
		"acp-connection-id": conn.ConnectionID,
		"X-ACP-Session-Token": conn.SessionToken,
	}

	rpcCall := func(id int, method string, params interface{}) (json.RawMessage, error) {
		body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		b, err := post("/api/v1/acp", string(body), extra, time.Until(deadline))
		if err != nil {
			return nil, err
		}
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		// SSE 或纯 JSON 双兼容
		scanner := bufio.NewScanner(strings.NewReader(string(b)))
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var j struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(line[6:]), &j) != nil {
				continue
			}
			if j.ID != nil && *j.ID == id {
				if j.Error != nil {
					return nil, fmt.Errorf("acp %s: %s", method, j.Error.Message)
				}
				return j.Result, nil
			}
		}
		// 纯 JSON 回退
		if json.Unmarshal(b, &resp) == nil && resp.Result != nil {
			return resp.Result, nil
		}
		return nil, fmt.Errorf("acp %s: 无响应", method)
	}

	if _, err := rpcCall(1, "initialize", map[string]interface{}{"protocolVersion": 1, "clientCapabilities": map[string]interface{}{}}); err != nil {
		return "", err
	}
	var sn struct {
		SessionID string `json:"sessionId"`
	}
	snb, err := rpcCall(2, "session/new", map[string]interface{}{
		"cwd": acpWorkDir(workDir), "mcpServers": []interface{}{},
	})
	if err != nil {
		return "", err
	}
	if json.Unmarshal(snb, &sn) != nil || sn.SessionID == "" {
		return "", fmt.Errorf("acp session/new 响应异常: %.200s", string(snb))
	}

	// prompt（流式收 agent_message_chunk；正文不在最终 result）
	task := goal
	if contextMsg != "" {
		task = goal + "\n\n【子任务背景/上下文】\n" + contextMsg
	}
	pb, _ := json.Marshal(map[string]interface{}{
		"sessionId": sn.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": task}},
	})
	req, err := http.NewRequest("POST", base+"/api/v1/acp", bytes.NewReader(pb))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("x-codebuddy-request", "1")
	for _, h := range strings.Split(cfg.ExtraHeader, ";") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if i := strings.Index(h, ":"); i > 0 {
			req.Header.Set(strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:]))
		}
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("acp prompt: %w", err)
	}
	defer resp.Body.Close()

	var texts []string
	var gotFinal bool
	var finalErr string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			finalErr = "acp: 子任务超时"
			break
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var j struct {
			Method string `json:"method"`
			ID     *int   `json:"id"`
			Params *struct {
				Update *struct {
					SessionUpdate string `json:"sessionUpdate"`
					Content       interface{} `json:"content"`
				} `json:"update"`
			} `json:"params"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line[6:]), &j) != nil {
			continue
		}
		if j.Error != nil {
			finalErr = "acp prompt: " + j.Error.Message
			break
		}
		if j.Method == "session/update" && j.Params != nil && j.Params.Update != nil {
			u := j.Params.Update
			if u.SessionUpdate == "agent_message" || u.SessionUpdate == "agent_message_chunk" {
				texts = append(texts, acpContentText(u.Content)...)
				if dep != nil && len(texts) > 0 {
					// 每 chunk 推一次进度（尾部 80 字）
					tail := texts[len(texts)-1]
					if len(tail) > 80 {
						tail = tail[len(tail)-80:]
					}
					dep("acp: " + tail)
				}
			}
		}
		if j.ID != nil && *j.ID == 3 {
			gotFinal = true
			if j.Error != nil {
				finalErr = "acp prompt: " + j.Error.Message
			}
			break
		}
	}
	if finalErr != "" && !gotFinal {
		if strings.Contains(finalErr, "Authentication") || strings.Contains(finalErr, "401") {
			return "", fmt.Errorf("acp: %s（%s）", finalErr, cfg.LoginHint)
		}
		return "", fmt.Errorf("%s", finalErr)
	}
	out := strings.TrimSpace(strings.Join(texts, "\n"))
	if out == "" {
		return "", fmt.Errorf("acp: CLI 无文本回复（%s）", cli)
	}
	return out, nil
}

// acpContentText 抽取 content 里的 text（list 或 dict 两种形态）。
func acpContentText(c interface{}) []string {
	var out []string
	switch v := c.(type) {
	case []interface{}:
		for _, it := range v {
			if m, ok := it.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok && m["type"] == "text" {
					out = append(out, t)
				}
			}
		}
	case map[string]interface{}:
		if t, ok := v["text"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}

// acpProbe 探活 serve。
func acpProbe(cfg ACPCliConfig, timeout time.Duration) bool {
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", cfg.ServePort, cfg.HealthPath), nil)
	if err != nil {
		return false
	}
	for _, h := range strings.Split(cfg.HealthHeader, ";") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if i := strings.Index(h, ":"); i > 0 {
			req.Header.Set(strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:]))
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// acpPassword 从启动日志抓密码（与 acp_cli.ts 同策略）。
// acpCookieCache 进程内 cookie 缓存（key: servePort）。
// gateway_session Max-Age 30 天；persist 到 ~/.mizar/ 让重启后复用。
var acpCookieCache = struct {
	mu    sync.Mutex
	value map[int]string
}{value: map[int]string{}}

// acpCookiePath cookie 持久化文件（~/.mizar/，不放 /tmp）。
// acpCookiePath cookie 持久化文件，按 CLI 区分：
// ~/.mizar/acp/<cli>/gateway_session
func acpCookiePath(cli string) string {
	return homeDir() + "/.mizar/acp/" + cli + "/gateway_session"
}

// acpLoadCookie 读缓存：内存 → 磁盘。
func acpLoadCookie(port int, cli string) string {
	acpCookieCache.mu.Lock()
	c, ok := acpCookieCache.value[port]
	acpCookieCache.mu.Unlock()
	if ok && c != "" {
		return c
	}
	if b, err := os.ReadFile(acpCookiePath(cli)); err == nil {
		c = strings.TrimSpace(string(b))
		if c != "" {
			acpCookieCache.mu.Lock()
			acpCookieCache.value[port] = c
			acpCookieCache.mu.Unlock()
			return c
		}
	}
	return ""
}

// acpSaveCookie 写缓存：内存 + 磁盘（0600）。
func acpSaveCookie(port int, cli string, cookie string) {
	acpCookieCache.mu.Lock()
	acpCookieCache.value[port] = cookie
	acpCookieCache.mu.Unlock()
	_ = os.MkdirAll(homeDir()+"/.mizar/acp/"+cli, 0o755)
	_ = os.WriteFile(acpCookiePath(cli), []byte(cookie), 0o600)
}

// acpPwPath 密码缓存文件（~/.mizar/，0600）。
// 敏感凭据不落 /tmp：日志可以在 tmp，缓存凭据必须在家目录。
// acpPwPath 密码缓存文件，按 CLI 区分：
// ~/.mizar/acp/<cli>/serve_pw
func acpPwPath(cli string) string {
	return homeDir() + "/.mizar/acp/" + cli + "/serve_pw"
}

func acpPassword(cli string, cfg ACPCliConfig) (string, error) {
	re := compileACPRegex(cfg.PwRegex)
	if re == nil {
		return "", fmt.Errorf("acp: 密码正则无效")
	}
	// 1) 先读缓存凭据（serve 重启密码不变时直接命中）
	if b, err := os.ReadFile(acpPwPath(cli)); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	// 2) 缓存未命中 → 从日志解析并落缓存
	for _, lf := range strings.Split(cfg.PwLogFiles, ";") {
		lf = strings.TrimSpace(lf)
		if lf == "" {
			continue
		}
		b, err := os.ReadFile(lf)
		if err != nil {
			continue
		}
		if m := re.FindSubmatch(b); len(m) > 1 {
			pw := string(m[1])
			_ = os.MkdirAll(homeDir()+"/.mizar/acp/"+cli, 0o755)
			_ = os.WriteFile(acpPwPath(cli), []byte(pw), 0o600)
			return pw, nil
		}
	}
	return "", fmt.Errorf("acp: 拿不到 serve 密码（检查 %s 与缓存 %s）", cfg.PwLogFiles, acpPwPath(cli))
}

// acpWorkDir 工作目录兜底。
func acpWorkDir(wd string) string {
	if strings.TrimSpace(wd) == "" {
		return "/data/codes/mizar"
	}
	return strings.TrimSpace(wd)
}

func compileACPRegex(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}
