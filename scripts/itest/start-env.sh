#!/bin/bash
# 启动 yaa 集成测试环境：mock 网关(18080) + yaa(18081，带认证)
# 用法: YAA_BIN=/path/to/yaa [DATA=/tmp/yaa-itest] bash start-env.sh
set -u
YAA_BIN="${YAA_BIN:?请设置 YAA_BIN=/path/to/yaa 二进制}"
DATA="${DATA:-/tmp/yaa-itest}"
mkdir -p "$DATA/skills" "$DATA/plugins" "$DATA/data"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONF="$SCRIPT_DIR/config-integration.yaml"

# 配置里的 /tmp/yaa-test 路径统一替换为 $DATA
sed -e "s#/tmp/yaa-test#$DATA#g" "$CONF" > "$DATA/config-integration.yaml"

# 清理旧进程（用进程名精确匹配，避免误杀当前 shell）
for p in $(pgrep -x yaa-bin 2>/dev/null) $(pgrep -f "config-integration.yaml" 2>/dev/null); do
  kill -9 "$p" 2>/dev/null
done
for p in $(pgrep -f "mock2.py" 2>/dev/null); do
  kill -9 "$p" 2>/dev/null
done
sleep 1

cd "$DATA"
MOCK_API_KEY=itest setsid nohup python3 "$SCRIPT_DIR/mock2.py" > mock.log 2>&1 &
sleep 1
MOCK_API_KEY=itest YAA_TEST_TOKEN=itest-secret-token setsid nohup "$YAA_BIN" \
  --config "$DATA/config-integration.yaml" > yaa.log 2>&1 & 
sleep 3

echo "health:    $(curl -s http://127.0.0.1:18081/api/v1/health | head -c 60)"
echo "agents401: $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18081/api/v1/agents)"
echo "agentsOK:  $(curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer itest-secret-token' http://127.0.0.1:18081/api/v1/agents)"
echo "log tail:  $(tail -2 "$DATA/yaa.log")"