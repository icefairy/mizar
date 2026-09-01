//go:build windows

package builtins

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"mizar/internal/plugins"
)

// win32api.go — Windows Win32 API 高性能封装（基于 syscall，μs 级）
// 替代原 PowerShell 方式的 ms 级延迟，提供鼠标/键盘/窗口等桌面自动化能力。

// ---------------------------------------------------------------------------
// DLL 与过程句柄（全局懒加载）
// ---------------------------------------------------------------------------

var (
	modUser32            *syscall.LazyDLL
	procSetCursorPos     *syscall.LazyProc
	procMouseEv          *syscall.LazyProc
	procKeybdEv          *syscall.LazyProc
	procGetWindowTextW   *syscall.LazyProc
	procIsWindowVisible  *syscall.LazyProc
	procEnumWindows      *syscall.LazyProc
	procGetSystemMetrics *syscall.LazyProc
)

func initWin32() {
	if modUser32 != nil {
		return
	}
	modUser32 = syscall.NewLazyDLL("user32.dll")
	procSetCursorPos = modUser32.NewProc("SetCursorPos")
	procMouseEv = modUser32.NewProc("mouse_event")
	procKeybdEv = modUser32.NewProc("keybd_event")
	procGetWindowTextW = modUser32.NewProc("GetWindowTextW")
	procIsWindowVisible = modUser32.NewProc("IsWindowVisible")
	procEnumWindows = modUser32.NewProc("EnumWindows")
	procGetSystemMetrics = modUser32.NewProc("GetSystemMetrics")
}

// ---------------------------------------------------------------------------
// 工具函数定义
// ---------------------------------------------------------------------------

// toolMoveMouse 移动鼠标到指定坐标。
func toolMoveMouse(args string) (string, error) {
	var p struct{ X, Y int }
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("move_mouse: args {x, y} required")
	}
	ret, _, err := procSetCursorPos.Call(uintptr(p.X), uintptr(p.Y))
	if ret == 0 {
		return "", fmt.Errorf("SetCursorPos failed: %v", err)
	}
	return fmt.Sprintf("鼠标已移动到 (%d, %d)", p.X, p.Y), nil
}

// toolClick 鼠标点击。
func toolClick(args string) (string, error) {
	var p struct {
		X, Y        int
		Button      string // "left" | "right" | "middle"
		DoubleClick bool
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("click: args {x?, y?, button?, doubleClick?} required")
	}
	if p.X != 0 || p.Y != 0 {
		procSetCursorPos.Call(uintptr(p.X), uintptr(p.Y))
	}
	var flags uint32
	switch p.Button {
	case "right":
		flags = 0x0008
	case "middle":
		flags = 0x0010
	default:
		flags = 0x0002
	}
	count := 1
	if p.DoubleClick {
		count = 2
	}
	for i := 0; i < count; i++ {
		procMouseEv.Call(uintptr(flags), 0, 0, 0, 0)
		time.Sleep(50)
		procMouseEv.Call(uintptr(flags^0x0004), 0, 0, 0, 0)
		time.Sleep(50)
	}
	btnName := "左"
	if p.Button == "right" {
		btnName = "右"
	} else if p.Button == "middle" {
		btnName = "中"
	}
	return fmt.Sprintf("鼠标%s%s按钮已发送", btnName, map[bool]string{true: "双", false: ""}[p.DoubleClick]), nil
}

// toolSendKey 发送单个按键。
func toolSendKey(args string) (string, error) {
	var p struct{ Key string }
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.Key == "" {
		return "", fmt.Errorf("send_key: args {key} required")
	}
	// SendKeys 特殊键映射（key 统一大写，与 VK 码名一致）
	keyMap := map[string]uint8{
		"ENTER":     0x0D,
		"ESC":       0x1B,
		"TAB":       0x09,
		"BACKSPACE": 0x08,
		"DELETE":    0x2E,
		"HOME":      0x24,
		"END":       0x23,
		"PAGEUP":    0x21,
		"PAGEDOWN":  0x22,
		"INSERT":    0x2D,
		"SPACE":     0x20,
		"F1":        0x70,
		"F2":        0x71,
		"F3":        0x72,
		"F4":        0x73,
		"F5":        0x74,
		"F6":        0x75,
		"F7":        0x76,
		"F8":        0x77,
		"F9":        0x78,
		"F10":       0x79,
		"F11":       0x7A,
		"F12":       0x7B,
		"UP":        0x26,
		"DOWN":      0x28,
		"LEFT":      0x25,
		"RIGHT":     0x27,
	}
	key := strings.ToUpper(p.Key)
	vk := uint8(0)
	if code, found := keyMap[key]; found {
		vk = code
	} else if len(key) == 1 {
		// 单字符：可打印 ASCII 的 VK 码与 ASCII 码相同
		vk = key[0]
	} else {
		return "", fmt.Errorf("send_key: 不支持的键名 %q（可用 Enter/Tab/Escape/Backspace/Del/Shift/Ctrl/Alt/Home/End/PageUp/PageDown/方向键/F1-F12 或单个字符）", p.Key)
	}
	procKeybdEv.Call(uintptr(vk), 0, 0, 0)
	time.Sleep(10)
	procKeybdEv.Call(uintptr(vk), 0, 0x2, 0) // KEYEVENTF_KEYUP
	return fmt.Sprintf("按键已发送: %s", p.Key), nil
}

