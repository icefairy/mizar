package main

// mousecmd.go —— /mouse 命令：运行期开关鼠标上报（滚轮 / 拖选复制 / 右键粘贴）。
//
// 背景：旧版 tcell 在 Windows（ConPTY）下鼠标事件会被双重编码成字符泄漏进输入框，
// 因此 Windows 曾默认关闭鼠标上报。tcell ≥ v2.13 改为 win32-input-mode + VT 解析后
// 鼠标已能正确还原，故现在各平台默认开启。
//
// 但总有例外：
//   - 老终端 / 特殊复用器仍会泄漏字符（输入框蹦 "[<35;66;61M"），需要关掉；
//   - 只想用键盘、不想鼠标选择干扰；相反也有人想确认它真的开着。
//
// 以前只能靠环境变量 MIZAR_MOUSE（每次启动都要设）。现在提供 /mouse：
//   /mouse            查看当前状态与用法
//   /mouse on|off     切换（立即生效，写入 config.json 的 mouse 字段）
//   /mouse toggle     反转
import (
	"fmt"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/config"
	"mizar/internal/diag"
)

// renderMouseStatus 渲染 /mouse 的状态说明。
func renderMouseStatus(on bool) string {
	state := "已开启"
	effect := "滚轮滚动、左键拖选即复制、双击选整行、右键粘贴均可用"
	if !on {
		state = "已关闭"
		effect = "鼠标事件交给终端（用终端原生拖选 + Ctrl+Shift+C/V 复制）；键盘 Alt+C 拷贝模式、PgUp/PgDn 不受影响"
	}
	return fmt.Sprintf("鼠标上报: %s\n效果: %s\n\n切换: /mouse on | /mouse off | /mouse toggle\n"+
		"（也可用环境变量 MIZAR_MOUSE=0/1，或 config.json 的 \"mouse\" 字段设置默认值）", state, effect)
}

// newMouseCommand 构造 /mouse 命令。persist 写入 config.json 的 mouse 字段。
func newMouseCommand(persist func(on bool) error) agent.Command {
	return agent.Command{
		Name:        "mouse",
		Description: "开关鼠标上报（TUI 滚轮/拖选复制/右键粘贴）：/mouse | /mouse on|off|toggle",
		Run: func(args string) (string, error) {
			arg := strings.ToLower(strings.TrimSpace(args))
			if arg == "" || arg == "status" || arg == "?" {
				return renderMouseStatus(MouseEnabled()), nil
			}

			var target bool
			switch arg {
			case "on", "1", "true", "enable", "开":
				target = true
			case "off", "0", "false", "disable", "关":
				target = false
			case "toggle", "switch":
				target = !MouseEnabled()
			default:
				return "", fmt.Errorf("未知参数 %q（可用: on / off / toggle）\n\n%s", arg, renderMouseStatus(MouseEnabled()))
			}

			diag.Logf("tui", "/mouse %s → 鼠标上报=%v", arg, target)
			if err := SetMouseEnabled(target); err != nil {
				return "", fmt.Errorf("切换鼠标失败: %w", err)
			}
			if persist != nil {
				if err := persist(target); err != nil {
					return "", fmt.Errorf("已切换（%s），但写入配置失败（重启后会恢复默认）: %w",
						map[bool]string{true: "开启", false: "关闭"}[target], err)
				}
			}
			if target {
				return "✓ 鼠标上报已开启（滚轮滚动 / 拖选即复制 / 右键粘贴）\n" + renderMouseStatus(true), nil
			}
			return "✓ 鼠标上报已关闭（改用终端原生拖选 + Ctrl+Shift+C/V；键盘快捷键不受影响）\n" + renderMouseStatus(false), nil
		},
	}
}

// persistMouse 把鼠标开关写入 config.json。
func persistMouse(on bool) error {
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	v := on
	cfg.Mouse = &v
	return config.Save(path, cfg)
}
