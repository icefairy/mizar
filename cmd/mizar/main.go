// Mizar (开阳) —— 极简自举 Agent CLI。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/llm"
	"mizar/internal/plugins"
)

var version = "v0.1.0"

func main() {
	var (
		baseURL = flag.String("base-url", "http://127.0.0.1:3002/v1", "OpenAI 兼容端点 (默认指向璇玑网关)")
		apiKey  = flag.String("api-key", "", "API Key (可选)")
		model   = flag.String("model", "deepseek-v4-flash", "模型名")
		extDir  = flag.String("ext", "extensions", "插件目录")
		task    = flag.String("task", "", "任务内容 (非空则单次执行)")
		showVer = flag.Bool("version", false, "显示版本")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("mizar", version)
		return
	}

	// 宿主函数集
	host := &engine.HostFuncs{
		FSRead:  func(p string) (string, error) { b, e := os.ReadFile(p); return string(b), e },
		FSWrite: func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
		FSList:  func(dir string) ([]string, error) {
			es, e := os.ReadDir(dir)
			if e != nil {
				return nil, e
			}
			out := make([]string, 0, len(es))
			for _, x := range es {
				out = append(out, x.Name())
			}
			return out, nil
		},
		Log: func(msg string) { log.Print(msg) },
	}
	// 提供真实 LLM 给插件 llm_chat
	client := llm.NewOpenAI(*baseURL, *apiKey, *model)
	host.LLMChat = func(messagesJSON string) (string, error) {
		var msgs []agent.Message
		if err := jsonUnmarshal(messagesJSON, &msgs); err != nil {
			return "", err
		}
		return client.Chat(msgs)
	}

	extAbs, err := filepath.Abs(*extDir)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(extAbs); os.IsNotExist(err) {
		if err := os.MkdirAll(extAbs, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	pm := plugins.NewManager(extAbs, host)
	loaded, failed := pm.LoadAll()
	for _, f := range loaded {
		log.Printf("插件加载: %s", f)
	}
	for f, e := range failed {
		log.Printf("插件失败: %s: %v", f, e)
	}
	tools := pm.Tools()
	log.Printf("可用工具 %d 个", len(tools))
	for _, t := range tools {
		fmt.Printf("  - %s\n", t.Name)
	}

	a := agent.New(client, pm)
	a.System = `你是开阳(Mizar) Agent，一个极简自举的智能体。你可以调用工具完成任务，工具出错时尝试修复或换一种方式。请用简洁的中文回答。`

	if *task != "" {
		reply, err := a.Run(*task)
		if err != nil {
			log.Fatalf("执行失败: %v", err)
		}
		fmt.Println("\n=== 最终回答 ===")
		fmt.Println(reply)
		return
	}

	// 交互模式
	interactive(a)
}
