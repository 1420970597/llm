FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json ./
COPY apps/web-user/package.json ./apps/web-user/package.json
RUN npm install
COPY apps/web-user ./apps/web-user

# 构建期注入 git 版本（issue #88）。
#
# 为什么必须注入：镜像里的产物此前不携带任何版本信息，页面上也看不到，
# 于是「我跑的是哪一版」只能靠比对镜像构建时间与提交时间来间接推断 ——
# 实测就是这么漏掉了一次（容器停在 compose-web-user:latest 的旧镜像上，
# 五个阶段路由全部被旧的重定向逻辑吃掉）。
#
# 默认值是 unknown 而**不是**伪造一个 SHA：构建方没传就必须显式表现为
# 「无法自证」，而不是看起来像当前源码。
ARG GIT_SHA=unknown
ARG BUILD_TIME=
ENV GIT_SHA=${GIT_SHA}
ENV BUILD_TIME=${BUILD_TIME}
RUN npm run build -w apps/web-user

FROM nginx:1.27-alpine
COPY deployments/docker/nginx/web-user.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/apps/web-user/dist /usr/share/nginx/html

# 把注入的版本记录成镜像标签，便于 `docker image inspect` 直接核对，无需进容器。
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"
EXPOSE 80
