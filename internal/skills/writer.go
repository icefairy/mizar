package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SkillWriter 自举沉淀：把"任务 → 插件 → 经验"固化为 SKILL.md。
// Agent 完成一次任务后调用它，把做法写成技能，下次直接复用（无需重新写代码）。
type SkillWriter struct {
	dir string
}

// NewWriter 创建沉淀写入器。
func NewWriter(dir string) *SkillWriter {
	return &SkillWriter{dir: dir}
}

// CreateFromPlugin 根据一个成功的插件生成 SKILL.md 技能。
// 参数:
//   - name: 技能名（如 disk-watch）
//   - description: 技能描述（给 LLM 看的触发条件）
//   - pluginName: 已加载的插件文件名（如 disk_watch.ts）
//   - tools: 插件提供的工具列表（tool_ 前缀去掉）
//   - usage: 使用说明（Agent 写的最优做法）
func (w *SkillWriter) CreateFromPlugin(name, description, pluginName string, tools []string, usage string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("skill name required")
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	sb.WriteString(fmt.Sprintf("description: %s\n", description))
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# %s\n\n", name))
	sb.WriteString("## 触发条件\n")
	sb.WriteString(fmt.Sprintf("当需要%s时使用。\n\n", description))
	sb.WriteString("## 依赖插件\n")
	sb.WriteString(fmt.Sprintf("插件文件：`%s`（自动加载，提供工具：%s）\n\n", pluginName, strings.Join(tools, ", ")))
	sb.WriteString("## 使用方式\n")
	sb.WriteString(usage)
	if !strings.HasSuffix(usage, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n---\n")
	sb.WriteString(fmt.Sprintf("_由 Mizar 自举沉淀生成于 %s_\n", time.Now().Format("2006-01-02 15:04")))
	path := filepath.Join(w.dir, name+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// CreateFromText 直接写一个纯文本技能（不需要插件）。
func (w *SkillWriter) CreateFromText(name, description, body string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("skill name required")
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)
	path := filepath.Join(w.dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