// toolTypeText 批量输入文本。
func toolTypeText(args string) (string, error) {
	var p struct{ Text string }
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.Text == "" {
		return "", fmt.Errorf("type_text: args {text} required")
	}
	for _, r := range p.Text {
		if r == '\n' {
			toolSendKey(`{"key":"Enter"}`)
		} else if r == '\t' {
			toolSendKey(`{"key":"Tab"}`)
		} else {
			toolSendKey(fmt.Sprintf(`{"key":"%c"}`, r))
		}
	}
	return fmt.Sprintf("已输入文本 (%d 字符)", len(p.Text)), nil
}

// toolDrag 拖动鼠标。
func toolDrag(args string) (string, error) {
	var p struct {
		SX, SY, EX, EY int
		Button         string
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("drag: args {sx, sy, ex, ey, button?} required")
	}
	var flags uint32 = 0x0002 // left default
	if p.Button == "right" {
		flags = 0x0008
	} else if p.Button == "middle" {
		flags = 0x0010
	}
	procSetCursorPos.Call(uintptr(p.SX), uintptr(p.SY))
	time.Sleep(100)
	procMouseEv.Call(uintptr(flags), 0, 0, 0, 0)
	time.Sleep(100)
	steps := 20
	for i := 1; i <= steps; i++ {
		x := p.SX + (p.EX-p.SX)*i/steps
		y := p.SY + (p.EY-p.SY)*i/steps
		procSetCursorPos.Call(uintptr(x), uintptr(y))
		procMouseEv.Call(uintptr(flags|0x0001), 0, 0, 0, 0)
		time.Sleep(5)
	}
	procMouseEv.Call(uintptr(flags^0x0004), 0, 0, 0, 0)
	return fmt.Sprintf("已从 (%d,%d) 拖动到 (%d,%d)", p.SX, p.SY, p.EX, p.EY), nil
}

