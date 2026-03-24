# Frontend Unification Refactor Plan

## Goal
将当前分离的用户端与管理端前端合并为一个统一控制台，并对齐 `/root/new-api` 最新前端的核心技术栈与控制台布局。

## Required outcomes
1. 使用与 new-api 一致的前端主技术栈：React + Vite + Semi UI + TailwindCSS + React Router。
2. 单一前端应用承载用户与管理员功能。
3. 登录后基于角色区分可见导航与可用能力。
4. 管理员拥有全部用户能力，并额外拥有系统配置能力。
5. 保持现有后端业务接口与同源 `/api` 访问方式。

## Backend changes
1. 增加登录鉴权接口与会话能力。
2. 增加 `/api/v1/auth/login`、`/api/v1/auth/me`、`/api/v1/auth/logout`。
3. 增加管理员权限校验，保护 `/api/v1/admin/*`。
4. 启动时引导默认管理员/默认用户账号，便于首启验证。

## Frontend changes
1. 以统一控制台替代当前双前端分离模型。
2. 顶部 Header + 左侧 Sider + 右侧路由工作区。
3. 路由拆分：登录、总览、数据集、领域图谱、问题、推理、奖励、导出、模型提供方、存储、策略、提示词、审计。
4. 管理员显示完整导航；普通用户仅显示数据集与生产链路导航。

## Deployment changes
1. web-user 与 web-admin 容器可复用同一前端构建产物。
2. 保持现有端口可访问；必要时让 admin 入口复用或跳转到统一控制台。

## Verification
1. `npm run build`
2. Docker rebuild unified frontend containers
3. 登录 / me / logout smoke
4. 普通用户数据链路 smoke
5. 管理员配置 + 数据链路 smoke
6. 前端源码无写死 localhost
