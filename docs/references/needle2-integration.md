# Needle 2 集成模式

## Python API（关键发现）

Needle 2 的 Python API 与直觉不同：

```python
import needle

@needle.tool
def my_tool(x: str) -> str:
    """Description with Args section."""
    return f"Result: {x}"

agent = needle.Needle(tools=[my_tool])

# ✅ 正确：用 complete()，不是 run()
result = agent.complete("Query text")

# 返回格式：
# {"type": "call", "function_calls": [{"name": "my_tool", "arguments": {...}}], "confidence": 0.99}
# {"type": "respond", "results": ["..."], "confidence": 0.xx}  # 不需要调用工具时

# ❌ 错误：run() 不支持 tools 参数
agent.run("...")  # TypeError
```

## 置信度问题

- Needle 2 默认 confidence 很低（<0.02），需自定义阈值
- 可用 LoRA 微调提升：`pip install "cactus-needle[train]"` + `needle finetune data.jsonl`
- 当前建议：confidence > 0.5 才使用，否则 fallback 到正则/大模型

## Go 集成方案

**推荐 HTTP Server 模式**（最简单）：
```
Mizar (Go) ──HTTP──▶ Needle 2 Server (Python :8765)
```

**Go Client 关键点**：
- 用 `http.Client` 发 POST /select
- 解析 `{"tool": "name", "arguments": {...}, "confidence": 0.xx}`
- 高置信度直接用，低置信度 fallback 到现有 JSON parsing

## 已知限制

1. 上下文窗口仅 256 tokens，不适合长对话
2. 中文理解一般，英文更好
3. 需要 `HF_ENDPOINT=https://hf-mirror.com` 环境变量下载模型（国内网络）
4. 当前版本需要 `agent.complete()` 而非 `agent.run()`

## 文件位置

- Python Server: `/data/codes/mizar/cmd/needle_server/server.py`
- Go Client: `/data/codes/mizar/internal/needle/`
- 测试报告: `/data/codes/mizar/docs/needle2-prototype-report.md`
