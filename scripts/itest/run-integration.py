#!/usr/bin/env python3
# yaa 工具集集成验证：经真实 Agent turn 逐一调用每个内置工具并断言结果
# 复用一个会话（顺带验证多轮状态与消息持久化）。
import json, re, sys, time, urllib.request

import os
BASE = "http://127.0.0.1:18081"
TOKEN = "itest-secret-token"
DATA = os.environ.get("ITEST_DATA", "/tmp/yaa-itest")

def api(path, method="GET", body=None, timeout=60):
    req = urllib.request.Request(BASE + path, method=method)
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Content-Type", "application/json")
    data = json.dumps(body).encode() if body is not None else None
    try:
        with urllib.request.urlopen(req, data, timeout=timeout) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        return json.loads(e.read())

def post_message(session_id, turn_id, content, timeout=120):
    """POST /messages 并返回完整 JSON（含消息/usage）。"""
    body = {"turn_id": turn_id, "content": content}
    d = api(f"/api/v1/sessions/{session_id}/messages", "POST", body, timeout=timeout)
    assert d.get("code") == 0, f"turn failed: {d}"
    return d["data"]

def messages(session_id):
    d = api(f"/api/v1/sessions/{session_id}/messages?page_size=200")
    return d["data"]["items"]

def last_assistant_text(session_id):
    ms = messages(session_id)
    for m in reversed(ms):
        if m["role"] == "assistant" and m.get("content"):
            return m["content"]
    return ""

def run_tool(session_id, name, args, check=None, label=None):
    """发起 !!tool 调用并返回最终 assistant 文本。check 为可调用谓词。"""
    turn_id = "turn_" + str(int(time.time() * 1000000)) + "_" + name[:10]
    content = "!!tool " + name + " " + json.dumps(args, ensure_ascii=False)
    post_message(session_id, turn_id, content)
    text = last_assistant_text(session_id)
    label = label or name
    ok = bool(("TOOL_OK" in text) and (check is None or check(text)))
    status = "PASS" if ok else "FAIL"
    print(f"[{status}] {label}")
    if not ok:
        print("   args:", json.dumps(args, ensure_ascii=False)[:120])
        print("   reply:", text[-300:])
    return ok

