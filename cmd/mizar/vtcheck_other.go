//go:build !windows

package main

// 非 Windows 平台：控制台探测与提示均为空实现（Unix 终端天然支持 VT）。

func consoleVTEnabled() bool { return true }

func windowsVersionHint() string { return "" }

func ensureConsoleUTF8() {}