// toolScreenshot 屏幕截图（使用 PowerShell 作为 fallback）。
func toolScreenshot(args string) (string, error) {
	var p struct {
		Path       string
		X, Y, W, H int
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
		return "", fmt.Errorf("screenshot: args {path} required")
	}
	// 纯 Go 实现截图需要复杂的 GDI 操作，这里使用 PowerShell 调用作为替代
	psCmd := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$rect = New-Object System.Drawing.Rectangle(%d, %d, %d, %d)
$bmp = New-Object System.Drawing.Bitmap($rect.Width, $rect.Height)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($rect.Location, [System.Drawing.Point]::Empty, $rect.Size)
$g.Dispose()
$bmp.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
`, p.X, p.Y, p.W, p.H, p.Path)
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", psCmd).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("截图失败: %v, output: %s", err, out)
	}
	return fmt.Sprintf("截图已保存到: %s", p.Path), nil
}

// toolGetScreenInfo 获取屏幕分辨率。
func toolGetScreenInfo(args string) (string, error) {
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	h, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return fmt.Sprintf("屏幕分辨率: %dx%d", int(w), int(h)), nil
}

// toolListWindows 获取窗口列表。
func toolListWindows(args string) (string, error) {
	var p struct{ Title string }
	json.Unmarshal([]byte(args), &p)

	var results []string
	// 使用回调函数枚举窗口
	type enumProcType func(syscall.Handle, uintptr) uintptr
	var callback enumProcType = func(hWnd syscall.Handle, lParam uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(uintptr(hWnd)); visible != 0 {
			buf := make([]uint16, 256)
			procGetWindowTextW.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&buf[0])), 256)
			title := syscall.UTF16ToString(buf)
			if p.Title == "" || strings.Contains(title, p.Title) {
				results = append(results, fmt.Sprintf("HWND=%X,Title=%s", hWnd, title))
			}
		}
		return 1
	}
	procEnumWindows.Call(uintptr(unsafe.Pointer(&callback)), 0)
	return strings.Join(results, "|"), nil
}

// toolSleep 等待指定毫秒。
func toolSleep(args string) (string, error) {
	var p struct{ MS int }
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("sleep: args {ms} required")
	}
	time.Sleep(time.Duration(p.MS) * time.Millisecond)
	return fmt.Sprintf("已等待 %dms", p.MS), nil
}

// ---------------------------------------------------------------------------
// 工具注册
// ---------------------------------------------------------------------------

func toolWin32MoveMouse() plugins.Tool {
	return plugins.Tool{
		Name:        "move_mouse",
		Description: "移动鼠标到指定坐标。何时用：控制鼠标位置。参数: {x: number, y: number} 屏幕像素坐标",
		Run: func(args string) (string, error) {
			initWin32()
			return toolMoveMouse(args)
		},
	}
}

func toolWin32Click() plugins.Tool {
	return plugins.Tool{
		Name:        "click",
		Description: "鼠标点击。何时用：模拟鼠标点击。参数: {x?: number, y?: number, button?: 'left'|'right'|'middle', doubleClick?: boolean}",
		Run: func(args string) (string, error) {
			initWin32()
			return toolClick(args)
		},
	}
}

func toolWin32SendKey() plugins.Tool {
	return plugins.Tool{
		Name:        "send_key",
		Description: "发送单个按键。何时用：模拟键盘输入单个键。参数: {key: string} 键名，如 \"Enter\", \"Escape\", \"a\", \"F1\" 等",
		Run: func(args string) (string, error) {
			initWin32()
			return toolSendKey(args)
		},
	}
}

func toolWin32TypeText() plugins.Tool {
	return plugins.Tool{
		Name:        "type_text",
		Description: "输入文本。何时用：批量输入文本内容。参数: {text: string} 要输入的文本",
		Run: func(args string) (string, error) {
			initWin32()
			return toolTypeText(args)
		},
	}
}

func toolWin32Drag() plugins.Tool {
	return plugins.Tool{
		Name:        "drag",
		Description: "拖动鼠标。何时用：模拟拖拽操作。参数: {sx: number, sy: number, ex: number, ey: number, button?: 'left'|'right'|'middle'}",
		Run: func(args string) (string, error) {
			initWin32()
			return toolDrag(args)
		},
	}
}

func toolWin32Screenshot() plugins.Tool {
	return plugins.Tool{
		Name:        "screenshot",
		Description: "屏幕截图。何时用：截取当前屏幕或指定区域保存为 PNG。参数: {path: string, x?: number, y?: number, w?: number, h?: number}",
		Run: func(args string) (string, error) {
			initWin32()
			return toolScreenshot(args)
		},
	}
}

func toolWin32GetScreenInfo() plugins.Tool {
	return plugins.Tool{
		Name:        "get_screen_info",
		Description: "获取屏幕分辨率。何时用：查询屏幕尺寸信息。参数: {}",
		Run: func(args string) (string, error) {
			initWin32()
			return toolGetScreenInfo(args)
		},
	}
}

func toolWin32ListWindows() plugins.Tool {
	return plugins.Tool{
		Name:        "list_windows",
		Description: "获取窗口列表。何时用：枚举当前可见窗口。参数: {title?: string} 可选按标题过滤",
		Run: func(args string) (string, error) {
			initWin32()
			return toolListWindows(args)
		},
	}
}

func toolWin32Sleep() plugins.Tool {
	return plugins.Tool{
		Name:        "sleep",
		Description: "等待指定毫秒。何时用：在操作间插入延迟。参数: {ms: number} 毫秒数",
		Run: func(args string) (string, error) {
			initWin32()
			return toolSleep(args)
		},
	}
}

// Win32Tools 返回所有 Win32 API 工具的切片。
func Win32Tools() []plugins.Tool {
	return []plugins.Tool{
		toolWin32MoveMouse(),
		toolWin32Click(),
		toolWin32SendKey(),
		toolWin32TypeText(),
		toolWin32Drag(),
		toolWin32Screenshot(),
		toolWin32GetScreenInfo(),
		toolWin32ListWindows(),
		toolWin32Sleep(),
	}
}

// platformTools 返回当前平台的额外内置工具。
// Windows 提供 Win32 桌面自动化工具（鼠标/键盘/窗口/截图）。
func platformTools() []plugins.Tool {
	return Win32Tools()
}