def main():
    results = []
    # 1) 准备 fixture
    s = api("/api/v1/agents/default/sessions", "POST", {"metadata": {"title": "itest"}})
    sid = s["data"]["id"]
    print(f"session={sid}")

    # 2) 普通对话（多轮）
    post_message(sid, "turn_" + str(time.time()) + "_plain1", "你好")
    t1 = last_assistant_text(sid)
    post_message(sid, "turn_" + str(time.time()) + "_plain2", "再聊一句")
    t2 = last_assistant_text(sid)
    ok = "模拟回复" in t1 and "再聊一句" in t2
    results.append(("plain multi-turn", ok))
    print(f"[{'PASS' if ok else 'FAIL'}] plain multi-turn")

    # 3) shell
    results.append(("shell", run_tool(sid, "shell", {"command": "echo itest-shell-ok"},
                                      check=lambda t: "itest-shell-ok" in t)))

    # 4) http
    results.append(("http", run_tool(sid, "http", {"url": "http://127.0.0.1:18080/v1/test/hello"},
                                     check=lambda t: "from-mock" in t and "hello" in t)))

    # 5) http blocked host（期望失败提示）
    results.append(("http-blocked", run_tool(sid, "http", {"url": "https://example.com/path"},
                                             check=lambda t: "host blocked" in t)))

    # 6) file_write + file_read + file_list
    w = run_tool(sid, "file_write", {"path": "itest/f.txt", "content": "itest-file-content\n", "create_dirs": True},
                 check=lambda t: "wrote" in t)
    r = run_tool(sid, "file_read", {"path": "itest/f.txt"},
                 check=lambda t: "itest-file-content" in t)
    l = run_tool(sid, "file_list", {"path": "itest"},
                 check=lambda t: "f.txt" in t)
    results += [("file_write", w), ("file_read", r), ("file_list", l)]

    # 7) file_delete
    d = run_tool(sid, "file_delete", {"path": "itest/f.txt"},
                 check=lambda t: "removed" in t or "deleted" in t)
    # 删除后 file_read 应为错误
    gone = run_tool(sid, "file_read", {"path": "itest/f.txt"},
                    check=lambda t: "error" in t.lower() or "no such file" in t.lower() or "file not found" in t.lower())
    results += [("file_delete", d), ("file_delete-check", gone)]

    # 8) file_search（先写一个搜索目标）
    run_tool(sid, "file_write", {"path": "itest/search-target.txt", "content": "needle-xyz123\nline2\n", "create_dirs": True})
    fs = run_tool(sid, "file_search", {"path": "itest", "query": "needle-xyz123", "use_gitignore": False},
                  check=lambda t: "needle-xyz123" in t and "search-target" in t)
    results.append(("file_search", fs))

    # 9) git_status/git_log（gitrepo fixture）
    gs = run_tool(sid, "git_status", {"repo_path": os.path.join(DATA, "gitrepo")},
                  check=lambda t: "nothing to commit" in t or "working tree clean" in t)
    gl = run_tool(sid, "git_log", {"repo_path": os.path.join(DATA, "gitrepo"), "limit": 5},
                  check=lambda t: "initial" in t)
    results += [("git_status", gs), ("git_log", gl)]

    # 10) config_query
    cq = run_tool(sid, "config_query", {}, check=lambda t: "config_version" in t or "agents" in t)
    results.append(("config_query", cq))

    # 11) introspection
    tl = run_tool(sid, "tool_list", {}, check=lambda t: "file_search" in t and "git_status" in t)
    al = run_tool(sid, "agent_list", {}, check=lambda t: "default" in t)
    pl = run_tool(sid, "provider_list", {}, check=lambda t: "mock" in t)
    sl = run_tool(sid, "session_list", {}, check=lambda t: sid[:10] in t or "msg" in t)
    mcp = run_tool(sid, "mcp_list", {}, check=lambda t: ("[]" in t or "no mcp" in t.lower() or "0" in t))
    rt = run_tool(sid, "runtime_status", {}, check=lambda t: "running" in t.lower() or "ready" in t.lower())
    results += [("tool_list", tl), ("agent_list", al), ("provider_list", pl),
                ("session_list", sl), ("mcp_list", mcp), ("runtime_status", rt)]

    # 12) process start/list/logs/stop（三步依赖）
    ps = run_tool(sid, "process_start", {"command": "/bin/sh", "args": ["-c", "echo proc-itest-hello; sleep 30"]},
                  check=lambda t: "proc_" in t)
    results.append(("process_start", ps))
    # 从上一轮 assistant 消息里解析 proc id
    pid = None
    for m in reversed(messages(sid)):
        if m["role"] == "assistant" and "proc_" in (m.get("content") or "") and "TOOL_OK" in (m.get("content") or ""):
            mm = re.search(r"(proc_\d+)", m["content"])
            if mm:
                pid = mm.group(1)
            break
    if pid:
        pl_ = run_tool(sid, "process_logs", {"id": pid}, check=lambda t: "proc-itest-hello" in t)
        pst = run_tool(sid, "process_stop", {"id": pid, "force": True}, check=lambda t: "stopped" in t)
        results += [("process_logs", pl_), ("process_stop", pst)]
    else:
        results += [("process_logs", False), ("process_stop", False)]
        print("[FAIL] process id not parsed")
        for m in messages(sid)[-6:]:
            print("   ", m["role"], (m.get("content") or "")[:120])

    # ---- 汇总 ----
    print("\n==== 集成验证汇总 ====")
    fails = [k for k, ok in results if not ok]
    for k, ok in results:
        print(f"  {'✓' if ok else '✗'} {k}")
    print(f"\n总 {len(results)} 项，失败 {len(fails)} 项：{fails}")
    return 1 if fails else 0

if __name__ == "__main__":
    sys.exit(main())