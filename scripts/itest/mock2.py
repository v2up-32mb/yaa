# Yaa 集成测试 mock 网关 v2
# 协议：OpenAI /v1/chat/completions（流式 + 非流式）
# 用户消息 "!!tool <name> <args-json>" → 第一次回复一个 tool_call；
# 收到 role=tool 消息后第二次回复 TOOL_OK: <工具结果原文>。
# 其余内容 → 普通 markdown 回复；以 "!!msgs" 开头 → 把最近消息摘要打回。
# 附带 /v1/test/hello 与 /v1/test/echo 端点供 http 工具测试。
import json, time
from http.server import HTTPServer, BaseHTTPRequestHandler

HELLO = '{"hello":"from-mock"}'

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, ctype, body):
        self.send_response(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith('/v1/test'):
            if self.path == '/v1/test/hello':
                return self._send(200, 'application/json', HELLO.encode())
            if self.path.startswith('/v1/test/echo'):
                return self._send(200, 'text/plain', b'echo:' + self.path.encode())
        self._send(404, 'text/plain', b'not found')

    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0))
        body = json.loads(self.rfile.read(n) or b'{}')
        stream = body.get('stream', False)
        if self.path.endswith('/chat/completions'):
            return self._completions(body, stream)
        self._send(404, 'text/plain', b'not found')

    def _completions(self, body, stream):
        msgs = body.get('messages', [])
        user_msgs = [m.get('content') or '' for m in msgs if m.get('role') == 'user']
        last_user = user_msgs[-1] if user_msgs else ''
        has_tool = bool(msgs) and msgs[-1].get("role") == "tool"

        if has_tool:
            # 工具已执行：把最后一次工具结果回显给 agent，便于断言真实链路
            tool_msgs = [m for m in msgs if m.get('role') == 'tool']
            result = tool_msgs[-1].get('content') or '(empty)'
            out = f"工具执行完成。结果如下（务必原样引用）：\n\n```\nTOOL_OK {result}\n```"
            usage = {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
            return self._reply(body, stream, [out], usage)
        elif last_user.startswith('!!tool'):
            parts = last_user.split(' ', 2)
            name = parts[1] if len(parts) > 1 else 'shell'
            args = {}
            if len(parts) > 2:
                try:
                    args = json.loads(parts[2])
                except Exception:
                    args = {"raw": parts[2]}
            tool_call = {"id": "call_it_" + str(int(time.time() * 1000))[-10:],
                         "type": "function",
                         "function": {"name": name, "arguments": json.dumps(args, ensure_ascii=False)}}
            if stream:
                return self._stream_tool_call(tool_call)
            msg = {"role": "assistant", "content": None, "tool_calls": [tool_call]}
            resp = {"id": "c1", "object": "chat.completion",
                    "choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls"}],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}}
            return self._send(200, 'application/json', json.dumps(resp).encode())
        elif last_user.startswith('!!slow'):
            text = "这是一段故意放慢的流式输出，用于测试中止按钮。" * 4
            if stream:
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.end_headers()
                for i in range(0, len(text), 3):
                    self._w(b'data: ' + json.dumps({"id": "s", "object": "chat.completion.chunk",
                                                    "choices": [{"index": 0, "delta": {"content": text[i:i+3]},
                                                                 "finish_reason": None}]}).encode() + b'\n\n')
                    time.sleep(0.25)
                self._w(b'data: [DONE]\n\n')
                return
            return self._reply(body, stream, [text], None)
        elif last_user.startswith('!!msgs'):
            summary = "; ".join(f"{m.get('role')}={ (m.get('content') or '')[:40] }" for m in msgs)
            return self._reply(body, stream, [f"收到消息序列: {summary}"], None)
        else:
            out = f"收到：{last_user[:60] or '(empty)'}。这是模拟回复。"
            return self._reply(body, stream, [out], None)

    def _reply(self, body, stream, texts, usage):
        if stream:
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for t in texts:
                for chunk in self._split_text(t):
                    self._w(b'data: ' + json.dumps({"id": "x", "object": "chat.completion.chunk",
                                                    "choices": [{"index": 0, "delta": {"content": chunk},
                                                                 "finish_reason": None}]}).encode() + b'\n\n')
            self._w(b'data: [DONE]\n\n')
            return
        msg = {"role": "assistant", "content": texts[0]}
        resp = {"id": "c1", "object": "chat.completion",
                "choices": [{"index": 0, "message": msg, "finish_reason": "stop"}],
                "usage": usage or {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}}
        self._send(200, 'application/json', json.dumps(resp).encode())

    def _stream_tool_call(self, tool_call):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        self._w(b'data: ' + json.dumps({"id": "c1", "object": "chat.completion.chunk",
                                        "choices": [{"index": 0, "delta": {"tool_calls": [tool_call]},
                                                     "finish_reason": "tool_calls"}]}).encode() + b'\n\n')
        self._w(b'data: [DONE]\n\n')

    def _split_text(self, t):
        # 按 2-4 个字符短块切分，模拟流式
        i, n = 0, len(t)
        while i < n:
            step = 2 + (i * 7) % 3
            yield t[i:i + step]
            i += step
            time.sleep(0.002)

    def _w(self, b):
        self.wfile.write(b)
        self.wfile.flush()


HTTPServer(('127.0.0.1', 18080), H).serve_forever()