package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/config"
)

// ---------------------------------------------------------------------------
// modelPicker 纯逻辑（上下键选择器的状态机，不依赖终端）
// ---------------------------------------------------------------------------

func TestModelPickerCursorStartsAtCurrent(t *testing.T) {
	models := []string{"a", "b", "c", "d"}
	p := newModelPicker(models, "c", 8)
	if got, ok := p.Selected(); !ok || got != "c" {
		t.Fatalf("Selected() = %q,%v, want c,true", got, ok)
	}
}

func TestModelPickerMoveClampsAtEnds(t *testing.T) {
	p := newModelPicker([]string{"a", "b", "c"}, "a", 8)
	p.Move(-1) // 已在首项，保持不动
	if got, _ := p.Selected(); got != "a" {
		t.Fatalf("Move(-1) at head → %q, want a", got)
	}
	p.Move(5) // 超出尾部，停在最后一项
	if got, _ := p.Selected(); got != "c" {
		t.Fatalf("Move(5) past tail → %q, want c", got)
	}
}

func TestModelPickerViewportFollowsCursor(t *testing.T) {
	models := make([]string, 20)
	for i := range models {
		models[i] = string(rune('a' + i))
	}
	p := newModelPicker(models, "a", 5)
	lo, hi := p.Lines()
	if lo != 0 || hi != 5 {
		t.Fatalf("初始视口 = [%d,%d), want [0,5)", lo, hi)
	}
	p.Move(4) // 光标到第 5 行（下标 4），仍在视口内
	if lo, hi = p.Lines(); lo != 0 || hi != 5 {
		t.Fatalf("光标在下标4时视口 = [%d,%d), want [0,5)", lo, hi)
	}
	p.Move(1) // 光标到下标 5 → 视口需滚动
	if lo, hi = p.Lines(); lo != 1 || hi != 6 {
		t.Fatalf("光标越出视口后 = [%d,%d), want [1,6)", lo, hi)
	}
	p.Move(100) // 到底：视口贴住尾部
	if lo, hi = p.Lines(); lo != 15 || hi != 20 {
		t.Fatalf("到底视口 = [%d,%d), want [15,20)", lo, hi)
	}
}

func TestModelPickerCurrentNotInList(t *testing.T) {
	// 当前模型不在清单里（网关接受任意模型名）：光标落在首项而非越界
	p := newModelPicker([]string{"a", "b"}, "unknown", 8)
	if got, _ := p.Selected(); got != "a" {
		t.Fatalf("Selected() = %q, want a", got)
	}
	// 空清单：Selected 报 false，不 panic
	p = newModelPicker(nil, "a", 8)
	if _, ok := p.Selected(); ok {
		t.Fatal("空清单 Selected() 应为 false")
	}
	p.Move(1) // 空清单移动不应 panic
}

func TestModelPickerVisibleDefault(t *testing.T) {
	// visible <= 0 时回退默认行数，避免 TUI 高度自适应算出 0 行视口
	p := newModelPicker([]string{"a", "b"}, "a", 0)
	_, hi := p.Lines()
	if hi <= 0 {
		t.Fatalf("默认视口高度应 > 0, got hi=%d", hi)
	}
}

// ---------------------------------------------------------------------------
// /model 命令（列表渲染 + 切换 + 持久化回调）
// ---------------------------------------------------------------------------

func testModelCommand(models []string, listErr error, current string, switchErr error) (agent.Command, *[]string) {
	var switched []string
	cmd := newModelCommand(
		"desc",
		func() ([]string, error) { return models, listErr },
		func() string { return current },
		func(name string) (string, error) {
			if switchErr != nil {
				return "", switchErr
			}
			switched = append(switched, name)
			return "✓ 模型已切换: " + current + " → " + name, nil
		},
	)
	return cmd, &switched
}

func TestModelCommandListShowsCurrentAndHint(t *testing.T) {
	c, _ := testModelCommand([]string{"m1", "m2"}, nil, "m2", nil)
	out, err := c.Run("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"m1", "m2", "当前模型: m2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("列表输出缺少 %q:\n%s", want, out)
		}
	}
	// 当前模型必须被标记（否则用户看不出自己在哪个）
	if !strings.Contains(out, "* m2") {
		t.Fatalf("当前模型未标记:\n%s", out)
	}
}

func TestModelCommandListDegradesOnFetchError(t *testing.T) {
	c, _ := testModelCommand(nil, errors.New("连接供应商失败"), "m1", nil)
	out, err := c.Run("")
	if err != nil {
		t.Fatalf("拉取失败不应让命令报错（仅提示）: %v", err)
	}
	if !strings.Contains(out, "连接供应商失败") || !strings.Contains(out, "m1") {
		t.Fatalf("拉取失败提示不完整:\n%s", out)
	}
}

func TestModelCommandSwitchByName(t *testing.T) {
	c, switched := testModelCommand([]string{"m1", "m2"}, nil, "m1", nil)
	out, err := c.Run("m2")
	if err != nil {
		t.Fatal(err)
	}
	if len(*switched) != 1 || (*switched)[0] != "m2" {
		t.Fatalf("switched = %v, want [m2]", *switched)
	}
	if !strings.Contains(out, "m2") {
		t.Fatalf("切换输出不含目标模型:\n%s", out)
	}
	// 目标在清单中：不应出现「不在清单中」告警
	if strings.Contains(out, "不在供应商清单中") {
		t.Fatalf("清单内模型不应告警:\n%s", out)
	}
}

