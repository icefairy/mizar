# Needle 2 集成到 Mizar 技术方案

> 日期：2026-09-17
> 状态：草案 v0.1
> 负责人：玄凌子

## 一、Needle 2 核心能力

### 1.1 基本规格

| 项目 | 数据 |
|---|---|
| 参数量 | 45M |
| 模型体积 | 14MB 二进制 |
| 运行时内存 | 28MB/session |
| 推理速度 | ~264 TPS (CPU) |
| 目标设备 | 手机/手表/智能家居/机器人（**无 GPU/NPU**） |

### 1.2 核心优势

1. **100% 合法 JSON 输出**：字节级语法编译，从 JSON Schema 直接生成解码器
2. **工具选择准确率高**：专门训练的工具调用模型
3. **离线运行**：不需要云端 API，适合边缘设备
4. **多工具并行调用**：支持一次调用多个工具

### 1.3 Python API（已测试通过）

```python
import needle

@needle.tool
def set_light(room: str, state: str) -> str:
    """Set the state of a light.
    
    Args:
        room: The room name
        state: on or off
    """
    return f"Light in {room} is now {state}"

agent = needle.Needle(tools=[set_light])
result = agent.run("Turn on the living room light")
# Result: {"type": "respond", "results": ["Light in living room is now on"]}
```

## 二、Mizar 当前 Tool Call 架构

### 2.1 数据流

```
用户请求
  │
  ▼
┌─────────────────┐
│  LLM（大模型）   │ ← 返回原生 tool_calls JSON
│  (DeepSeek等)   │
└─────────────────┘
  │
  ▼
┌─────────────────┐
│ toolCallsToText │ ← 转换为 agent 格式
│                 │   {"action":"tool","tool":"xxx","args":"{...}"}
└─────────────────┘
  │
  ▼
┌─────────────────┐
│ parseCallJSON   │ ← 解析 JSON，提取 action/tool/args
│                 │ ← 失败则 retry 或报错
└─────────────────┘
  │
  ▼
┌─────────────────┐
│  Agent 循环     │ ← 执行工具，获取结果
└─────────────────┘
```

### 2.2 痛点

1. **JSON 格式不稳定**：大模型经常输出畸形 JSON，需要 `tryFixJSON()` 修复
2. **工具选择不准**：弱模型选错工具或参数错误
3. **延迟高**：大模型推理 + retry 逻辑增加延迟

## 三、集成方案

### 3.1 方案 A：前置路由层（推荐）

```
用户请求
  │
  ▼
┌─────────────────┐
│ Needle 2 (本地) │ ← 工具选择 + 参数提取
│  14MB 模型     │   100% 合法 JSON
└─────────────────┘
  │
  ▼
┌─────────────────┐
│ 确定性路由     │ ← 直接执行，无需 retry
│  (Go 代码)     │
└─────────────────┘
  │
  ▼
┌─────────────────┐
│ 工具执行       │
└─────────────────┘
  │
  ▼
┌─────────────────┐
│ 大模型（可选）  │ ← 复杂推理时才调用
└─────────────────┘
```

**优点**：
- 简单工具调用本地处理，延迟 <50ms
- 100% 合法 JSON，无需 retry 逻辑
- 减少大模型调用次数，降低成本

**缺点**：
- Needle 2 上下文窗口只有 256 tokens，复杂场景仍需大模型
- 需要维护两套模型（Needle 2 + 大模型）

### 3.2 方案 B：混合模式

```
用户请求
  │
  ▼
┌─────────────────┐
│ Needle 2 (本地) │ ← 意图识别 + 工具选择
└─────────────────┘
  │
  ├─ 简单工具 ───▶ 直接执行
  │
  └─ 复杂推理 ───▶ 大模型
```

**优点**：
- 平衡速度和能力
- 简单场景快速响应，复杂场景用大模型

**缺点**：
- 需要意图分类器
- 边界判断有歧义

### 3.3 方案 C：纯本地（不推荐）

仅用 Needle 2 处理所有工具调用，不调大模型。

**缺点**：
- 上下文窗口太小（256 tokens）
- 无法处理复杂推理
- 不适合 Mizar 的使用场景

## 四、Go 集成方案

### 4.1 方案：CGO 调用 Needle 2

