#!/usr/bin/env bash
#
# issue-autofix 轮次编排入口（外部调度轨道 / catch-up）
#
# 为什么需要它：pi-subagents 的 schedule 是**进程内 setTimeout**（证据：
# node_modules/pi-subagents/src/runs/background/scheduled-runs.ts:799
# `this.timersApi.setTimeout(...)`），进程退出后不会触发；而 restore() 只在
# 工具调用时执行（同文件 :968）。因此「定时任务」不能只依赖进程内定时器。
#
# 本脚本就是补足的那条轨道：crontab 每 6 小时调用它，它用 `pi -p` 无头拉起
# SOP 执行体，由 SOP 自己去操作 pi-subagents schedule（list/show/run-due）。
#
# 关键工程问题（本脚本存在的真正理由）：**`pi -p` 在模型失败时也可能 exit 0**。
# 实测：默认 provider 会被网关拒绝（403 unsupported_country_region_territory），
# 消息流里没有 assistant 输出，但进程退出码仍是 0。若直接 cron 调用，失败会
# 静默无声 —— 无人值守任务「什么都没做」和「做完了」无法区分。
# 因此这里做三件事：
#   1. 显式写死 --provider/--model（默认 provider 在本机不可用）；
#   2. 解析 JSON 事件流，要求至少出现一次**非空的 assistant 文本**；
#   3. 超时兜底（timeout 命令）+ 日志落盘 + 非零退出码。
#
# 用法：
#   scripts/issue-bot/round.sh                # 跑一轮
#   scripts/issue-bot/round.sh --due-only     # 只拉活 pi 内定时器（schedule.run-due）
#   scripts/issue-bot/round.sh --help
#
# 环境变量：
#   ISSUE_AUTOFIX_PROVIDER  默认 my-custom-provider
#   ISSUE_AUTOFIX_MODEL     默认 deepseek-v4.1-flash
#   ISSUE_AUTOFIX_TIMEOUT   默认 10800（3 小时，秒）
#   ISSUE_AUTOFIX_DRY_RUN   非空则只打印将要执行的命令
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/root/llm}"
WORK_DIR="${ISSUE_AUTOFIX_DIR:-$REPO_ROOT/.pi/issue-autofix}"
LOG_DIR="$WORK_DIR/logs"
SOP="$REPO_ROOT/docs/plans/issue-autofix-prompt.md"

# --------------------------------------------------------------- PATH ----
# cron 的默认 PATH 只有 /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin，
# 而 pi（及其 node）安装在 pi-node 的版本目录内。**实测**：不补全 PATH 时，
# 首次 cron 触发会以 `timeout: failed to run command 'pi': No such file or directory`
# + exit 127 失败 —— 定时任务看起来「装好了」实则一次都没跑成。
# 因此这里显式把 pi 的 bin 目录（用 current 符号链接，避免锁死版本号）前置。
PI_NODE_BIN="${PI_NODE_BIN:-$HOME/.local/share/pi-node/current/bin}"
export PATH="$PI_NODE_BIN:$HOME/.pi/agent/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"

PROVIDER="${ISSUE_AUTOFIX_PROVIDER:-my-custom-provider}"
MODEL="${ISSUE_AUTOFIX_MODEL:-deepseek-v4.1-flash}"
# 默认 4h：必须 **不小于** 调度侧 `timeoutMs`。否则当 cron 轨道直接执行 SOP
# （非 --due-only）时，外层 timeout 会先于调度层把本轮杀掉，同样会遗留认领锁。
TIMEOUT_S="${ISSUE_AUTOFIX_TIMEOUT:-14400}"

DUE_ONLY=0
for arg in "$@"; do
  case "$arg" in
  --due-only) DUE_ONLY=1 ;;
  -h | --help)
    sed -n '2,30p' "$0"
    exit 0
    ;;
  *)
    echo "未知参数：$arg" >&2
    exit 2
    ;;
  esac
done

mkdir -p "$LOG_DIR"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
LOG="$LOG_DIR/round-$STAMP.log"
EVENTS="$LOG_DIR/round-$STAMP.jsonl"
info() { echo "[issue-autofix round $(date -u +%H:%M:%SZ)] $*" | tee -a "$LOG"; }

info "启动：provider=$PROVIDER model=$MODEL timeout=${TIMEOUT_S}s log=$LOG"

