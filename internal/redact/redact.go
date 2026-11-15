// Package redact 做敏感信息脱敏：会话/导出/日志/工具结果回填前，把疑似密钥、
// 令牌、密码、私钥、手机号等替换成掩码。
//
// 为什么 Mizar 需要这个（2026-11-15 审计发现）：全仓 grep "redact" 零结果，
// 意味着 API key / token 可能以明文留在
//   - 会话 JSONL（~/.mizar/sessions/*.jsonl）
//   - `mizar -export` 导出的 HTML
//   - 诊断日志（~/.mizar/logs/*.log）
//   - 回填给模型的工具结果与错误信息
//
// 设计要点（参照 hermes agent/redact.py 的实战经验）：
//   - **前缀族**优先：各家 key 有固定前缀（sk-/ghp_/AKIA/AQ./xoxb-/AIza…），
//     前缀 + 足够长的 body 才判为密钥，避免把 sk-prod-deployment-01 这类
//     普通标识符误杀（要求 body 里有 ≥16 位连续字母数字且大小写/数字混排）
//   - **掩码保留可调试性**：短 token 全掩；长 token 留前 4 后 4（sk-ab...wxyz）
//   - 私钥 PEM 块整段掩码
//   - 手机号（E.164 与国内 11 位）opt-in，默认关闭（正常对话里常出现手机号）
//   - 默认**不**动普通文本，只匹配高置信度模式，宁可漏报不误报
package redact

import (
	"regexp"
	"strings"
)

// Mask 掩码符号。
const Mask = "***"

// minBodyRun 前缀密钥 body 里要求的「连续字母数字」最小长度。
// 用于排除 sk-prod-deployment-01 这类人类可读标识符（它们有连字符分隔）。
const minBodyRun = 16

