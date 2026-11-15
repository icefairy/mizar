package builtins

import (
	"encoding/json"
	"strings"
	"testing"

	"mizar/internal/memory"
)

func TestMemoryToolAddListRemove(t *testing.T) {
	st := memory.NewStore(t.TempDir())
	changed := 0
	tool := MemoryTool(st, func() { changed++ })

	// add
	out, err := tool.Run(`{"action":"add","category":"user","text":"偏好结论先行"}`)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "user") {
		t.Errorf("add 输出应含分类: %s", out)
	}
	if changed != 1 {
		t.Errorf("add 应触发 onChanged 一次，got %d", changed)
	}

	// add 未知识别归 reference
	if _, err := tool.Run(`{"action":"add","category":"weird","text":"外部链接"}`); err != nil {
		t.Fatalf("add unknown: %v", err)
	}

	// list
	out, err = tool.Run(`{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "偏好结论先行") || !strings.Contains(out, "reference") {
		t.Errorf("list 输出异常: %s", out)
	}

	// remove index 1 of user
	if _, err := tool.Run(`{"action":"remove","category":"user","index":1}`); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m, _ := st.List("user")
	if len(m[memory.CategoryUser]) != 0 {
		t.Errorf("删除后 user 应为空: %v", m[memory.CategoryUser])
	}

	// count
	out, err = tool.Run(`{"action":"count"}`)
	if err != nil || !strings.Contains(out, "1") {
		t.Errorf("count: %s err=%v", out, err)
	}
}

func TestMemoryToolErrors(t *testing.T) {
	st := memory.NewStore(t.TempDir())
	tool := MemoryTool(st, nil)

	if _, err := tool.Run(`{"action":"add"}`); err == nil {
		t.Error("add 缺 text 应报错")
	}
	if _, err := tool.Run(`{"action":"remove","category":"user"}`); err == nil {
		t.Error("remove 缺 index 应报错")
	}
	if _, err := tool.Run(`{"action":"bogus"}`); err == nil {
		t.Error("未知 action 应报错")
	}
	if _, err := tool.Run(`not json`); err == nil {
		t.Error("非法 JSON 应报错")
	}
}

func TestMemoryToolNilStore(t *testing.T) {
	if tool := MemoryTool(nil, nil); tool.Name != "" {
		t.Error("nil store 应返回空工具")
	}
}

func TestMemoryToolDescriptionMentionsArgs(t *testing.T) {
	tool := MemoryTool(memory.NewStore(t.TempDir()), nil)
	var _ = json.Valid
	if !strings.Contains(tool.Description, "action") {
		t.Error("工具描述应含 action 参数说明")
	}
}