func TestModelCommandWarnsWhenNameNotInList(t *testing.T) {
	c, switched := testModelCommand([]string{"m1"}, nil, "m1", nil)
	out, err := c.Run("m9")
	if err != nil {
		t.Fatal(err)
	}
	if len(*switched) != 1 {
		t.Fatalf("切换仍应执行, got %v", *switched)
	}
	if !strings.Contains(out, "不在供应商清单中") {
		t.Fatalf("清单外模型应告警:\n%s", out)
	}
}

func TestModelCommandSwitchErrorPropagates(t *testing.T) {
	c, _ := testModelCommand([]string{"m1"}, nil, "m1", errors.New("持久化失败"))
	if _, err := c.Run("m1"); err == nil {
		t.Fatal("切换失败应返回错误")
	}
}

// 名称唯一前缀匹配：用户手打时不至于因为少打几个字符就切错。
func TestFindModel(t *testing.T) {
	models := []string{"deepseek-v3", "deepseek-r1", "glm-4"}
	if got, ok := findModel(models, "deepseek-v3"); !ok || got != "deepseek-v3" {
		t.Fatalf("精确匹配失败: %q %v", got, ok)
	}
	if got, ok := findModel(models, "glm-4"); !ok || got != "glm-4" {
		t.Fatalf("精确匹配失败: %q %v", got, ok)
	}
	if _, ok := findModel(models, "deepseek"); ok {
		t.Fatal("歧义前缀不应算命中")
	}
	if _, ok := findModel(models, "nope"); ok {
		t.Fatal("不存在的名字不应算命中")
	}
}

// ---------------------------------------------------------------------------
// TUI 层：选择器渲染与确认路径（复用 queue_test 的 tuiModel 构造器）
// ---------------------------------------------------------------------------

// TestRenderModelPickerShowsCursorAndCurrent 选择器渲染应标出：当前模型（*）与选中行（▸）。
func TestRenderModelPickerShowsCursorAndCurrent(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.LLM = modelStub("m2")
	m.modelPickerActive = newModelPicker([]string{"m1", "m2", "m3"}, "m2", 10)
	m.renderModelPicker()

	text := m.textView.GetText(true)
	if !strings.Contains(text, "▸") {
		t.Fatalf("缺少选中标记 ▸:\n%s", text)
	}
	if !strings.Contains(text, "*") {
		t.Fatalf("缺少当前模型标记 *:\n%s", text)
	}
	for _, want := range []string{"m1", "m2", "m3", "Enter 确认"} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染缺少 %q:\n%s", want, text)
		}
	}
}

// TestRenderModelPickerViewportHint 清单超出视口时应提示还有更多，避免用户以为只有这几项。
func TestRenderModelPickerViewportHint(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.LLM = modelStub("m0")
	models := []string{"m0", "m1", "m2", "m3", "m4"}
	m.modelPickerActive = newModelPicker(models, "m0", 2)
	m.renderModelPicker()
	if text := m.textView.GetText(true); !strings.Contains(text, "还有") {
		t.Fatalf("视口被截断时应提示剩余数量:\n%s", text)
	}
}

// TestCloseModelPickerRestoresView 取消后应退出选择器且聊天区恢复普通视图。
func TestCloseModelPickerRestoresView(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.LLM = modelStub("m1")
	m.lines = append(m.lines, chatLine{role: "bot", content: "历史回复"})
	m.modelPickerActive = newModelPicker([]string{"m1", "m2"}, "m1", 10)
	m.closeModelPicker()
	if m.modelPickerActive != nil {
		t.Fatal("closeModelPicker 后 modelPickerActive 应为 nil")
	}
	if text := m.textView.GetText(true); !strings.Contains(text, "历史回复") {
		t.Fatalf("退出选择器后应恢复普通视图:\n%s", text)
	}
}

// modelStub 最小 LLM 桩：提供 Model/SetModel/Info，供选择器与 /model 命令联动。
// 只实现 agent.LLM 必需的 Chat；其余为选择器路径可选读取的方法。
type modelStub string

func (s modelStub) Chat(msgs []agent.Message) (string, error) { return "", nil }
func (s modelStub) ModelName() string                         { return string(s) }
func (s modelStub) Info() (string, string)                    { return "", "" }
func (s modelStub) SetModel(m string)                         {}
func (s modelStub) ThinkingEnabled() string                   { return "" }

// ---------------------------------------------------------------------------
// 集成：真实 HTTP 供应商 + 真实 config.ListModels + 真实切模型回调
// （覆盖「拉取→列出→切换→持久化」全链路，不 mock 网络层）
// ---------------------------------------------------------------------------

func TestModelCommandIntegrationWithRealProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"alpha-1"},{"id":"beta-2"}]}`)
	}))
	defer srv.Close()

	var persisted string
	cmd := newModelCommand(
		"desc",
		func() ([]string, error) { return config.ListModels(srv.URL+"/v1", "k") },
		func() string { return "alpha-1" },
		func(name string) (string, error) {
			persisted = name
			return "✓ 模型已切换: alpha-1 → " + name, nil
		},
	)

	// 1) 无参：真实拉取并列出
	out, err := cmd.Run("")
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	for _, want := range []string{"alpha-1", "beta-2", "当前模型: alpha-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("真实拉取结果缺少 %q:\n%s", want, out)
		}
	}

	// 2) 带参：真实切换并持久化
	out, err = cmd.Run("beta-2")
	if err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if persisted != "beta-2" {
		t.Fatalf("persisted = %q, want beta-2", persisted)
	}
	if strings.Contains(out, "不在供应商清单中") {
		t.Fatalf("清单内模型不应告警:\n%s", out)
	}
}
