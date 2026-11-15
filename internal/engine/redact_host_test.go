package engine

import (
	"strings"
	"testing"
)

// TestRedactTextHostFunc P3 回归：插件能调 redact_text 脱敏。
func TestRedactTextHostFunc(t *testing.T) {
	e, err := New(&HostFuncs{})
	if err != nil {
		t.Fatal(err)
	}
	script := `
var out = redact_text("key=sk-proj-AbCdEf0123456789GhIjKl");
var phoneKept = redact_text("联系 13812345678");
var phoneMasked = redact_text_phone("联系 13812345678");
var result = JSON.stringify({out: out, phoneKept: phoneKept, phoneMasked: phoneMasked});
`
	if err := e.RunScript("redact_test", script); err != nil {
		t.Fatalf("run: %v", err)
	}
	// 直接读 JS 变量
	res := e.vm.Get("result")
	if res == nil || res.String() == "undefined" {
		t.Fatal("result 未定义")
	}
	s := res.String()
	if strings.Contains(s, "AbCdEf0123456789") {
		t.Errorf("密钥未脱敏: %s", s)
	}
	if !strings.Contains(s, "***") {
		t.Errorf("应有掩码: %s", s)
	}
	if !strings.Contains(s, "13812345678") {
		t.Errorf("redact_text 默认不应脱敏手机号: %s", s)
	}
}
