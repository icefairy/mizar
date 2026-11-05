package zigboot

import (
	"os"
	"strings"
	"testing"
)

func TestEnvInjectsZigDir(t *testing.T) {
	env := Env([]string{"PATH=/usr/bin:/bin", "HOME=/root"})
	t.Logf("Available=%v ZigBinary=%s", Available(), ZigBinary())
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
			t.Logf("PATH=%s", kv)
			if Available() && !strings.Contains(kv, CacheDir()) {
				t.Fatalf("PATH 未包含 zig 目录: %s", kv)
			}
		}
	}
	if !found {
		t.Fatal("PATH 丢失")
	}
	if len(env) != 2 {
		t.Fatalf("条目数应保持 2，实际 %d", len(env))
	}
}

func TestEnvNoPathEntry(t *testing.T) {
	env := Env([]string{"HOME=/root"})
	if !Available() {
		t.Skip("zig 未释放")
	}
	last := env[len(env)-1]
	if !strings.HasPrefix(last, "PATH=") {
		t.Fatalf("应补 PATH，实际 %v", env)
	}
	t.Logf("补的 PATH=%s", last)
	_ = os.Getenv("PATH")
}
