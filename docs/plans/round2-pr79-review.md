# 父代理对 PR #79 (R1) 的合并前审查结论

结论：**CHANGES-REQUIRED**（父代理直接判定，不走子代理复审）。

PR #79 的核心修复是**正确的**（`stageRouteNavMap` 改为从阶段工作台声明的 `navParent` 派生，
单一来源消除三处漂移），`npm run build` 与 `go test ./test/...` 均通过，且**未引入任何新依赖**
（`package.json` / `package-lock.json` 相对 `origin/main` 无 diff，共享 `node_modules` 未被污染）。

但父代理用**变异测试**发现守卫存在真实弱点，必须修掉才能合并。

## 变异验证的实测结果（可复现）

### 变异 1：改错 navParent 指向 → `go test ./test/...` **未捕获**

```bash
cd /root/llm   # 在 mr/79 (R1 rebased) 上
python3 - <<'PY'
p='apps/web-user/src/App.tsx'
s=open(p,encoding='utf-8').read()
old="  { label: '主题结构', route: '/console/domains', icon: GitBranch, caption: '生成并确认主题结构', navParent: '/console/tasks' },"
new="  { label: '主题结构', route: '/console/domains', icon: GitBranch, caption: '生成并确认主题结构', navParent: '/console/results' },"
assert old in s
open(p,'w',encoding='utf-8').write(s.replace(old,new))
PY
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "go test ./test/..."
# 结果：ok  github.com/1420970597/llm/test  0.029s      <-- 没捕获
```

原因：`test/frontend_routes_test.go` 的 `TestStageWorkbenchPagesMustDeclareNavParent`
只断言 `navParent` **指向 userPages 里真实存在的路由**（`/console/tasks` 与 `/console/results`
都是合法侧边栏项），因此把 `/console/domains` 的归属错写成 `/console/results` 仍然合法。
它拦得住「缺 navParent」与「指向不存在的路由」，拦不住「指向**错误**的路由」——
而后者正是 #61 的漂移形态本身。

### 变异 2：把派生退回手写常量表 → `.mjs` 能捕获，但 CI 跑不到

`test/l15_stage_routes.mjs` 的 `problemsWithNavDerivation` 与
「阶段声明的 route→navParent/label 与契约冻结值一一对应」这两条断言**确实**能捕获它，
但整脚本在到达源码级断言之前就被**真实 API 登录**卡住：

```bash
node test/l15_stage_routes.mjs
# [FAIL] 真实登录本 lane 的 API: http://127.0.0.1:18101/api/v1 不可达或被拒：fetch failed（先跑 scripts/l15-r1-stack.sh）
# STAGE ROUTES FAILED: 1/1 项未通过 -> 真实登录本 lane 的 API
```

CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
**都不会**起 `18101` 的候选容器，因此这个 `.mjs` 脚本在 CI 里 100% 失败、
实际上等价于「有一个永远跑不起来的守卫」。

## 必须修复的两点

1. **把纯源码级断言与需要真实 API 的断言分离。**
   源码级断言（含变异自证）必须在**无任何容器**的情况下可运行并 exit 0，
   这样 CI 才能真正执行它。需要真实 API 的部分作为**可选**阶段：
   默认跳过并在输出里明确写「已跳过（需 --with-api 且先跑 scripts/l15-r1-stack.sh）」，
   绝不把「环境不可达」表现为「断言失败」——那会让 CI 永久红/绿失真。
2. **补「navParent 取值正确性」断言。**
   把 5 个阶段路由各自的期望归属冻结在测试里（`/console/domains → /console/tasks`，
   `/console/questions|reasoning|rewards|exports → /console/results`），
   对**源码文本**求值断言取值一致。这样变异 1 才会失败。
   Go 侧同理：`test/frontend_routes_test.go` 增加取值正确性断言，
   或明确把这部分交给 `.mjs`（但 `.mjs` 必须能在 CI 跑，见第 1 点）。

## 为什么这是父代理的职责

契约 §2 明确「父代理是唯一合并者」；§3 要求「禁止伪造测试通过」。
一个在 CI 里永远失败、且拦不住真正漂移的守卫，等于没有守卫。
父代理用变异测试证明它拦不住，就必须在合并前修掉，而不是把「看起来有测试」当成通过。

## 修复后必须复核

```bash
# 1) 无容器时必须通过（CI 等价）
node test/l15_stage_routes.mjs      # 期望 exit 0，且源码级断言全 PASS
# 2) 变异 1 必须被捕获
（改错一个 navParent → 期望上面命令 exit != 0）
# 3) 变异 2 必须被捕获
（把派生退回手写表 → 期望 exit != 0）
# 4) 变异 3（#61 本体）必须被捕获
（把某阶段路由退回 <Navigate> → 期望 exit != 0）
# 5) 其余门禁不变
npm run build -w apps/web-user
docker run --rm -v $PWD:/w -w /w golang:1.24-alpine sh -c "go test ./test/..."
```