```go
// needle_go.go
package mizar

/*
#cgo CFLAGS: -I/path/to/needle/include
#cgo LDFLAGS: -lneedle -lm
#include "needle.h"

// Go callback for tool execution
extern int go_tool_callback(const char* tool_name, const char* args_json, char* result, int result_size);

// Needle tool executor wrapper
int needle_run_tool(const char* query, const char* tools_json) {
    // 调用 Needle 2 C API
    // 通过 callback 执行工具
}
*/
import "C"

import (
    "encoding/json"
    "unsafe"
)

// NeedleClient 封装 Needle 2 客户端
type NeedleClient struct {
    handle unsafe.Pointer
}

// NewNeedleClient 创建 Needle 客户端
func NewNeedleClient() (*NeedleClient, error) {
    // 初始化 Needle 2
    // ...
}

// SelectTool 选择工具并提取参数
func (nc *NeedleClient) SelectTool(query string, tools []Tool) (selectedTool string, args map[string]interface{}, err error) {
    // 调用 Needle 2
    // 解析返回的 JSON
    // ...
}

// Tool 定义工具
type Tool struct {
    Name        string      `json:"name"`
    Description string      `json:"description"`
    Params      json.RawMessage `json:"parameters"`
}
```

### 4.2 方案：HTTP Server（更简单）

```go
// needle_server.go
package mizar

import (
    "net/http"
    "encoding/json"
)

// NeedleServer Needle 2 作为独立 HTTP 服务
type NeedleServer struct {
    baseURL string
    client  *http.Client
}

// NewNeedleServer 创建 Needle 服务客户端
func NewNeedleServer(url string) *NeedleServer {
    return &NeedleServer{
        baseURL: url,
        client:  &http.Client{},
    }
}

// SelectTool 选择工具
func (ns *NeedleServer) SelectTool(query string, tools []Tool) (SelectedTool, error) {
    req := map[string]interface{}{
        "query": query,
        "tools": tools,
    }
    
    resp, err := ns.client.Post(ns.baseURL+"/select", "application/json", ...)
    // ...
}
```

### 4.3 方案：WASM（最灵活）

```go
// needle_wasm.go
package mizar

/*
#include <stdint.h>
extern int32_t needle_select_tool(uint8_t* query, int32_t query_len, 
                                  uint8_t* tools, int32_t tools_len,
                                  uint8_t* result, int32_t result_size);
*/
import "C"
```

**优点**：
- 无需 CGO
- 可在浏览器端运行
- 跨平台

**缺点**：
- 需要编译 WASM 版本
- 性能略低

## 五、实施计划

### 阶段 1：原型验证（3天）

- [ ] 编译 Needle 2 的 C/WASM 版本
- [ ] 实现 Go 绑定（优先 WASM 方案）
- [ ] 测试工具选择准确率
- [ ] 对比大模型工具选择准确率

### 阶段 2：集成测试（3天）

- [ ] 在 Mizar 中添加 Needle 前置层
- [ ] 实现工具路由逻辑
- [ ] 测试简单工具调用场景
- [ ] 测试复杂推理场景（降级到大模型）

### 阶段 3：优化迭代（持续）

- [ ] 调优 Prompt 模板
- [ ] 收集工具调用日志，优化 Needle 2 的 tool 描述
- [ ] 考虑 LoRA 微调（针对 Mizar 的工具集）

## 六、预期收益

| 指标 | 现状 | 目标 | 提升 |
|---|---|---|---|
| 工具选择准确率 | ~85% | ~95% | +10% |
| 简单工具调用延迟 | ~500ms | ~50ms | 10x |
| JSON 格式错误率 | ~15% | ~0% | 消除 |
| 大模型调用次数 | 100% | ~30% | 减少 70% |

## 七、风险与应对

| 风险 | 影响 | 应对措施 |
|---|---|---|
| Needle 2 不支持中文 | 工具选择失败 | Prompt 英文化，或微调中文版本 |
| 工具数量增多后准确率下降 | 选错工具 | 使用 tool_index_path 参数，限制工具数量 |
| CGO 编译复杂 | 构建困难 | 优先 WASM 方案 |
| 内存占用 | 资源紧张 | Needle 2 仅需 28MB，可接受 |

## 八、结论

**建议采用方案 A（前置路由层）**：

1. Needle 2 负责简单工具选择（灯光控制、查询天气等）
2. 复杂推理仍走大模型
3. 用 WASM 方案集成，避免 CGO 复杂度

**下一步**：先编译 Needle 2 的 WASM 版本，验证 Go 集成可行性。
