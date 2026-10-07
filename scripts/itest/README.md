# yaa 集成测试基建（脚本级）

针对「经真实 Agent turn 调用每个内置工具」与「WebUI 浏览器场景」的可复现验证。

## 依赖
- 端口固定：mock 网关 `127.0.0.1:18080`，yaa `127.0.0.1:18081`
- token：`itest-secret-token`（`config-integration.yaml` 里以 `${YAA_TEST_TOKEN}` 注入）
- 可选 WebUI 场景：无头 Chromium（`--remote-debugging-port=19230`）+ Node≥22

## 用法
```bash
# 1) 构建 yaa
cd /opt/yaa && go build -o /tmp/yaa-bin ./cmd/yaa
# 2) 启动环境（mock + yaa，带认证；脚本内自动清理旧进程）
YAA_BIN=/tmp/yaa-bin bash start-env.sh
#    —— 注意：start-env.sh 固定引用 /tmp/ yaafi – 按需改脚本里的 YAA_BIN 与端口
# 3) 工具集集成验证（22 项，真实 Agent turn）
MOCK_API_KEY=itest YAA_TEST_TOKEN=itest-secret-token python3 run-integration.py
# 4) WebUI 浏览器场景（8 个场景，含认证/流式/工具卡/中止/删除/主题）
#    先手动起：chromium --headless=new --no-sandbox --remote-debugging-port=19230 ...
node run-webui-dom.js ws://127.0.0.1:19230/devtools/page/<id>
```

## 说明
- mock2.py：OpenAI 兼容网关。用户消息 `!!tool <name> <args-json>` → 先返回该工具的
  tool_call，工具执行后返回 `TOOL_OK <结果原文>`（便于断言真实链路）；`!!slow` 为慢速流
  用于测试中止；`/v1/test/hello` 供 http 工具测试。
- run-integration.py：单会话多轮，逐一调用 shell/http/file_*/file_search/git_*/config_query/
  introspection/process_*，断言 agent 最终回复回显了真实工具输出。
- run-webui-dom.js：纯 DOM 断言（生产构建无内部状态钩子），覆盖 401 降级、设置重连、
  新建会话、流式渲染、工具卡片、中止生成、明暗主题、删除会话，并汇总 console 异常。
