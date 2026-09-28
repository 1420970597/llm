#!/usr/bin/env bash
#
# issue-autofix 前置探针、认领锁与轮次台账（唯一权威实现）
#
# 为什么需要它：定时任务在无人值守下运行。如果直接在「不可信环境」里开工，会产生
# 假证据 —— 例如容器没起来导致 UI 复现失败，Agent 会误判「缺陷仍存在」并写出
# 错误的 issue 评论。本脚本把「环境是否可信」「本轮该做哪条 issue」「证据是否合格」
# 做成**可判定的机器事实**，而不是让模型自由心证。
#
# 反污染（对齐本仓库既有约定 test/l15_issue_triage.py §6.2）：
#   - 只读写 .pi/issue-autofix/ 下的台账（.pi/ 已在 .gitignore:10），不污染工作树
#   - 台账 append-only，不改写历史轮次（可审计「第几轮做了什么」）
#
# 运行环境约束（AGENTS.md §1）：宿主机没有 Go / Node 工具链。
#   本脚本只使用 gh / curl / docker / python3 / git / flock —— 不调用 go / npm / npx。
#   真正的 Go 与前端门禁一律通过容器与用户级 node 执行（见 SOP 文档）。
#
# 用法：
#   scripts/issue-bot/preflight.sh probe                   # 环境前置探针（退出码见下）
#   scripts/issue-bot/preflight.sh ensure-labels           # 幂等创建标签
#   scripts/issue-bot/preflight.sh scan                    # 列出本轮可认领的 issue
#   scripts/issue-bot/preflight.sh claim <n>               # 认领（互斥锁 + 打标签）
#   scripts/issue-bot/preflight.sh release <n> [--blocked] # 释放认领（必做）
#   scripts/issue-bot/preflight.sh next-round <n>          # 该 issue 本轮轮次号
#   scripts/issue-bot/preflight.sh budget <n>              # 轮次预算是否还允许继续
#   scripts/issue-bot/preflight.sh ledger-add <n> <round> <result> <branch> <pr> [note]
#   scripts/issue-bot/preflight.sh ledger-show [n]         # 查看台账（按 issue 过滤）
#   scripts/issue-bot/preflight.sh evidence-check <before> <after>
#   scripts/issue-bot/preflight.sh raw-url <ref> <repo-relative-path>
#   scripts/issue-bot/preflight.sh ledger-path
#
# probe 退出码约定：
#   0  = 环境可信，可以开工
#   10 = 环境降级（基础栈不可用等），本轮必须跳过，不产生任何 issue 评论
#   1  = 配置性错误（gh 未登录、不是 git 仓库、go-gate.sh 缺失），需要人工介入
#
# 其他退出码：
#   claim   0=成功 2=已被认领/被禁止 3=轮次预算已耗尽（应升级人工）
#   budget  0=可继续 3=已达 MAX_ROUNDS 上限
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/root/llm}"
WORK_DIR="${ISSUE_AUTOFIX_DIR:-$REPO_ROOT/.pi/issue-autofix}"
LEDGER="$WORK_DIR/ledger.jsonl"
LOCK="$WORK_DIR/.lock"
MAX_PER_ROUND="${ISSUE_AUTOFIX_MAX_PER_ROUND:-3}"
MAX_ROUNDS="${ISSUE_AUTOFIX_MAX_ROUNDS:-3}"

# 证据截图的最小字节数。低于此值几乎一定是空白页/加载失败的占位图，
# 不能作为「修复前/修复后」证据（防「用一张白图冒充已验证」）。
MIN_SHOT_BYTES="${ISSUE_AUTOFIX_MIN_SHOT_BYTES:-5120}"

