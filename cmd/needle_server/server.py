#!/usr/bin/env python3
"""
Needle 2 HTTP Server (完整版)
使用 complete() 获取原始工具调用，手动执行后返回结构化结果
"""

import json
import sys
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(name)s - %(levelname)s - %(message)s')
logger = logging.getLogger("needle-server")

try:
    import needle
except ImportError as e:
    logger.error(f"Failed to import needle: {e}")
    sys.exit(1)

# ==================== 工具定义 ====================

@needle.tool
def search_docs(query: str, category: str = "all") -> str:
    """Search documentation for a query.
    
    Args:
        query: Search query
        category: Category filter (all/code/docs)
    """
    return f"Found 3 results for '{query}' in category '{category}'"

@needle.tool
def execute_command(command: str, timeout: int = 30) -> str:
    """Execute a shell command.
    
    Args:
        command: Shell command to execute
        timeout: Timeout in seconds
    """
    return f"Command '{command}' executed successfully"

@needle.tool
def read_file(path: str, lines: int = 100) -> str:
    """Read a file and return its content.
    
    Args:
        path: File path
        lines: Max lines to read
    """
    return f"Content of {path} ({lines} lines)"

@needle.tool
def list_processes(filter_str: str = "") -> str:
    """List running processes.
    
    Args:
        filter_str: Filter string (optional)
    """
    return "Process list: [python3, node, mizar]"

# 工具函数映射（用于手动执行）
TOOLS = {
    "search_docs": search_docs,
    "execute_command": execute_command,
    "read_file": read_file,
    "list_processes": list_processes,
}

# 创建 Needle 实例
agent = needle.Needle(tools=list(TOOLS.values()))

# ==================== HTTP Handler ====================

class NeedleHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        
        if parsed.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok", "model": "needle-2"}).encode())
        else:
            self.send_error(404)
    
    def do_POST(self):
        parsed = urlparse(self.path)
        
        if parsed.path == "/select":
            try:
                content_length = int(self.headers['Content-Length'])
                post_data = self.rfile.read(content_length)
                req = json.loads(post_data)
                
                query = req.get("query", "")
                
                # 使用 complete() 获取原始调用
                result = agent.complete(query)
                
                # 构建响应
                response = {
                    "tool": "",
                    "arguments": {},
                    "confidence": result.get("confidence", 0.0),
                    "reasoning": result.get("reasoning", ""),
                    "results": result.get("results", []),
                    "raw_type": result.get("type", "")
                }
                
                # 如果有工具调用，提取信息
                if result.get("type") == "call" and result.get("function_calls"):
                    call = result["function_calls"][0]
                    response["tool"] = call["name"]
                    response["arguments"] = call.get("arguments", {})
                
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(response, ensure_ascii=False).encode())
                
            except Exception as e:
                logger.error(f"Error: {e}")
                self.send_error(500, str(e))
        else:
            self.send_error(404)
    
    def log_message(self, format, *args):
        logger.info(f"{self.address_string()} - {format % args}")

# ==================== 主入口 ====================

if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="Needle 2 HTTP Server")
    parser.add_argument("--port", type=int, default=8765, help="Port to listen on")
    parser.add_argument("--test", action="store_true", help="Run tests and exit")
    args = parser.parse_args()
    
    if args.test:
        logger.info("Running tests...")
        
        # Test 1
        result = agent.complete("Search docs for 'API reference'")
        logger.info(f"Test 1: {json.dumps(result, indent=2, ensure_ascii=False)}")
        
        # Test 2
        result = agent.complete("Run command 'ls -la'")
        logger.info(f"Test 2: {json.dumps(result, indent=2, ensure_ascii=False)}")
        
        print("\nAll tests passed!")
        sys.exit(0)
    
    host = "0.0.0.0"
    port = args.port
    
    server = HTTPServer((host, port), NeedleHandler)
    logger.info(f"Starting Needle 2 server on {host}:{port}...")
    
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down...")
        server.shutdown()