// 前缀族：命中后还要过 looksLikeSecretBody 才算真密钥。
var prefixPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_.\-]{12,}`),       // OpenAI / Anthropic / 各类 sk-*
	regexp.MustCompile(`ctx7sk-[A-Za-z0-9_\-]{10,}`),    // Context7
	regexp.MustCompile(`ghp_[A-Za-z0-9]{10,}`),          // GitHub PAT classic
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{10,}`),  // GitHub PAT fine-grained
	regexp.MustCompile(`gh[ousr]_[A-Za-z0-9]{10,}`),     // GitHub OAuth/other
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`), // Slack
	regexp.MustCompile(`xapp-\d+-[A-Za-z0-9\-]{10,}`),   // Slack app-level
	regexp.MustCompile(`AIza[A-Za-z0-9_\-]{30,}`),       // Google API key
	regexp.MustCompile(`AQ\.[A-Za-z0-9_\-]{40,}`),       // Gemini authorization key
	regexp.MustCompile(`ya29\.[A-Za-z0-9_\-]{20,}`),     // Google OAuth access token
	regexp.MustCompile(`GOCSPX-[A-Za-z0-9_\-]{8,}`),     // Google OAuth client secret
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),              // AWS access key id
	regexp.MustCompile(`sk_live_[A-Za-z0-9]{10,}`),      // Stripe live
	regexp.MustCompile(`sk_test_[A-Za-z0-9]{10,}`),      // Stripe test
	regexp.MustCompile(`rk_live_[A-Za-z0-9]{10,}`),      // Stripe restricted
	regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{10,}`),       // SendGrid
	regexp.MustCompile(`hf_[A-Za-z0-9]{10,}`),           // HuggingFace
	regexp.MustCompile(`r8_[A-Za-z0-9]{10,}`),           // Replicate
	regexp.MustCompile(`npm_[A-Za-z0-9]{10,}`),          // npm
	regexp.MustCompile(`pypi-[A-Za-z0-9_\-]{10,}`),      // PyPI
	regexp.MustCompile(`dop_v1_[A-Za-z0-9]{10,}`),       // DigitalOcean
	regexp.MustCompile(`pplx-[A-Za-z0-9]{10,}`),         // Perplexity
	regexp.MustCompile(`fal_[A-Za-z0-9_\-]{10,}`),       // Fal.ai
	regexp.MustCompile(`fc-[A-Za-z0-9]{10,}`),           // Firecrawl
	regexp.MustCompile(`gAAAA[A-Za-z0-9_=\-]{20,}`),     // Codex encrypted token
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{10,}`),     // GitLab PAT
}

// 上下文族：`password=xxx` / `"api_key": "xxx"` 这类「键名 + 值」。
// 只替换值，保留键名（便于排查是哪一项）。
// 约定：**最后一个捕获组是「值」**（其余是键名/前缀）。
var kvPatterns = []*regexp.Regexp{
	// JSON/YAML/ini 形态：键名可能带引号，冒号/等号后可能有引号
	regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|access[_-]?token|auth[_-]?token|secret[_-]?key|client[_-]?secret|private[_-]?key)["']?\s*[:=]\s*["']?([^"'\s,;}]{8,})["']?`),
	regexp.MustCompile(`(?i)\b(password|passwd|pwd)["']?\s*[:=]\s*["']?([^"'\s,;}]{4,})["']?`),
	// HTTP 头：Bearer <token>
	regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9_\-\.=]{16,})`),
}

// 私钥 PEM 块（整段掩码）。
var pemPattern = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

// 手机号（opt-in）：E.164 与国内 11 位。
var phonePattern = regexp.MustCompile(`(?:\+?86[\s-]?)?1[3-9]\d{9}\b|\+\d{1,3}[\s-]?\d{6,14}\b`)

// option 控制可选规则。
type options struct {
	phones bool
}

// Option 配置项。
type Option func(*options)

// WithPhoneRedaction 开启手机号脱敏（默认关闭——正常对话里常出现手机号）。
func WithPhoneRedaction(on bool) Option {
	return func(o *options) { o.phones = on }
}

// Text 脱敏一段文本。opts 控制可选规则。
func Text(s string, opts ...Option) string {
	if s == "" {
		return s
	}
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	out := s

	// 1. 私钥块（先做，避免内部内容触发其他规则）
	out = pemPattern.ReplaceAllString(out, "-----REDACTED PRIVATE KEY-----")

	// 2. 前缀族（需过 body 校验，避免误杀普通标识符）
	for _, re := range prefixPatterns {
		out = replaceAllFunc(out, re, func(m string) string {
			if !looksLikeSecretBody(m) {
				return m // 形似但不像真密钥（如 sk-prod-deployment-01）
			}
			return MaskToken(m)
		})
	}

	// 3. 键值对（保留键名，掩码值）
	for _, re := range kvPatterns {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if len(sub) < 2 {
				return m
			}
			val := sub[len(sub)-1] // 约定：最后一个捕获组是值
			if val == "" || looksLikePlaceholder(val) {
				return m
			}
			return strings.Replace(m, val, MaskToken(val), 1)
		})
	}

	// 4. 手机号（opt-in）
	if o.phones {
		out = phonePattern.ReplaceAllStringFunc(out, func(m string) string {
			return MaskToken(m)
		})
	}
	return out
}

// replaceAllFunc 对匹配位置做替换（regexp 没提供 Func 版本，包一层）。
func replaceAllFunc(s string, re *regexp.Regexp, fn func(string) string) string {
	var sb strings.Builder
	last := 0
	for _, loc := range re.FindAllStringIndex(s, -1) {
		sb.WriteString(s[last:loc[0]])
		sb.WriteString(fn(s[loc[0]:loc[1]]))
		last = loc[1]
	}
	if last == 0 {
		return s
	}
	sb.WriteString(s[last:])
	return sb.String()
}

// strongPrefixes 本身足够独特的厂商前缀：只要前缀命中就直接判为密钥，
// 不再要求 body 有 ≥16 位连续字母数字。
//
// 为什么需要豁免：Slack 的 `xoxb-11223344-EXAMPLE-TOKEN` body 是
// 「两段各 12 位」用连字符分隔，通不过 body 连续性检查，但它确实只可能是 token。
// 而 `sk-` 这种两个字母的前缀太泛（sk-prod-deployment-01 也是合法标识符），
// 必须保留 body 校验。
var strongPrefixes = []string{
	"xox", "xapp-", "ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_",
	"AKIA", "AIza", "AQ.", "ya29.", "GOCSPX-", "glpat-", "ctx7sk-",
	"sk_live_", "sk_test_", "rk_live_", "SG.", "pypi-", "dop_v1_", "doo_v1_",
	"gAAAA", "npm_", "pplx-", "fal_", "bb_live_",
}

// hasStrongPrefix 报告 token 是否以足够独特的厂商前缀开头。
func hasStrongPrefix(token string) bool {
	for _, p := range strongPrefixes {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}

// looksLikeSecretBody 判断「前缀 + body」是否像真密钥。
//
// 规则：body 里要有一段 ≥16 位的连续字母数字，且整 token 至少 20 字符。
// 这样 sk-prod-deployment-01 这种人类可读标识符不会被误杀。
// 强独特前缀（strongPrefixes）直接放行，不做 body 校验。
func looksLikeSecretBody(token string) bool {
	if len(token) < 12 {
		return false
	}
	if hasStrongPrefix(token) {
		return true
	}
	// 去掉已知短前缀后看剩余部分
	body := token
	if i := strings.IndexAny(token, "-_."); i >= 0 && i < len(token)-1 {
		// 只切掉第一段前缀（sk- / ghp_ / AQ. 等）
		body = token[i+1:]
	}
	// AWS AKIA 是固定 16 位大写，没有分隔符
	if strings.HasPrefix(token, "AKIA") {
		return len(token) == 20
	}
	run := 0
	for _, r := range body {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			run++
			if run >= minBodyRun {
				return true
			}
		} else {
			run = 0
		}
	}
	// 特殊：全大写的长串（如 gAAAA 开头）
	return false
}

// looksLikePlaceholder 已是掩码/占位符的值不再二次处理。
func looksLikePlaceholder(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "*", "***", "xxxx", "xxxxx", "null", "none", "true", "false", "nil":
		return true
	}
	return strings.Trim(v, "*xX") == ""
}

// MaskToken 生成掩码：短 token 全掩；长 token 留前 4 后 4。
// 例：sk-abcdefghijklmnop → sk-a***mnop
func MaskToken(tok string) string {
	const floor = 16
	if len(tok) < floor {
		return Mask
	}
	head, tail := 4, 4
	if len(tok) < head+tail+4 {
		return Mask
	}
	return tok[:head] + Mask + tok[len(tok)-tail:]
}

// Enabled 全局开关（默认开启——脱敏是安全默认值）。
var enabled = true

// SetEnabled 设置全局开关（config security.redact 控制）。
func SetEnabled(on bool) { enabled = on }

// IsEnabled 报告全局开关。
func IsEnabled() bool { return enabled }

// Apply 按全局开关脱敏（业务代码统一走这个入口）。
func Apply(s string) string {
	if !enabled {
		return s
	}
	return Text(s)
}