# 陈旧认领阈值（小时）。**为什么必须有这个值**：任何硬杀（调度超时 / OOM /
# 机器重启 / Ctrl-C）都会把 autofix-running 永久留在 issue 上，使其再也不被任何
# 一轮处理。实测已发生：20:23 轮次被 30min 默认超时杀死后，#191/#160/#214
# 三条 issue 全部被锁死。
#
# 不变式：STALE_CLAIM_HOURS 必须 **严格大于** 调度侧 timeoutMs，否则会把一个
# 仍在正常运行的认领误判为陈旧并与之并发处理。当前取值：6h > 4h(调度超时)。
STALE_CLAIM_HOURS="${ISSUE_AUTOFIX_STALE_CLAIM_HOURS:-6}"

LABEL_AUTO="autofix-auto"
LABEL_RUN="autofix-running"
LABEL_PAUSE="autofix-paused"
LABEL_BLOCK="autofix-blocked"

# 跨进程互斥：即使两个调度进程同时醒来（例如 cron 拉活与进程内定时器撞车），
# 也只有一个能进入认领临界区。
lock_run() {
  mkdir -p "$WORK_DIR"
  if command -v flock >/dev/null 2>&1; then
    exec flock -w 30 "$LOCK" "$@"
  fi
  echo "[issue-autofix] WARN: flock 缺失，未加互斥锁（并发时可能重复认领）" >&2
  "$@"
}

now_iso() { date -u +%Y-%m-%dT%H:%M:%SZ; }
die() { echo "FATAL: $*" >&2; exit 1; }
degraded() { echo "DEGRADED: $*" >&2; exit 10; }
info() { echo "[issue-autofix] $*" >&2; }

# 返回 autofix-running 标签**最后一次被添加**至今的秒数。
# 用 GitHub timeline API 而非标签本身：标签列表不带时间，timeline 才是权威时间源。
# 无法判定时输出空串（调用方必须按「保守保留」处理，不得当作 0）。
_label_added_seconds_ago() {
  local n="$1" ts
  ts=$(gh api "repos/{owner}/{repo}/issues/$n/timeline?per_page=100" \
        --jq "[.[] | select(.event==\"labeled\" and .label.name==\"$LABEL_RUN\")] | last | .created_at // empty" \
        2>/dev/null)
  [ -z "$ts" ] && { echo ""; return 0; }
  python3 - "$ts" <<'PY'
import datetime, sys
raw = sys.argv[1].replace('Z', '+00:00')
added = datetime.datetime.fromisoformat(raw)
if added.tzinfo is None:
    added = added.replace(tzinfo=datetime.timezone.utc)
now = datetime.datetime.now(datetime.timezone.utc)
print(max(0, int((now - added).total_seconds())))
PY
}

# 陈旧认领回收：把被硬杀而残留的 autofix-running 摘掉。
# 返回 0 = 已回收（或本就无锁）；1 = 锁仍然有效（保留）。
# 保守原则：时间戳取不到 / 刚被加上，一律保留 —— 宁可少回收，不可与在跑的任务抢同一条 issue。
_reclaim_stale_claim() {
  local n="$1" age limit
  limit=$((STALE_CLAIM_HOURS * 3600))
  age=$(_label_added_seconds_ago "$n")
  if [ -z "$age" ]; then
    info "#$n 带 $LABEL_RUN 但无法取得加锁时间，保守保留（需人工确认）"
    return 1
  fi
  if [ "$age" -lt "$limit" ]; then
    info "#$n 正被 ${age}s 前的认领占用（阈值 ${limit}s），跳过"
    return 1
  fi
  info "#$n 的 $LABEL_RUN 已陈旧 $((age / 3600))h（阈值 ${STALE_CLAIM_HOURS}h）—— 判定为被硬杀遗留，回收"
  gh issue edit "$n" --remove-label "$LABEL_RUN" >/dev/null 2>&1 || true
  local after
  after=$(gh issue view "$n" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null)
  case ",$after," in
    *",$LABEL_RUN,"*) info "#$n 陈旧锁回收失败（仍带标签），跳过"; return 1 ;;
  esac
  info "#$n 陈旧锁已回收（复核通过）"
  return 0
}
degraded() { echo "DEGRADED: $*" >&2; exit 10; }
info() { echo "[issue-autofix] $*"; }

