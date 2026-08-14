package engine

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"time"
)

// countTokensEstimate 估算文本 token 数（保守启发式：CJK 按字符，其他按 4 字符/token）。
// 与 internal/agent 压缩器 EstimateTokens 同口径，保证插件估算与 Agent 压缩触发一致。
func countTokensEstimate(s string) int {
	if s == "" {
		return 0
	}
	chars := 0
	cjk := 0
	for _, r := range s {
		chars++
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		}
	}
	nonCJK := chars - cjk
	return cjk + (nonCJK+3)/4
}

// registerStdlib 注册纯函数宿主函数（无 I/O、无状态、零副作用）。
// 这类函数是插件日用的高频缺口：时间、UUID、编解码、哈希、路径、URL。
// 不依赖 host 配置，引擎创建时无条件注册（与 db_query/mcp_call 不同）。
func registerStdlib(reg func(name string, fn any)) {
	// 时间
	reg("time_now", func() string { return time.Now().UTC().Format(time.RFC3339Nano) })
	reg("time_unix", func() int64 { return time.Now().Unix() })

	// UUID v4（crypto/rand，非时间戳伪随机）
	reg("uuid", func() (string, error) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("uuid: %w", err)
		}
		b[6] = (b[6] & 0x0f) | 0x40 // version 4
		b[8] = (b[8] & 0x3f) | 0x80 // variant 10
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
	})

	// 编解码
	reg("base64_encode", func(s string) string {
		return base64.StdEncoding.EncodeToString([]byte(s))
	})
	reg("base64_decode", func(s string) (string, error) {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return "", fmt.Errorf("base64_decode: %w", err)
		}
		return string(b), nil
	})

	// 哈希（SHA-256 hex）
	reg("hash_sha256", func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	})

	// 路径（POSIX 语义，插件运行环境一致）
	reg("path_join", func(parts ...string) string { return path.Join(parts...) })
	reg("path_base", func(p string) string { return path.Base(p) })
	reg("path_dir", func(p string) string { return path.Dir(p) })

	// URL 解析 → JSON 字符串
	reg("url_parse", func(u string) (string, error) {
		parsed, err := url.Parse(u)
		if err != nil {
			return "", fmt.Errorf("url_parse: %w", err)
		}
		m := map[string]any{
			"scheme":   parsed.Scheme,
			"host":     parsed.Host,
			"path":     parsed.Path,
			"query":    parsed.RawQuery,
			"fragment": parsed.Fragment,
		}
		if parsed.User != nil {
			m["user"] = parsed.User.Username()
		}
		b, err := json.Marshal(m)
		if err != nil {
			return "", fmt.Errorf("url_parse: %w", err)
		}
		return string(b), nil
	})

	// count_tokens(text) → int：估算 token 数（CJK 按字符、其他按 4 字符/token）。
	// 与 Agent 压缩触发同口径；估算用，精确值以 LLM API usage 为准。
	reg("count_tokens", func(s string) int { return countTokensEstimate(s) })
}
