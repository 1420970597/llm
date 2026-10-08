#!/usr/bin/env bash
#
# 机器可复现：在办载体 PR 的**合并队列冲突**（issue-autofix 第 3 轮实测发现）。
#
# 为什么要有这个脚本：第 3 轮之前，每一轮都把「待人工合并」写成
# 「唯一剩余动作 = 合并 PR」。但那句话**隐含假设合并可串行完成**。实测不成立：
#   #238 / #240 / #241 都改了 `test/l15_issue197_remediation.mjs` 的同一批数组位置，
#   三者中**任意顺序**合并都会在第三条上得到 `CONFLICT (content)`。
# 即「点一下合并」这个动作本身会卡住 —— 这是必须让人工知道的新事实。
#
# 判定是机器事实（git 退出码 + `--diff-filter=U` 列出的冲突文件），不靠肉眼。
#
# 用法（在仓库根执行）：
#   docs/audit/issue-212-r3/check-merge-queue.sh
#
# 退出码：
#   0 = 冲突已复现（符合当前结论）
#   1 = 竟然干净合并（说明冲突已被消解，应更新本轮结论与评论）
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT" || exit 2

# 本轮三条在办载体：各自单独 MERGEABLE/CLEAN，但**合在一起**不行。
PRS=(238 240 241)

WORK="$(mktemp -d)"
cleanup() { git worktree remove --force "$WORK" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

git fetch -q origin main || { echo "无法 fetch origin/main" >&2; exit 2; }
git worktree add -q --detach "$WORK" origin/main || exit 2
git -C "$WORK" config user.email autofix@local
git -C "$WORK" config user.name issue-autofix

conflicts_seen=0
for order in "238 240 241" "238 241 240" "240 238 241" "240 241 238" "241 238 240" "241 240 238"; do
  git -C "$WORK" reset -q --hard origin/main
  git -C "$WORK" clean -qfd
  failed_at=''
  for pr in $order; do
    sha=$(gh pr view "$pr" --json headRefOid --jq .headRefOid)
    git -C "$WORK" fetch -q origin "$sha"
    if ! git -C "$WORK" merge -q --no-edit FETCH_HEAD >/dev/null 2>&1; then
      failed_at="$pr"
      break
    fi
  done
  if [ -n "$failed_at" ]; then
    files=$(git -C "$WORK" diff --name-only --diff-filter=U | tr '\n' ' ')
    echo "order[$order] -> CONFLICT at #$failed_at ; files=[${files% }]"
    conflicts_seen=1
    git -C "$WORK" merge --abort >/dev/null 2>&1 || true
  else
    echo "order[$order] -> CLEAN"
  fi
done

if [ "$conflicts_seen" -eq 1 ]; then
  echo "结论：三条载体 PR 无法串行干净合并 —— 需先消解 test/l15_issue197_remediation.mjs 的重叠。"
  exit 0
fi
echo "结论：队列已可干净合并（冲突已被消解）—— 请更新本轮结论。" >&2
exit 1