# ---------------------------------------------------------------- probe ----

cmd_probe() {
  local bad=""

  cd "$REPO_ROOT" 2>/dev/null || die "REPO_ROOT 不存在：$REPO_ROOT"
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "不是 git 仓库：$REPO_ROOT"

  echo "== 配置层 =="
  if gh auth status >/dev/null 2>&1; then
    echo "  ✓ gh 已登录"
  else
    die "gh 未登录（定时任务无法取 issue / 评论）"
  fi
  local repo
  repo=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) \
    || die "无法解析 gh 仓库（检查 remote 与网络）"
  echo "  ✓ repo = $repo"

  # 门禁脚本是「按输出判定」的唯一入口（gofmt -l 列文件时仍 exit 0，见 #94 / PR #151）。
  # 它缺失时 SOP 的验证一节无法执行，属于配置性问题，必须人工介入。
  if [ -x "$REPO_ROOT/scripts/go-gate.sh" ]; then
    echo "  ✓ scripts/go-gate.sh 可执行"
  else
    die "scripts/go-gate.sh 缺失或不可执行（后端门禁无权威入口）"
  fi

  # 源码工作树必须干净：脏树会让「修复前的基线证据」不可信，
  # 也会让后续 git worktree / 分支切换失败。
  if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
    degraded "源码工作树不干净，拒绝在脏基线上取证（先清理再等下一轮）"
  fi
  echo "  ✓ 工作树干净"

  if git ls-remote --exit-code origin HEAD >/dev/null 2>&1; then
    echo "  ✓ origin 可达"
  else
    degraded "origin 不可达（无法 fetch 最新 issue / main）"
  fi

  echo "== 运行层（真实栈）=="
  # issue 的判定标准是「在真实栈上用真实 Chromium 复现」，
  # 因此基础栈必须健康，否则本轮证不出任何东西。
  local svc missing=""
  for svc in llm-api-1 llm-worker-1 llm-web-user-1 llm-postgres-1 llm-redis-1 llm-minio-1; do
    case "$(docker inspect -f '{{.State.Running}}' "$svc" 2>/dev/null)" in
      true) echo "  ✓ $svc running" ;;
      *)    echo "  ✗ $svc 未运行"; missing="$missing $svc" ;;
    esac
  done
  [ -n "$missing" ] && bad="容器未运行:$missing"

  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://127.0.0.1:8080/healthz 2>/dev/null || echo 000)
  [ "$code" = "200" ] && echo "  ✓ api /healthz 200" || { echo "  ✗ api /healthz=$code"; bad="$bad api:$code"; }

  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://127.0.0.1:3210/ 2>/dev/null || echo 000)
  [ "$code" = "200" ] && echo "  ✓ web 200" || { echo "  ✗ web=$code"; bad="$bad web:$code"; }

  # 评论要求「图文并茂」，截图能力缺失时应降级而不是产出纯文字评论。
  if [ -d /root/.pi/agent/npm/node_modules/playwright ]; then
    echo "  ✓ playwright 可用"
  else
    echo "  ✗ playwright 缺失"; bad="$bad playwright"
  fi

  # 前端门禁（tsc + vite）走用户级 node/npm，宿主机不装 Node（AGENTS.md §1）。
  if command -v npm >/dev/null 2>&1; then
    echo "  ✓ npm 可用（$(command -v npm)）"
  else
    echo "  ✗ npm 不可用"; bad="$bad npm"
  fi

  # 图片要能被 GitHub 渲染，必须提交进仓库并走 raw 链接（既有约定：docs/audit/）。
  # 注意：`git ls-tree <dir>` 对**不存在的目录也返回 0 且无输出**，直接用退出码
  # 判定是空判（vacuous）——必须判「输出非空」。
  if [ -n "$(git ls-tree origin/main --name-only docs/audit/ 2>/dev/null)" ]; then
    echo "  ✓ docs/audit 已跟踪（评论区贴图路径可用）"
  else
    echo "  ✗ docs/audit 未跟踪"; bad="$bad docs-audit"
  fi

  if [ -n "$bad" ]; then degraded "环境不可信：$bad"; fi

  echo "== 状态 =="
  info "台账：$LEDGER"
  info "本轮最大认领数：$MAX_PER_ROUND；单 issue 最大迭代轮数：$MAX_ROUNDS"
  echo "PROBE OK"
  return 0
}

