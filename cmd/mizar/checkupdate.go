package main

// /checkupdate —— 从项目主页检测最新版本。
//
// 设计取舍（大道至简）：
//   - 不引入 HTML 解析依赖：站点结构简单固定（badge + h3），正则足够且抗噪。
//   - 不做自动更新：只提醒用户，由用户自行决定（明确需求）。
//   - 失败一律降级为友好提示，绝不影响其他功能。

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const updateSiteURL = "https://icefairy.github.io/mizar/"

// semverRe 匹配语义化版本号（要求三段数字 + 可选 v 前缀，避免把 "AGPL v3" 当成版本）。
var semverRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// siteInfo 站点解析结果。
type siteInfo struct {
	Version string // 形如 v0.5.3
	Title   string // 最新动态标题（去掉版本号部分）
	Summary string // 最新动态正文摘要
}

// badgeRe 抓版本徽章：<div class="badge">v0.5.3</div>
var badgeRe = regexp.MustCompile(`class="badge"[^>]*>\s*(v\d+\.\d+\.\d+)\s*<`)

// newsRe 抓最新动态首条卡片：<h3>v0.5.3 · 标题</h3> ... <p>正文</p>
var newsRe = regexp.MustCompile(`(?s)<h3>\s*v\d+\.\d+\.\d+\s*[·\-–]\s*([^<]+?)\s*</h3>\s*<p>(.*?)</p>`)

// htmlTagRe 用于剥离摘要里的 HTML 标签，让输出在终端里可读。
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// parseSiteInfo 从主页 HTML 提取版本与最新动态。
func parseSiteInfo(html string) siteInfo {
	var info siteInfo
	// 优先用 badge（位置更权威）；退化到全文第一个语义化版本。
	if m := badgeRe.FindStringSubmatch(html); len(m) > 1 {
		info.Version = m[1]
	} else if m := semverRe.FindString(html); m != "" {
		info.Version = m
	}
	if m := newsRe.FindStringSubmatch(html); len(m) > 2 {
		info.Title = strings.TrimSpace(htmlTagRe.ReplaceAllString(m[1], ""))
		body := strings.TrimSpace(htmlTagRe.ReplaceAllString(m[2], ""))
		// 折叠空白，避免 <strong>/<code> 剥离后留下多余空格。
		info.Summary = strings.Join(strings.Fields(body), " ")
	}
	return info
}

// compareSemver 比较两个语义化版本，返回 -1/0/1。
// 只比较前三段数字；缺失段按 0 处理（如 v0.5 == v0.5.0）。
func compareSemver(a, b string) int {
	pa, pb := semverParts(a), semverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

// semverParts 提取三段数字，非法或缺失填 0。
func semverParts(v string) [3]int {
	var out [3]int
	m := semverRe.FindStringSubmatch(v)
	if len(m) < 4 {
		return out
	}
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out
}

// fetchSiteInfo 拉取主页并解析，带超时保护。
func fetchSiteInfo(url string, timeout time.Duration) (siteInfo, error) {
	cli := &http.Client{Timeout: timeout}
	resp, err := cli.Get(url)
	if err != nil {
		return siteInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return siteInfo{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return siteInfo{}, err
	}
	return parseSiteInfo(string(body)), nil
}

// formatUpdateReport 生成用户可见的检查结果。
// 无论有无新版都给出明确结论，不静默。
func formatUpdateReport(current string, info siteInfo, err error) string {
	if err != nil {
		return fmt.Sprintf("✗ 检查更新失败（网络不可达或站点异常）: %v\n  当前版本: %s\n  可稍后重试，或手动访问 %s", err, current, updateSiteURL)
	}
	if info.Version == "" {
		return fmt.Sprintf("✗ 未能从站点解析出版本号（站点结构可能已变）\n  当前版本: %s\n  请手动访问 %s", current, updateSiteURL)
	}

	var sb strings.Builder
	switch cmp := compareSemver(info.Version, current); {
	case cmp > 0:
		fmt.Fprintf(&sb, "🔔 发现新版本: %s（当前 %s）\n", info.Version, current)
		if info.Title != "" {
			fmt.Fprintf(&sb, "\n  更新内容: %s\n", info.Title)
		}
		if info.Summary != "" {
			fmt.Fprintf(&sb, "  %s\n", info.Summary)
		}
		sb.WriteString("\n  本命令不会自动更新。请自行获取新版本后替换二进制：\n")
		fmt.Fprintf(&sb, "    详情/下载: %s\n", updateSiteURL)
	case cmp < 0:
		fmt.Fprintf(&sb, "✓ 当前版本 %s 比站点版本 %s 更新（可能是本地开发版）\n  站点: %s", current, info.Version, updateSiteURL)
	default:
		fmt.Fprintf(&sb, "✓ 已是最新版本 %s", current)
		if info.Title != "" {
			fmt.Fprintf(&sb, "（%s）", info.Title)
		}
	}
	return sb.String()
}
