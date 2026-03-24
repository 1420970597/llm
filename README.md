# LLM Data Factory

一个面向企业级使用场景的 LLM 长链思考训练数据工厂。

系统支持：
- 用户输入目标数据集规模与关键词
- 生成领域图谱并支持人工审核
- 异步生成问题、长思维链/答案、奖励评估数据
- 将大文本与最终导出工件落到 MinIO / S3 兼容对象存储
- 通过管理员后台配置模型、存储、策略、Prompt 与审计信息

---

## 1. 当前完成度

项目当前已经完成以下能力：

### 用户端
- 数据集计划估算
- 数据集创建
- 领域图谱生成与确认
- 问题生成任务触发与预览
- 推理/答案生成任务触发与预览
- 奖励数据生成任务触发与预览
- 导出任务触发与工件预览
- 运行态统计查看

### 管理端
- 模型提供方管理
- 存储配置管理
- 生成策略管理
- Prompt 模板管理
- 审计日志查看

### 后端与基础设施
- Go API
- Go Worker
- PostgreSQL 元数据存储
- Redis 异步队列
- MinIO / S3 兼容对象存储
- Docker Compose 本地部署
- JSONL 数据集导出

---

## 2. 项目结构

```text
apps/
  api/          Go API 服务
  worker/       Go Worker 服务
  web-user/     用户端 React 前端
  web-admin/    管理端 React 前端
internal/
  config/       配置加载
  crypto/       密钥加密
  llm/          领域/问题/推理/奖励生成逻辑
  migrate/      SQL 迁移执行器
  model/        领域模型
  storage/      MinIO/S3 对象存储封装
  store/        PostgreSQL/Redis 数据访问层
sql/migrations/ 数据库迁移脚本
deployments/
  docker/       Dockerfile 与 nginx 配置
  compose/      Docker Compose 编排
```

---

## 3. 技术栈

### 前端
- React
- TypeScript
- Vite
- Ant Design（管理端）
- Framer Motion
- Lucide Icons

### 后端
- Go
- pgx / PostgreSQL
- Redis
- MinIO / S3 兼容对象存储

### 部署
- Docker
- Docker Compose
- nginx 反向代理

---

## 4. 启动方式

### 4.1 一键启动

在项目根目录执行：

```bash
docker compose -f deployments/compose/docker-compose.yml up -d --build
```

### 4.2 访问地址

- 用户端：`http://<你的服务器IP>:3210`
- 管理端：`http://<你的服务器IP>:3211`
- API 健康检查：`http://<你的服务器IP>:38080/healthz`
- Worker 健康检查：`http://<你的服务器IP>:38081/healthz`
- MinIO API：`http://<你的服务器IP>:19000`
- MinIO Console：`http://<你的服务器IP>:19001`

本项目默认外部端口已避开常见冲突端口，不需要停止现有服务。

---

## 5. 前端网络设计

### 5.1 为什么不会再出现浏览器访问 localhost 的问题

前端已改为**同源 API 调用**模式：

- 前端代码默认请求 `/api/...`
- `web-user` 与 `web-admin` 容器中的 nginx 会把 `/api/` 反向代理到 Go API 容器

因此：
- 浏览器不会直接请求 `http://localhost:38080`
- 不会再触发公网域名页面去访问 loopback 地址导致的 CORS / Private Network Access 错误

### 5.2 检查 frontend 是否写死 localhost

当前前端源码中的 API 请求已全部移除写死的 `localhost`。

可以用以下命令自检：

```bash
rg -n "localhost|127\.0\.0\.1" apps/web-user apps/web-admin -g '!**/dist/**'
```

如果命令无输出，说明前端源码没有写死本地回环地址。

补充说明：
- `deployments/compose/docker-compose.yml` 中的 `127.0.0.1` 仅用于 **容器内部健康检查**
- 这些健康检查不会暴露给浏览器，也不会让公网访问的页面去请求宿主机或用户本机的 loopback 地址

### 5.3 同源代理自检

可以直接通过前端入口验证同源 `/api` 代理是否正常：

```bash
curl http://127.0.0.1:3210/api/v1/platform/runtime
curl http://127.0.0.1:3210/api/v1/admin/generation-strategies
curl http://127.0.0.1:3211/api/v1/admin/dashboard
curl http://127.0.0.1:3211/api/v1/admin/providers
```

如果以上命令返回 JSON，说明：
- 浏览器侧请求走的是前端站点同源 `/api`
- `web-user` / `web-admin` 的 nginx 反向代理已经生效
- 前端不会因为写死 `localhost:38080` 而触发浏览器私网访问问题

---

## 6. 主要业务流程

### 6.1 管理员流程
1. 登录管理端
2. 配置模型提供方
3. 配置对象存储
4. 配置生成策略
5. 配置 Prompt 模板
6. 查看审计日志