# ----------------------------------------------------------- labels --------

cmd_ensure_labels() {
  cd "$REPO_ROOT" || die "无法进入仓库"
  local spec
  for spec in \
    "$LABEL_AUTO|自动修复守护接管|5319e7" \
    "$LABEL_RUN|自动修复进行中（勿手工并发）|fbca04" \
    "$LABEL_PAUSE|禁止自动修复|d73a4a" \
    "$LABEL_BLOCK|自动修复已升级待人工|b60205"
  do
    local name=${spec%%|*}; rest=${spec#*|}
    local desc=${rest%%|*}; color=${rest##*|}
    if gh label create "$name" --description "$desc" --color "$color" --force >/dev/null 2>&1; then
      echo "  ✓ label $name"
    else
      echo "  · label $name 已存在"
    fi
  done
}

# ------------------------------------------------------------- scan --------

# 输出 TSV：number \t round \t title
# 排序意图：优先「上一轮未收口」的 issue（迭代优先），其次按最后更新升序（先老后新）。
cmd_scan() {
  cd "$REPO_ROOT" || die "无法进入仓库"

  local tmp
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' RETURN
  # 只取判定需要的字段：不拉 comments（正文可能极大且此处不使用）。
  if ! gh issue list --state open --limit 100 \
    --json number,title,updatedAt,labels >"$tmp" 2>/dev/null; then
    die "拉取 issue 列表失败"
  fi

  # 用文件传参而不是 heredoc + herestring：两者都抢 stdin，shellcheck SC2261。
  python3 - "$LEDGER" "$MAX_PER_ROUND" "$MAX_ROUNDS" \
           "$LABEL_PAUSE" "$LABEL_RUN" "$LABEL_BLOCK" "$tmp" <<'PY'
import json, sys, os
ledger_path, max_per_round, max_rounds, L_PAUSE, L_RUN, L_BLOCK, issues_path = sys.argv[1:8]
max_per_round = int(max_per_round)
max_rounds = int(max_rounds)

# 台账决定「这个 issue 走到第几轮 / 是否已升级人工」
rounds, blocked = {}, {}
if os.path.exists(ledger_path):
    with open(ledger_path, encoding='utf-8') as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            n = rec.get('issue')
            if n is None:
                continue
            rounds[n] = max(rounds.get(n, 0), int(rec.get('round', 0)))
            if rec.get('result') == 'blocked':
                blocked[n] = True
            elif rec.get('result') in ('fixed', 'closed'):
                blocked[n] = False

with open(issues_path, encoding='utf-8') as fh:
    issues = json.load(fh)
rows, exhausted = [], []
for it in issues:
    labels = {l['name'] for l in it.get('labels', [])}
    if L_PAUSE in labels:      # 人工刹车
        continue
    if L_RUN in labels:        # 已被认领（并发保护）
        continue
    if blocked.get(it['number']):  # 已升级人工，本轮不再自动重试
        continue
    rnd = rounds.get(it['number'], 0)
    # 轮次预算：已达上限的 issue 不再自动重试，必须升级人工。
    # （上一版设计把 MAX_ROUNDS 只写在文档里、代码从不执行 —— 等于没有上限。）
    if rnd >= max_rounds:
        exhausted.append((it['number'], rnd))
        continue
    rows.append((it['number'], rnd, it['title'], it['updatedAt']))

# 迭代优先：有历史轮次的排前面；同类内按 updatedAt 升序
rows.sort(key=lambda r: (0 if r[1] > 0 else 1, r[3]))
for n, rnd, title, _ in rows[:max_per_round]:
    print(f"{n}\t{rnd}\t{title}")

# 轮次耗尽的 issue 走 stderr，供 SOP 判「升级人工」而非静默丢弃
for n, rnd in exhausted:
    print(f"# 轮次耗尽：# {n} 已做 {rnd}/{max_rounds} 轮 → 应升级人工（autofix-blocked）", file=sys.stderr)
PY
}

# ------------------------------------------------------------ claim --------

cmd_claim() {
  local n="${1:?用法: claim <issue-number>}"
  cd "$REPO_ROOT" || die "无法进入仓库"

  # 轮次预算先于认领检查：超限的 issue 不该被再次认领。
  if ! cmd_budget "$n" >/dev/null 2>&1; then
    info "#$n 已达最大迭代轮数 $MAX_ROUNDS，拒绝认领（应升级人工）"; return 3
  fi

  local labels
  labels=$(gh issue view "$n" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null) \
    || die "读取 issue #$n 标签失败"

  case ",$labels," in
    *",$LABEL_PAUSE,"*) info "#$n 已标记禁止自动修复，跳过"; return 2 ;;
    *",$LABEL_RUN,"*)
      # 已被认领：先判是否为硬杀遗留的陈旧锁，是则回收后继续，否则跳过。
      if ! _reclaim_stale_claim "$n"; then
        return 2
      fi
      ;;
  esac

  gh issue edit "$n" --add-label "$LABEL_AUTO" --add-label "$LABEL_RUN" >/dev/null 2>&1 \
    || die "认领 issue #$n 失败"

  # 复核：确认标签真的落下（API 成功 ≠ 状态可见）
  labels=$(gh issue view "$n" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null)
  case ",$labels," in
    *",$LABEL_RUN,"*) echo "CLAIMED $n"; return 0 ;;
    *) die "认领后复核失败：#$n 未见 $LABEL_RUN" ;;
  esac
}

