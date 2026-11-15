package redact

import (
	"strings"
	"testing"
)

func TestPrefixKeys(t *testing.T) {
	cases := []string{
		"sk-proj-AbCdEf0123456789GhIjKlMnOpQrStUv",
		"ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ012345",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-11223344-EXAMPLE-TOKEN",
		"AIzaSyD-abcdefghijklmnopqrstuvwxyz12345",
		"hf_aBcDeFgHiJkLmNoPqRsTuVwX",
		"glpat-aBcDeFgHiJkLmNoPqRsT",
	}
	for _, k := range cases {
		out := Text("key=" + k)
		if strings.Contains(out, k) {
			t.Errorf("未脱敏: %s → %s", k, out)
		}
		if !strings.Contains(out, Mask) {
			t.Errorf("应含掩码: %s", out)
		}
	}
}

// 形似但不是密钥的人类可读标识符不能被误杀。
func TestNotOverRedact(t *testing.T) {
	cases := []string{
		"sk-prod-deployment-01",
		"sk-feature-flags",
		"task-run-1234",
		"fc-feature-flags-v2",
		"my-secret-key-name",
	}
	for _, s := range cases {
		if out := Text(s); out != s {
			t.Errorf("误杀普通标识符: %q → %q", s, out)
		}
	}
}

func TestKVPatterns(t *testing.T) {
	in := `{"api_key": "abcdefgh12345678", "password": "hunter2", "other": "keep"}`
	out := Text(in)
	if strings.Contains(out, "abcdefgh12345678") {
		t.Errorf("api_key 值未脱敏: %s", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("password 值未脱敏: %s", out)
	}
	if !strings.Contains(out, "api_key") {
		t.Errorf("键名应保留: %s", out)
	}
	if !strings.Contains(out, "keep") {
		t.Errorf("无关字段不应改动: %s", out)
	}
}

func TestBearerToken(t *testing.T) {
	in := "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456"
	out := Text(in)
	if strings.Contains(out, "abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("Bearer token 未脱敏: %s", out)
	}
}

func TestPrivateKeyBlock(t *testing.T) {
	in := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----"
	out := Text(in)
	if strings.Contains(out, "MIIEow") {
		t.Errorf("私钥内容未脱敏: %s", out)
	}
	if !strings.Contains(out, "REDACTED PRIVATE KEY") {
		t.Errorf("应替换为占位: %s", out)
	}
}

func TestPhoneOptIn(t *testing.T) {
	in := "联系我 13812345678 或 +86 13900001111"
	// 默认不脱敏
	if out := Text(in); out != in {
		t.Errorf("手机号默认不应脱敏: %s", out)
	}
	// 显式开启才脱敏
	out := Text(in, WithPhoneRedaction(true))
	if strings.Contains(out, "13812345678") {
		t.Errorf("开启后应脱敏: %s", out)
	}
}

func TestPlaceholderNotDoubleMasked(t *testing.T) {
	for _, s := range []string{`password=***`, `api_key=xxx`, `password=null`} {
		if out := Text(s); strings.Contains(out, "******") {
			t.Errorf("占位符不应二次掩码: %q → %q", s, out)
		}
	}
}

func TestMaskToken(t *testing.T) {
	// 短 token 全掩
	if got := MaskToken("abc"); got != Mask {
		t.Errorf("短 token 应全掩: %q", got)
	}
	// 长 token 留前 4 后 4
	long := "abcdefghijklmnopqrstuvwxyz"
	got := MaskToken(long)
	if !strings.HasPrefix(got, "abcd") || !strings.HasSuffix(got, "wxyz") {
		t.Errorf("长 token 应留前 4 后 4: %q", got)
	}
	if strings.Contains(got, "efghijklmnop") {
		t.Errorf("中段应被掩掉: %q", got)
	}
}

func TestGlobalSwitch(t *testing.T) {
	SetEnabled(false)
	defer SetEnabled(true)
	in := "sk-proj-AbCdEf0123456789GhIjKlMnOpQrStUv"
	if out := Apply(in); out != in {
		t.Errorf("全局关闭时不应脱敏: %s", out)
	}
	SetEnabled(true)
	if out := Apply(in); out == in {
		t.Errorf("全局开启时应脱敏")
	}
}

func TestEmptyAndPlain(t *testing.T) {
	if Text("") != "" {
		t.Error("空串应原样返回")
	}
	plain := "今天天气不错，我们写点 Go 代码。"
	if out := Text(plain); out != plain {
		t.Errorf("普通文本不应改动: %s", out)
	}
}
