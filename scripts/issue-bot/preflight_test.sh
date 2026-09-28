#!/usr/bin/env bash
#
# preflight.sh / round.sh 自测（契约回归）
#
# 为什么必须有：本脚本是无人值守守护的**唯一判定入口**，它的行为退化会比缺功能更危险
# ——例如「认领锁失效」会让两个进程同时改同一条 issue，产出互相矛盾的评论；
# 「证据门禁失效」会让白图通过，评论里的「图文并茂」变成假证据。
# 因此这里对**不变式**做回归，而不是对实现细节做快照。
#
# 覆盖：正常路径 + 边界/异常路径（对齐 AGENTS.md §4.3 的自测闭环要求）
#
# 用法：bash scripts/issue-bot/preflight_test.sh
# 特点：**完全隔离** —— 用临时 ISSUE_AUTOFIX_DIR，不碰真实台账；
#       只测只读/纯本地命令，不发任何 GitHub 写请求。
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
PREFLIGHT="$HERE/preflight.sh"
ROUND="$HERE/round.sh"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "  ✗ $1"; }
check() { # check <描述> <期望> <实际>
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1（期望 [$2] 实际 [$3]）"; fi
}

export TMPROOT
TMPROOT=$(mktemp -d)
trap 'rm -rf "$TMPROOT"' EXIT
export ISSUE_AUTOFIX_DIR="$TMPROOT/autofix"

echo "== 0. 脚本自身健全性 =="
# 用显式 if/else 而非 `A && B || C`：后者在 B 失败时会误跑 C（shellcheck SC2015）。
if bash -n "$PREFLIGHT"; then ok "preflight.sh 语法合法"; else bad "preflight.sh 语法非法"; fi
if bash -n "$ROUND"; then ok "round.sh 语法合法"; else bad "round.sh 语法非法"; fi
if [ -x "$PREFLIGHT" ]; then ok "preflight.sh 可执行位已设置"; else bad "preflight.sh 缺少可执行位"; fi
if [ -x "$ROUND" ]; then ok "round.sh 可执行位已设置"; else bad "round.sh 缺少可执行位"; fi

echo "== 1. 台账初始为空 =="
check "空台账 next-round 从 1 开始" "1" "$("$PREFLIGHT" next-round 999)"
check "空台账 ledger-show 不报错" "0" "$("$PREFLIGHT" ledger-show 999 >/dev/null 2>&1; echo $?)"
check "空台账 budget 允许继续" "0" "$("$PREFLIGHT" budget 999 >/dev/null 2>&1; echo $?)"

echo "== 2. 正常路径：轮次递进 =="
"$PREFLIGHT" ledger-add 999 1 partial "fix/TASK-999-x" "" "第一轮" >/dev/null
check "写入 round 1 后 next-round = 2" "2" "$("$PREFLIGHT" next-round 999)"
"$PREFLIGHT" ledger-add 999 2 partial "fix/TASK-999-x" "" "第二轮" >/dev/null
check "写入 round 2 后 next-round = 3" "3" "$("$PREFLIGHT" next-round 999)"

echo "== 3. 边界：轮次取最大值（乱序写入不回退）=="
"$PREFLIGHT" ledger-add 999 1 partial "b" "" "乱序回写更小轮次" >/dev/null
check "乱序写入小轮次不回退" "3" "$("$PREFLIGHT" next-round 999)"

echo "== 4. 边界：issue 之间互相隔离 =="
check "另一 issue 仍从 1 开始" "1" "$("$PREFLIGHT" next-round 888)"

echo "== 5. 边界：台账损坏行必须被跳过而不是崩溃 =="
printf 'not json at all\n{"issue": 777, "round": 4, "result": "partial"}\n\n' >> "$ISSUE_AUTOFIX_DIR/ledger.jsonl"
check "损坏行被跳过且后续可解析" "5" "$("$PREFLIGHT" next-round 777)"
check "损坏台账下其他命令不崩溃" "0" "$("$PREFLIGHT" ledger-show 777 >/dev/null 2>&1; echo $?)"