cmd_release() {
  local n="${1:?用法: release <issue-number> [--blocked]}"
  cd "$REPO_ROOT" || die "无法进入仓库"

  # release 必须**验证**标签真的摘掉：残留 autofix-running 会让该 issue
  # 被永久认定为「已被认领」，从而再也不被任何一轮处理 —— 最隐蔽的故障模式。
  # 因此这里不能像早期版本那样「调用成功就报成功」，必须回读复核。
  if gh issue edit "$n" --remove-label "$LABEL_RUN" >/dev/null 2>&1; then
    local labels
    labels=$(gh issue view "$n" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null)
    case ",$labels," in
      *",$LABEL_RUN,"*) echo "FATAL: #$n 仍带 $LABEL_RUN（释放失败，下轮会跳过该 issue）" >&2; return 1 ;;
      *) info "#$n 已释放认领（复核通过）" ;;
    esac
  else
    info "#$n 本无 $LABEL_RUN（无需释放）"
  fi

  if [ "${2:-}" = "--blocked" ]; then
    if gh issue edit "$n" --add-label "$LABEL_BLOCK" >/dev/null 2>&1; then
      info "#$n 已升级人工（$LABEL_BLOCK）"
    else
      echo "FATAL: #$n 打 $LABEL_BLOCK 失败" >&2; return 1
    fi
  fi
}

# ----------------------------------------------------------- ledger --------

# 读取台账，输出每个 issue 的 {max_round, last_result}
_ledger_stats() {
  python3 - "$LEDGER" "$1" <<'PY'
import json, os, sys
path, n = sys.argv[1], int(sys.argv[2])
mx, last = 0, None
if os.path.exists(path):
    with open(path, encoding='utf-8') as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if rec.get('issue') == n:
                mx = max(mx, int(rec.get('round', 0)))
                last = rec.get('result')
print(f"{mx}\t{last or ''}")
PY
}

