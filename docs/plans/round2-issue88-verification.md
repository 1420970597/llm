# 父代理复核：issue #88（部署前端落后于源码）

## 结论

**#88 描述的故障在当前已部署系统上不存在** —— 5 个阶段路由全部可达、标题正确、无重定向。
但 #88 揭示的**风险类别**（无法从部署物判断它是否对应 HEAD）是真实的，因此 #88 **不应仅以
「已重建」关闭**，而应要求一个可自证的版本标识。该工作由 R16 lane 承担。

## 复核证据（三层，逐层加重）

### 1. 镜像构建时间 vs #79 提交时间

```text
docker inspect llm-web-user-1 --format '{{.Image}}' | xargs docker image inspect --format '{{.Created}}'
# 2026-09-19T09:40:11Z

git log -1 --format=%cI 33a368d   # fix(web-user): 阶段路由归属… (#79)
# 2026-09-19T15:43:28+08:00  ==  2026-09-19T07:43:28Z
```

镜像构建（09:40Z）**晚于** #79 的提交（07:43Z）→ 镜像已包含该修复。

### 2. 容器资产里不存在 #88 所述的旧重定向映射

```bash
docker exec llm-web-user-1 sh -c \
  'grep -o "/console/domains\":\"/console/tasks\"" /usr/share/nginx/html/assets/index-74Aru6bD.js'
# 无输出
```

> 父代理第一次核对时把**自己 echo 的说明文字**误当命令输出，得出「仍是旧镜像」的错误初判；
> 这里更正：该 grep **没有**任何输出，即旧映射确实不存在。

同资产里确实有 2 处 `/console/tasks`，但都是**正当链接**，不是重定向映射：

```text
…asks",label:"生成数据",detail:"问题、思维链、答案逐步产出",route:"/console/tasks"}   <- 侧边栏导航项
…on"),children:"回到质量评估"}),…onClick:()=>t("/console/tasks"),children:"回到我的任务"  <- 返回任务列表按钮
```

### 3. 真浏览器实测（决定性证据）

用真 Chromium（全局 playwright）登录后逐个访问 5 个阶段路由：

```text
node /tmp/check88.mjs
OK  /console/domains   -> /console/domains   「生成主题结构并完成确认」
OK  /console/questions -> /console/questions 「题目生成结果中心」
OK  /console/reasoning -> /console/reasoning 「答案与思路结果中心」
OK  /console/rewards   -> /console/rewards   「质量评分结果中心」
OK  /console/exports   -> /console/exports   「导出结果中心」
```

**无任何重定向，路径与标题均正确** → #88 所述现象已消失。

## 为什么仍然要留一条 lane（R16）

#88 的真正价值不是「当时坏了」，而是暴露了三件事：

1. **无法从运行中的系统判断它对应哪个 commit。** 页面上没有版本标识、health 端点也不返回构建信息。
   用户按 README 操作时**无法察觉自己在跑旧代码** —— 这正是 #88 的成因（审计者跑了旧容器很久才
   通过资产里的一张映射表反推出来）。
2. **`docker compose up -d --build` 的默认行为需要被验证。** 重建即恢复说明构建链路本身没问题，
   但「什么时候需要重建」依赖人的自觉。
3. **审计成本极高。** 判断「是否落后」需要 `docker image inspect` + `git log` 手工比对，
   而不是一条命令。

因此 R16 lane 的交付标准是「**部署后能自证版本**」（例如把 git SHA 注入镜像并在页面可见），
而不是「已重建，问题消失」。

## 与 issue #88 的关系

#88 可以关闭，但**必须附上本文档作为证据**，并说明防复发手段由 R16 lane 交付。
若 R16 未能在本轮完成，则 #88 应保持开启（因为「无法自证版本」这一根因仍在）。