echo "== 6. 异常路径：缺参数/非法入参必须失败而不是静默成功 =="
"$PREFLIGHT" next-round >/dev/null 2>&1; check "next-round 缺参数退出非 0" "1" "$?"
"$PREFLIGHT" ledger-add 1 >/dev/null 2>&1; check "ledger-add 缺参数退出非 0" "1" "$?"
"$PREFLIGHT" claim >/dev/null 2>&1; check "claim 缺参数退出非 0" "1" "$?"
"$PREFLIGHT" ledger-add 1 1 bogus "b" "" "非法 result" >/dev/null 2>&1
check "ledger-add 拒绝非法 result" "1" "$?"
"$PREFLIGHT" ledger-add 1 abc partial "b" "" "非法 round" >/dev/null 2>&1
check "ledger-add 拒绝非数字 round" "1" "$?"

echo "== 7. 轮次预算上限必须由代码执行（不是只写在文档里）=="
# 关键不变式：达到 MAX_ROUNDS 且上轮非 fixed 时，必须拒绝继续认领。
export ISSUE_AUTOFIX_MAX_ROUNDS=3
"$PREFLIGHT" ledger-add 555 1 partial "b" "" "r1" >/dev/null
"$PREFLIGHT" ledger-add 555 2 partial "b" "" "r2" >/dev/null
check "未达上限时 budget 允许继续" "0" "$("$PREFLIGHT" budget 555 >/dev/null 2>&1; echo $?)"
"$PREFLIGHT" ledger-add 555 3 partial "b" "" "r3" >/dev/null
check "达上限后 budget 返回 3" "3" "$("$PREFLIGHT" budget 555 >/dev/null 2>&1; echo $?)"
check "达上限后输出 EXHAUSTED" "EXHAUSTED" "$("$PREFLIGHT" budget 555 | cut -d' ' -f1)"
# 上轮已 fixed 的 issue 不应被预算拦下（它已收口，不需要再迭代）
"$PREFLIGHT" ledger-add 556 3 fixed "b" "pr" "已收口" >/dev/null
check "上轮 fixed 时预算不拦" "0" "$("$PREFLIGHT" budget 556 >/dev/null 2>&1; echo $?)"
unset ISSUE_AUTOFIX_MAX_ROUNDS

echo "== 8. 边界：ledger-show 按 issue 精确过滤（不靠文本间距匹配）=="
out=$("$PREFLIGHT" ledger-show 555 | wc -l | tr -d ' ')
check "ledger-show 555 命中 3 行" "3" "$out"
# 999 与 9990 这类前缀重叠必须不互相污染
"$PREFLIGHT" ledger-add 9990 1 partial "b" "" "前缀重叠陷阱" >/dev/null
out=$("$PREFLIGHT" ledger-show 999 | wc -l | tr -d ' ')
check "ledger-show 999 不被 9990 污染" "3" "$out"

echo "== 9. 证据门禁：图文并茂必须是机器可判定的 =="
"$PREFLIGHT" evidence-check "$TMPROOT/nope-before.png" "$TMPROOT/nope-after.png" >/dev/null 2>&1
check "缺失截图被拒绝" "1" "$?"
# 造两张「够大但不同」的假 PNG
python3 - "$TMPROOT" <<'PY'
import os, sys
d = sys.argv[1]
open(os.path.join(d, 'a.png'), 'wb').write(b'\x89PNG\r\n\x1a\n' + os.urandom(8192))
open(os.path.join(d, 'b.png'), 'wb').write(b'\x89PNG\r\n\x1a\n' + os.urandom(8192))
PY
check "真实且不同的两张图通过" "0" "$("$PREFLIGHT" evidence-check "$TMPROOT/a.png" "$TMPROOT/b.png" >/dev/null 2>&1; echo $?)"
# 判定行取末行：前面还有逐图的 ✓ 大小行（`$(...)` 会剥掉结尾换行）
check "通过时输出 EVIDENCE OK" "EVIDENCE OK" "$("$PREFLIGHT" evidence-check "$TMPROOT/a.png" "$TMPROOT/b.png" | tail -n1)"
# 同一张图冒充「修复前后」是典型的假证据，必须被拒
check "前后同图被拒绝" "1" "$("$PREFLIGHT" evidence-check "$TMPROOT/a.png" "$TMPROOT/a.png" >/dev/null 2>&1; echo $?)"
# 白图/加载失败的小图必须被拒
printf 'tiny' > "$TMPROOT/small.png"
check "过小截图被拒绝" "1" "$("$PREFLIGHT" evidence-check "$TMPROOT/small.png" "$TMPROOT/b.png" >/dev/null 2>&1; echo $?)"