### 6.2 用户流程
1. 登录用户端
2. 输入关键词与目标数据集规模
3. 进行计划估算
4. 创建数据集
5. 生成领域图谱
6. 人工修订并确认领域
7. 触发问题生成
8. 触发推理生成
9. 触发奖励数据生成
10. 触发导出并获取工件

---

## 7. 数据持久化设计

### PostgreSQL
保存：
- 模型配置
- 存储配置
- 生成策略
- Prompt 模板
- 数据集元数据
- 领域/问题/推理/奖励记录元数据
- 审计日志
- 导出工件元数据

### Redis
保存：
- 异步任务队列
- Worker 消费消息
- 队列长度统计

### MinIO / S3
保存：
- 推理长文本 JSON
- 奖励数据 JSON
- 导出 JSONL 工件

---

## 8. 已验证功能

已做自动化/半自动验证：

- `npm run build`
- Dockerized `go test ./...`
- API 镜像构建
- Worker 镜像构建
- 前端镜像构建
- PostgreSQL + Redis + MinIO + API + Worker 端到端 smoke test
- 通过 `http://127.0.0.1:3210/api/...` 与 `http://127.0.0.1:3211/api/...` 的同源代理验证

已验证的完整链路包括：
- 管理端配置模型提供方
- 管理端配置存储配置
- 管理端配置生成策略
- 管理端配置 Prompt
- 用户端计划估算
- 用户端创建数据集
- 用户端生成领域
- 用户端确认领域
- 用户端生成问题
- 用户端生成推理
- 用户端生成奖励数据
- 用户端导出 JSONL 数据集工件

最近一次同源烟雾验证结果：
- 通过管理端同源 `/api` 创建 mock 模型提供方、MinIO 存储配置、生成策略、Prompt 模板
- 通过用户端同源 `/api` 完成估算、建库、领域生成、领域确认、问题生成、推理生成、奖励生成、导出
- 验证结果：`domainCount=10`、`questionCount=20`、`reasoningCount=20`、`rewardCount=20`、`artifactCount=1`、`runtimeQueueDepth=0`

---

## 9. UI 说明

前端现已按 `/root/new-api/web/src` 的**页面结构**重做，而不是只做样式微调。

### 9.1 用户端页面
- 登录落地页
- 总览页
- 计划编排页
- 领域图谱页
- 问题生成页
- 推理生成页
- 奖励数据页
- 导出交付页

### 9.2 管理端页面
- 登录落地页
- 总览页
- 模型提供方页
- 存储配置页
- 生成策略页
- 提示词模板页
- 审计日志页

### 9.3 布局特征
- 固定顶部导航
- 固定左侧导航栏
- 右侧分页工作区
- 更接近 `/root/new-api` 的后台工作台信息架构
- 全部中文界面文案
- 保留同源 `/api` 调用方式，不再写死 localhost

说明：
- 少量技术专有名词（如 S3、MinIO、JSONL）保留英文，便于和基础设施配置保持一致

---

## 10. 常见问题

### 10.1 为什么用户端页面打不开 API？
请确认：
- `compose-api-1` 已启动
- `web-user` / `web-admin` 已重建到最新版本
- nginx 配置中的 `/api/` 反代已生效

可用以下命令测试：

```bash
curl http://127.0.0.1:3210/api/v1/platform/runtime
curl http://127.0.0.1:3211/api/v1/admin/dashboard
```

### 10.2 如果磁盘空间不足怎么办？
根据项目执行约束，优先清理 Docker 构建缓存：

```bash
docker builder prune -af
```

### 10.3 如果端口冲突怎么办？
禁止停止现有服务，直接修改 `deployments/compose/docker-compose.yml` 中的对外端口映射即可。

---

## 11. 停止服务

```bash
docker compose -f deployments/compose/docker-compose.yml down
```

如需连同卷一起删除：

```bash
docker compose -f deployments/compose/docker-compose.yml down -v
```

---

## 12. 后续建议

虽然当前系统已经完成从计划到导出的完整闭环，但仍建议继续做：

- 管理端代码分割与包体优化
- 更完整的浏览器端自动化测试
- 更强的任务状态可视化
- 真正的第三方 OpenAI 协议模型联调验证
- 更细粒度的失败重试与补偿机制
- 数据导出格式扩展（如 Parquet）

---

## 13. 维护说明

如果你要继续迭代本项目，建议优先遵循以下顺序：
1. 先修改数据模型与迁移
2. 再修改 store / llm / worker 逻辑
3. 再修改 API
4. 最后修改前端展示
5. 每次变更后都执行：

```bash
npm run build
docker compose -f deployments/compose/docker-compose.yml config
docker run --rm -v /root/llm:/workspace -w /workspace --entrypoint /bin/sh golang:1.24 -lc 'export PATH=/usr/local/go/bin:$PATH && go test ./...'
```
