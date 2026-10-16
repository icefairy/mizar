package main

// modelcmd.go —— /model 命令与「上下键选择模型」选择器。
//
// 背景：旧版 /model 只能打印模型清单，用户还得再手打一遍模型全名。弱模型/长模型名
// （如 qwen3-235b-a22b）手打极易出错——输入错一个字符就是一个 404 或者静默换了别的模型。
//
// 方案（最小改动）：
//   - 命令主体抽成 newModelCommand（可单测，不依赖终端）：无参数仍列出清单（经典
//     readline / Web 端 / -task 模式行为不变），带参数则切换。
//   - TUI 里额外接一层选择器：输入 /model 直接弹出清单，↑↓ 选择、Enter 确认、Esc 取消。
//     选择器只读写 tuiModel 上的 picker 状态，渲染复用聊天区 TextView（与拷贝模式同一套路，
//     不引入 tview.Modal 新交互范式）。
//
// 注意：不要让选择器成为唯一入口——无 tview 的环境（管道 / Web UI / 经典模式）仍走命令，
// 否则 /model 会在这些场景下变成「什么都没发生」。

import (
	"fmt"
	"strings"

	"mizar/internal/agent"
)

// ---------------------------------------------------------------------------
// 模型清单渲染（命令与选择器共用）
// ---------------------------------------------------------------------------

// renderModelList 把模型清单渲染为「* 为当前」的表格 + 切换提示。
// 拉取失败时降级为提示 + 直接指定模型名的用法（不让命令报错，用户仍能继续工作）。
func renderModelList(models []string, listErr error, current string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "当前模型: %s\n", current)
	switch {
	case listErr != nil:
		fmt.Fprintf(&sb, "获取模型清单失败: %v\n", listErr)
		sb.WriteString("可直接指定模型名（不依赖清单）: /model <名称>")
	case len(models) == 0:
		sb.WriteString("供应商未返回可用模型，可直接指定模型名: /model <名称>")
	default:
		fmt.Fprintf(&sb, "供应商可用模型（共 %d 个，* 为当前）：\n", len(models))
		for _, m := range models {
			mark := " "
			if m == current {
				mark = "*"
			}
			fmt.Fprintf(&sb, "  %s %s\n", mark, m)
		}
		sb.WriteString("切换: /model <名称>（TUI 下直接输入 /model 可用 ↑↓ 选择）")
	}
	return sb.String()
}

// findModel 在清单中查找用户输入的模型名。
// 优先精确匹配；退化为唯一前缀匹配（手打长名字少打几个字符时仍能命中），
// 前缀命中多项时视为歧义，返回未命中——宁可不切，也不要切到用户没想选的模型。
func findModel(models []string, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, m := range models {
		if m == name {
			return m, true
		}
	}
	hit := ""
	for _, m := range models {
		if strings.HasPrefix(m, name) {
			if hit != "" {
				return "", false // 歧义
			}
			hit = m
		}
	}
	return hit, hit != ""
}

// ---------------------------------------------------------------------------
// /model 命令
// ---------------------------------------------------------------------------

// newModelCommand 构造 /model 命令。
// list/current/switchModel 由调用方注入，使命令本身可单测（不碰网络与全局配置）。
func newModelCommand(
	description string,
	list func() ([]string, error),
	current func() string,
	switchModel func(name string) (string, error),
) agent.Command {
	return agent.Command{
		Name:        "model",
		Description: description,
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			// 清单拉取失败不阻断：列表与切换都要能降级工作（网关可能接受任意模型名）
			models, listErr := list()
			if args == "" {
				return renderModelList(models, listErr, current()), nil
			}
			out, err := switchModel(args)
			if err != nil {
				return "", err
			}
			// 清单可获取但目标不在内：警告不阻断（网关代理场景可能接受任意名）
			if listErr == nil && len(models) > 0 {
				if _, ok := findModel(models, args); !ok {
					out += fmt.Sprintf("\n⚠ 模型 %q 不在供应商清单中（可执行 /model 查看可用清单）", args)
				}
			}
			return out, nil
		},
	}
}

// ---------------------------------------------------------------------------
// 选择器状态机（纯逻辑，便于单测）
// ---------------------------------------------------------------------------

// modelPicker 模型选择器状态：光标 + 可见窗口。
// 只管「选哪个」与「显示哪一段」，不碰渲染与终端，便于测试。
type modelPicker struct {
	models  []string
	cursor  int // 当前选中下标
	top     int // 可见窗口起始下标
	visible int // 可见行数（<=0 回退 defaultPickerRows）
}

// defaultPickerRows 选择器默认可见行数（终端高度不可用时的回退）。
const defaultPickerRows = 12

func newModelPicker(models []string, current string, visible int) *modelPicker {
	if visible <= 0 {
		visible = defaultPickerRows
	}
	p := &modelPicker{models: models, visible: visible}
	// 光标落在当前模型上（找不到则首项），省掉用户「先找到自己在哪」的一步
	for i, m := range models {
		if m == current {
			p.cursor = i
			break
		}
	}
	p.clampTop()
	return p
}

// Move 移动光标（delta 行），越界时停在首/末项（不循环——循环会让用户失去位置感）。
func (p *modelPicker) Move(delta int) {
	if len(p.models) == 0 {
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.models) {
		p.cursor = len(p.models) - 1
	}
	p.clampTop()
}

// Selected 返回当前选中的模型名；清单为空时 ok=false。
func (p *modelPicker) Selected() (string, bool) {
	if len(p.models) == 0 || p.cursor < 0 || p.cursor >= len(p.models) {
		return "", false
	}
	return p.models[p.cursor], true
}

// Lines 返回可见窗口 [lo, hi)。窗口始终贴着清单上/下边界，不会越界。
func (p *modelPicker) Lines() (int, int) {
	if len(p.models) == 0 {
		return 0, 0
	}
	hi := p.top + p.visible
	if hi > len(p.models) {
		hi = len(p.models)
	}
	return p.top, hi
}

// clampTop 调整窗口起点，保证光标可见。
func (p *modelPicker) clampTop() {
	n := len(p.models)
	if n == 0 {
		p.top = 0
		return
	}
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+p.visible {
		p.top = p.cursor - p.visible + 1
	}
	if maxTop := n - p.visible; p.top > maxTop {
		p.top = maxTop
	}
	if p.top < 0 {
		p.top = 0
	}
}