cmd_next_round() {
  local n="${1:?用法: next-round <issue-number>}"
  local stats max
  stats=$(_ledger_stats "$n") || die "读取台账失败"
  max=${stats%%$'\t'*}
  echo $((max + 1))
}

# 轮次预算门禁：0 = 还能继续；3 = 已达 MAX_ROUNDS，必须升级人工。
cmd_budget() {
  local n="${1:?用法: budget <issue-number>}"
  local stats max last
  stats=$(_ledger_stats "$n") || die "读取台账失败"
  max=${stats%%$'\t'*}
  last=${stats#*$'\t'}
  if [ "$max" -ge "$MAX_ROUNDS" ] && [ "$last" != "fixed" ]; then
    echo "EXHAUSTED $n rounds=$max/$MAX_ROUNDS last=${last:-none}"
    return 3
  fi
  echo "OK $n rounds=$max/$MAX_ROUNDS"
  return 0
}

cmd_ledger_add() {
  local n="${1:?}" round="${2:?}" result="${3:?}" branch="${4:-}" pr="${5:-}" note="${6:-}"
  case "$result" in
    fixed|partial|blocked|closed) ;;
    *) die "result 必须是 fixed|partial|blocked|closed，收到：$result" ;;
  esac
  case "$round" in
    ''|*[!0-9]*) die "round 必须是正整数，收到：$round" ;;
  esac
  python3 - "$LEDGER" "$n" "$round" "$result" "$branch" "$pr" "$note" <<'PY'
import json, os, sys, datetime
path = sys.argv[1]
rec = {
    "ts": datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
    "issue": int(sys.argv[2]),
    "round": int(sys.argv[3]),
    "result": sys.argv[4],          # fixed | partial | blocked | closed
    "branch": sys.argv[5] or None,
    "pr": sys.argv[6] or None,
    "note": sys.argv[7] or None,
}
os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, 'a', encoding='utf-8') as fh:
    fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
print(json.dumps(rec, ensure_ascii=False))
PY
}

cmd_ledger_show() {
  local n="${1:-}"
  if [ ! -f "$LEDGER" ]; then echo "（台账为空：$LEDGER）"; return 0; fi
  # 按 issue 过滤走 python 解析，而不是 grep 文本匹配：
  # 后者依赖 json.dumps 的 `"issue": 191,` 精确间距，格式一变就静默漏报。
  if [ -n "$n" ]; then
    python3 - "$LEDGER" "$n" <<'PY'
import json, sys
path, n = sys.argv[1], int(sys.argv[2])
found = False
with open(path, encoding='utf-8') as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if rec.get('issue') == n:
            print(json.dumps(rec, ensure_ascii=False))
            found = True
if not found:
    print(f"（#{n} 无记录）")
PY
  else
    cat "$LEDGER"
  fi
}

# --------------------------------------------------------- evidence --------

# 「图文并茂」的机器门禁：截图必须是真实、非空、且前后确实不同的两张图。
# 防的是「用文字描述替代截图」与「用同一张图/白图冒充修复前后对比」。
cmd_evidence_check() {
  local before="${1:?用法: evidence-check <before.png> <after.png>}"
  local after="${2:?用法: evidence-check <before.png> <after.png>}"
  local rc=0

  local f
  for f in "$before" "$after"; do
    if [ ! -f "$f" ]; then
      echo "MISSING: $f 不存在（不许用文字描述替代截图）" >&2; rc=1; continue
    fi
    local size
    size=$(wc -c <"$f" | tr -d ' ')
    if [ "$size" -lt "$MIN_SHOT_BYTES" ]; then
      echo "TOO_SMALL: $f 仅 ${size}B < ${MIN_SHOT_BYTES}B（疑似空白/加载失败图）" >&2; rc=1
    else
      echo "  ✓ $(basename "$f") ${size}B"
    fi
  done
  [ "$rc" -ne 0 ] && { echo "EVIDENCE REJECTED" >&2; return 1; }

  local bsum asum
  bsum=$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$before")
  asum=$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$after")
  if [ "$bsum" = "$asum" ]; then
    echo "IDENTICAL: 前后截图完全一致（sha256 ${bsum:0:12}）—— 无法证明缺陷发生变化" >&2
    echo "EVIDENCE REJECTED" >&2
    return 1
  fi

  echo "EVIDENCE OK"
  return 0
}

