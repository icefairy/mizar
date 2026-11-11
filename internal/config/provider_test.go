package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadUpgradesLegacyConfig 旧版单供应商配置在 Load 时自动迁移到 providers 收藏夹，
// 并原样保留旧字段（新旧版均可读写），同时备份原文件。
func TestLoadUpgradesLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	legacy := `{"base_url":"https://api.deepseek.com/v1","api_key":"sk-abc","model":"deepseek-chat","context_window":128000}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseURL != "https://api.deepseek.com/v1" || c.APIKey != "sk-abc" || c.Model != "deepseek-chat" {
		t.Fatalf("旧字段应保留: %+v", c)
	}
	if len(c.Providers) != 1 {
		t.Fatalf("应自动迁移出 1 条 provider, got %d", len(c.Providers))
	}
	if c.Providers[0].BaseURL != c.BaseURL || c.Providers[0].APIKey != "sk-abc" {
		t.Fatalf("映射错误: %+v", c.Providers[0])
	}
	if c.ActiveProvider != c.Providers[0].Name {
		t.Fatalf("active 应为迁移出的名字: %q vs %q", c.ActiveProvider, c.Providers[0].Name)
	}
	// 已写回磁盘
	data, _ := os.ReadFile(p)
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Providers) != 1 {
		t.Fatalf("升级后应写回 providers, got %s", data)
	}
	// 备份存在
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatalf("应生成 .bak 备份: %v", err)
	}
	// 再 Load 一次不再变化（幂等）
	c2, _ := Load(p)
	if len(c2.Providers) != 1 || c2.ActiveProvider != c.ActiveProvider {
		t.Fatalf("二次加载不应重复迁移: %+v", c2)
	}
}

// TestLoadNoBaseURLNoMigration 空配置不迁移。
func TestLoadNoBaseURLNoMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"model":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("无 base_url 不应迁移: %+v", c.Providers)
	}
}

// TestProviderCRUD 收藏夹增删查。
func TestProviderCRUD(t *testing.T) {
	c := &Config{}
	n := c.UpsertProvider(Provider{Name: "home", BaseURL: "http://127.0.0.1:3002/v1"})
	if n != "home" || len(c.Providers) != 1 {
		t.Fatalf("Upsert 失败: %+v", c)
	}
	// 同名覆盖
	c.UpsertProvider(Provider{Name: "HOME", BaseURL: "https://a.example.com/v1"})
	if len(c.Providers) != 1 || c.Providers[0].BaseURL != "https://a.example.com/v1" {
		t.Fatalf("同名应覆盖: %+v", c.Providers)
	}
	if p := c.FindProvider("home"); p == nil || p.BaseURL == "" {
		t.Fatalf("FindProvider 大小写不敏感失败")
	}
	c.ActiveProvider = "home"
	if !c.RemoveProvider("HOME") {
		t.Fatal("RemoveProvider 应成功")
	}
	if len(c.Providers) != 0 || c.ActiveProvider != "" {
		t.Fatalf("删除后应清空: %+v", c)
	}
}

// TestDeriveProviderName 名字推导。
func TestDeriveProviderName(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/v1": "api.deepseek.com",
		"http://127.0.0.1:3002/v1":    "127.0.0.1",
		"":                            "custom",
		"https://x.example.com":       "x.example.com",
	}
	for in, want := range cases {
		if got := DeriveProviderName(in); got != want {
			t.Errorf("DeriveProviderName(%q)=%q want %q", in, got, want)
		}
	}
}
