# go-win7 补丁（构建期依赖，不参与 mizar 运行时）

来源：https://github.com/XTLS/go-win7 （GPL-3.0，见本目录 LICENSE）
版本：v0.0.0-20261008195919-f6e0c8ebb3f1

## 用途

Go 1.21 起官方最低要求 Windows 10 / Server 2016。用官方 SDK 编出的 mizar.exe
在 Windows 7 SP1 上会在 runtime 初始化阶段崩溃，栈顶形如：

```
internal/runtime/syscall/windows/asm_windows_amd64.s:68 +0x89 fp=... sp=... pc=...
```

原因是 `main` 之前就有 Win8+/Win10+ 专属调用：

| 问题 | 官方实现 | 补丁后 |
|---|---|---|
| runtime 取随机数 | `ProcessPrng`（bcryptprimitives.dll，Win8+） | `BCryptGenRandom`（bcrypt.dll，Win7 有） |
| 长路径支持 | `RtlGetCurrentPeb` + PEB 位域（Win10 15063+） | 直接关闭，canUseLongPaths=false |
| 旧控制台句柄 | 假定是真句柄 | Win7 上回退为「在当前进程内 Duplicate」 |
| LoadLibraryExW 新标志 | 不识别 → 加载失败 | 未更新系统回退到 `LoadLibraryA`（legacy 补丁） |

## 文件

- `go-1-26.diff`：标准补丁（必须）。适用于 go1.26.x。
- `legacy-1-26.diff`：过老补丁（可选，但建议打上）。对应「没装到 2016-04 全部更新」
  的老系统，把 `LoadLibraryExW` 换成 `LoadLibraryA` 回退、socket 回退等。
- `LICENSE`：GPL-3.0（构建期工具链补丁；mizar 自身不受其许可约束）。

## 使用

```bash
scripts/build-win7.sh              # 首次会自动建一次 SDK（约 5-8 分钟）
scripts/build-win7.sh --rebuild-sdk
```

产物：`dist/mizar-windows-amd64.exe`，可用 `scripts/pe-check-win7.py` 自检。

## 系统前提

Windows 7 **SP1** + `KB4474419`（SHA-2 签名支持）+ 2016-04 之前的便利汇总更新
（或 `KB3020369` + `KB3125574` + `KB4490628`）。通俗判据：能正常跑 Chrome 109 就行。