# 生成 raw.githubusercontent 固定 SHA 链接，避免手工拼 URL 出错。
cmd_raw_url() {
  local ref="${1:?用法: raw-url <ref> <repo-relative-path>}"
  local rel="${2:?用法: raw-url <ref> <repo-relative-path>}"
  cd "$REPO_ROOT" || die "无法进入仓库"
  local sha owner repo
  # 必须解析成完整 40 位 SHA：用分支名做链接会在后续提交后内容漂移。
  sha=$(git rev-parse --verify --quiet "${ref}^{commit}") || die "无法解析 ref：$ref"
  owner=$(gh repo view --json owner -q .owner.login 2>/dev/null) || die "无法解析 owner"
  repo=$(gh repo view --json name -q .name 2>/dev/null) || die "无法解析 repo 名"
  echo "https://raw.githubusercontent.com/${owner}/${repo}/${sha}/${rel}"
}

# ----------------------------------------------------- reclaim-stale --------

# 扫描所有带 autofix-running 的 issue，回收陈旧锁。
#
# **为什么必需**：`scan` 会把带 autofix-running 的 issue 直接从候选里过滤掉，
# 因此陈旧锁永远走不到 `claim` 里的回收分支 —— 只把回收挂在 claim 上是一条
# 不可达的路径。实测教训：20:23 轮次被超时杀死后，3 条 issue 的锁在「下一轮」
# 依然存在，且 `scan` 根本不会列出它们，永远无人回收。
# 因此回收必须是一个**独立于 scan 的前置步骤**。
cmd_reclaim_stale() {
  cd "$REPO_ROOT" || die "无法进入仓库"
  local nums
  nums=$(gh issue list --state open --limit 100 --label "$LABEL_RUN" \
           --json number -q '.[].number' 2>/dev/null) \
    || die "拉取 $LABEL_RUN issue 列表失败"
  if [ -z "$nums" ]; then
    info "无带 $LABEL_RUN 的 issue，无需回收"
    echo "RECLAIMED 0"
    return 0
  fi

  local reclaimed=0 kept=0 n
  for n in $nums; do
    if _reclaim_stale_claim "$n"; then reclaimed=$((reclaimed + 1)); else kept=$((kept + 1)); fi
  done
  info "陈旧锁回收完成：回收 $reclaimed，保留 $kept"
  echo "RECLAIMED $reclaimed"
}

# ------------------------------------------------------------ main ---------

case "${1:-help}" in
  probe)          lock_run "$0" __probe ;;
  __probe)        cmd_probe ;;
  ensure-labels)  cmd_ensure_labels ;;
  scan)           cmd_scan ;;
  reclaim-stale)  lock_run "$0" __reclaim_stale ;;
  __reclaim_stale) cmd_reclaim_stale ;;
  claim)          shift; lock_run "$0" __claim "$@" ;;
  __claim)        shift; cmd_claim "$@" ;;
  release)        shift; lock_run "$0" __release "$@" ;;
  __release)      shift; cmd_release "$@" ;;
  next-round)     shift; cmd_next_round "$@" ;;
  budget)         shift; cmd_budget "$@" ;;
  ledger-add)     shift; lock_run "$0" __ledger_add "$@" ;;
  __ledger_add)   shift; cmd_ledger_add "$@" ;;
  ledger-show)    shift; cmd_ledger_show "$@" ;;
  evidence-check) shift; cmd_evidence_check "$@" ;;
  raw-url)        shift; cmd_raw_url "$@" ;;
  ledger-path)    echo "$LEDGER" ;;
  *)
    sed -n '2,45p' "$0"
    ;;
esac
