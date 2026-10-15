#!/usr/bin/env python3
"""
Needle 2 HTTP Server
为 Mizar 提供工具选择 API（100% 合法 JSON 输出）
"""

import json
import sys
import logging
from typing import Optional

# 设置日志
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(name)s - %(levelname)s - %(message)s')
logger = logging.getLogger("needle-server")

# 延迟导入 needle（可能在无网络环境失败）
try:
    import needle
except ImportError as e:
    logger.error(f"Failed to import needle: {e}")
    logger.error("Install with: pip install cactus-needle")
    sys.exit(1)

# ==================== 工具定义 ====================

@needle.tool
def search_docs(query: str, category: str = "all") -> str:
    """Search documentation for a query.
    
    Args:
        query: Search query
        category: Category filter (all/code/docs)
    """
    # 模拟搜索
    return f"Found 3 results for '{query}' in category '{category}'"

@needle.tool
def execute_command(command: str, timeout: int = 30) -> str:
    """Execute a shell command.
    
    Args:
        command: Shell command to execute
        timeout: Timeout in seconds
    """
    # 模拟执行
    return f"Command '{command}' executed successfully"

@needle.tool
def read_file(path: str, lines: int = 100) -> str:
    """Read a file and return its content.
    
    Args:
        path: File path
        lines: Max lines to read
    """
    # 模拟读取
    return f"Content of {path} ({lines} lines)"

@needle.tool
def list_processes(filter_str: str = "") -> str:
    """List running processes.
    
    Args:
        filter_str: Filter string (optional)
    """
    # 模拟列出进程
    return "Process list: [python3, node, mizar]"

# ==================== 初始化 Needle Agent ====================

# 创建工具列表
TOOLS = [search_docs, execute_command, read_file, list_processes]

# 创建 Needle 实例
agent = needle.Needle(tools=TOOLS)

# ==================== 简单测试 ====================

def test_needle():
    """测试 Needle 2 是否能正常工作"""
    logger.info("Running basic tests...")
    
    # 测试 1: 简单工具调用
    result = agent.run("Search docs for 'API reference'")
    logger.info(f"Test 1 - Search: {json.dumps(result, indent=2, ensure_ascii=False)}")
    
    # 测试 2: 多工具调用
    result = agent.run("Run command 'ls -la' and read file '/etc/hosts'")
    logger.info(f"Test 2 - Multiple: {json.dumps(result, indent=2, ensure_ascii=False)}")
    
    return True

# ==================== 主入口 ====================

if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="Needle 2 HTTP Server")
    parser.add_argument("--port", type=int, default=8765, help="Port to listen on (default: 8765)")
    parser.add_argument("--test", action="store_true", help="Run tests and exit")
    args = parser.parse_args()
    
    if args.test:
        success = test_needle()
        sys.exit(0 if success else 1)
    
    # 启动 HTTP server（需要 fastapi + uvicorn）
    try:
        from fastapi import FastAPI, HTTPException
        from pydantic import BaseModel
        import uvicorn
        
        app = FastAPI(title="Needle 2 Server", description="Tool selection API for Mizar")
        
        class ToolRequest(BaseModel):
            query: str
            tools: Optional[list] = None
        
        class ToolResponse(BaseModel):
            tool: str
            arguments: dict
            confidence: float
            reasoning: str
        
        @app.get("/health")
        async def health():
            return {"status": "ok", "model": "needle-2"}
        
        @app.post("/select", response_model=ToolResponse)
        async def select_tool(req: ToolRequest):
            """Select tool and extract arguments"""
            try:
                result = agent.run(req.query)
                
                # 解析结果
                if result.get("type") == "respond" and result.get("function_calls"):
                    call = result["function_calls"][0]
                    return ToolResponse(
                        tool=call["name"],
                        arguments=json.loads(call["arguments"]),
                        confidence=result.get("confidence", 0.0),
                        reasoning=result.get("reasoning", "")
                    )
                else:
                    # 没有工具调用，返回文本回复
                    return ToolResponse(
                        tool="",
                        arguments={},
                        confidence=result.get("confidence", 0.0),
                        reasoning=result.get("reasoning", "")
                    )
            except Exception as e:
                logger.error(f"Error: {e}")
                raise HTTPException(status_code=500, detail=str(e))
        
        @app.post("/extract")
        async def extract_structured(req: ToolRequest):
            """Extract structured data from text"""
            try:
                result = needle.extract(
                    req.query,
                    schema={"type": "object", "properties": {"key": {"type": "string"}}}
                )
                return {"data": result}
            except Exception as e:
                logger.error(f"Error: {e}")
                raise HTTPException(status_code=500, detail=str(e))
        
        logger.info(f"Starting Needle 2 server on port {args.port}...")
        uvicorn.run(app, host="0.0.0.0", port=args.port)
        
    except ImportError as e:
        logger.error(f"FastAPI/Uvicorn not installed: {e}")
        logger.error("Install with: pip install fastapi uvicorn")
        sys.exit(1)