# 依赖探测：把「找不到命令」变成可读原因，而不是让它变成一个神秘的 127。
# （实测教训：cron 轨道首次触发就是因为 pi 不在 PATH 而静默失败。）
PI_BIN=$(command -v pi 2>/dev/null || true)
if [ -z "$PI_BIN" ]; then
  info "FATAL: 找不到 pi 可执行文件（已尝试 PATH 补全：$PI_NODE_BIN）。"
  info "       请在 crontab 显式设置 PATH，或导出 PI_NODE_BIN 指向包含 pi 的 bin 目录。"
  exit 127
fi
info "使用 pi：$PI_BIN"
for dep in timeout python3; do
  if ! command -v "$dep" >/dev/null 2>&1; then
    info "FATAL: 缺少依赖命令 $dep"
    exit 1
  fi
done

if [ ! -f "$SOP" ]; then
  info "FATAL: SOP 缺失：$SOP（定时任务无规则来源，拒绝空跑）"
  exit 1
fi

if [ "$DUE_ONLY" = "1" ]; then
  PROMPT='Call the subagent tool exactly once with the single argument {"action":"schedule.run-due"}.
Then report the raw tool result verbatim, and stop. Do not do anything else.'
else
  # prompt 只做一件事：读 SOP 并执行。所有判定规则都在版本化管理的手册里，
  # 这样改规则 = 改文档 + 提交（可 review / 可回滚），而不是改一段看不见的调度配置。
  PROMPT="你被 crontab 拉活，需要执行一轮 issue-autofix 守护任务。

严格执行以下手册（唯一权威规则来源，必须完整读取后再动手）：

    $SOP

如果该文件不存在，立即停止并报告缺失。不要凭记忆执行规则。"
fi

CMD=("$PI_BIN" -p --no-session --mode json
  --provider "$PROVIDER" --model "$MODEL"
  --append-system-prompt "本轮是无交互定时任务：不要请求确认，不要提问。按 SOP 执行到底，末尾按 SOP §10 输出轮次总结。"
  "$PROMPT")

if [ -n "${ISSUE_AUTOFIX_DRY_RUN:-}" ]; then
  printf 'DRY_RUN:'
  printf ' %q' "${CMD[@]}"
  echo
  exit 0
fi

cd "$REPO_ROOT" || {
  info "FATAL: 无法进入 $REPO_ROOT"
  exit 1
}

set +e
timeout "$TIMEOUT_S" "${CMD[@]}" >"$EVENTS" 2>>"$LOG"
rc=$?
set -e

if [ "$rc" -eq 124 ]; then
  info "FATAL: 超时（${TIMEOUT_S}s）被杀死 —— 本轮未完成，检查残留认领"
  exit 124
fi
if [ "$rc" -ne 0 ]; then
  info "FATAL: pi 退出码 $rc（详见 $LOG）"
  exit "$rc"
fi

# 从 JSON 事件流中抽出最终 assistant 文本。注意 --mode json 下 assistant 内容
# 只在流事件的 message 里，**stdout 不是纯文本**，因此不能直接把 stdout 当报告。
SUMMARY=$(
  python3 - "$EVENTS" <<'PY'
import json, sys
texts = []
try:
    with open(sys.argv[1], encoding='utf-8') as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get('type') != 'agent_end':
                continue
            for msg in ev.get('messages', []):
                if msg.get('role') != 'assistant':
                    continue
                for block in msg.get('content') or []:
                    if isinstance(block, dict) and block.get('type') == 'text':
                        t = (block.get('text') or '').strip()
                        if t:
                            texts.append(t)
except FileNotFoundError:
    pass
print("\n".join(texts))
PY
)

if [ -z "$SUMMARY" ]; then
  # 这就是本脚本要防的核心失败：退出码 0 但模型其实什么都没说。
  info "FATAL: pi 退出码 0 但没有任何 assistant 输出 —— 判定为本轮静默失败（常见原因：provider 拒绝/额度/网络）"
  tail -c 1500 "$LOG" >&2 || true
  exit 1
fi

{
  echo "----- 轮次总结 -----"
  echo "$SUMMARY"
} | tee -a "$LOG"

if printf '%s' "$SUMMARY" | grep -q 'probe: *DEGRADED'; then
  info "结果：环境降级，本轮空转（未产生任何 issue 评论）"
  exit 10
fi

info "结果：轮次完成"
exit 0