echo "== 10. raw-url 必须固定 40 位 SHA（分支名会让内容漂移）=="
url=$("$PREFLIGHT" raw-url HEAD docs/audit/README.md 2>/dev/null)
if printf '%s' "$url" | grep -qE '/llm/[0-9a-f]{40}/docs/audit/README\.md$'; then
  ok "raw-url 返回固定 SHA 链接"
else
  bad "raw-url 未返回固定 SHA 链接：$url"
fi
check "非法 ref 必须失败" "1" "$("$PREFLIGHT" raw-url 'no-such-ref-xyz' docs/audit/README.md >/dev/null 2>&1; echo $?)"

echo "== 11. probe 在脏工作树/坏环境下必须降级（不得返回 0）=="
# 关键不变式：环境不可信时**绝不能**返回 0，否则守护会在坏基线上取证并写出错误评论。
# 保存并恢复 errexit 状态：否则 `set -e` 会泄漏到后续小节，
# 让第 12 节「故意返回 3 的 budget」把整个测试提前杀掉（第一版就踩了这个坑）。
case "$-" in *e*) HAD_ERREXIT=1 ;; *) HAD_ERREXIT=0 ;; esac
set +e
"$PREFLIGHT" probe >/dev/null 2>&1
probe_exit=$?
if [ "$HAD_ERREXIT" = "1" ]; then set -e; else set +e; fi
case "$probe_exit" in
  0|10|1) ok "probe 返回受契约约束的退出码（$probe_exit）" ;;
  *) bad "probe 返回了契约外退出码：$probe_exit" ;;
esac
if [ "$probe_exit" = "10" ]; then
  ok "脏工作树被正确判定为降级（拒绝在脏基线取证）"
elif [ "$probe_exit" = "0" ]; then
  echo "  · 工作树干净，跳过脏树断言"
else
  bad "预期降到 10，实际 $probe_exit"
fi

echo "== 12. 只读命令不得产生副作用 =="
before=$(wc -l < "$ISSUE_AUTOFIX_DIR/ledger.jsonl")
# 用无记录的编号：777 的行上轮 round=4 已超预算，budget 会（正确地）返回 3，
# 这里只想验证「只读命令不写台账」，不要混入预算语义。
"$PREFLIGHT" next-round 777 >/dev/null
"$PREFLIGHT" budget 7777 >/dev/null
"$PREFLIGHT" ledger-show >/dev/null
after=$(wc -l < "$ISSUE_AUTOFIX_DIR/ledger.jsonl")
check "只读命令未改动台账行数" "$before" "$after"
# scan 也应只读（无 gh 凭据时它可能失败，但绝不能写台账）
"$PREFLIGHT" scan >/dev/null 2>&1 || true
after2=$(wc -l < "$ISSUE_AUTOFIX_DIR/ledger.jsonl")
check "scan 未改动台账行数" "$before" "$after2"

echo "== 13. round.sh 必须拒绝在无规则来源时空跑 =="
# 关键不变式：SOP 不存在时绝不能「跑到哪算哪」，必须硬失败。
out=$(ISSUE_AUTOFIX_DRY_RUN=1 REPO_ROOT="$TMPROOT/nonexistent-repo" bash "$ROUND" 2>&1; echo "exit=$?")
if printf '%s' "$out" | grep -q 'exit=1'; then
  ok "SOP 缺失时 round.sh 非 0 退出（拒绝无规则空跑）"
else
  bad "SOP 缺失时 round.sh 未硬失败：$out"
fi

echo "== 14. round.sh 参数契约 =="
"$ROUND" --bogus-flag >/dev/null 2>&1; check "未知参数退出 2" "2" "$?"
"$ROUND" --help >/dev/null 2>&1; check "--help 退出 0" "0" "$?"

echo
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
echo "PREFLIGHT SELF-TEST OK"
