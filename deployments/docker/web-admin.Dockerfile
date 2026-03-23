FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json ./
COPY apps/web-user/package.json ./apps/web-user/package.json
COPY apps/web-admin/package.json ./apps/web-admin/package.json
RUN npm install
COPY apps/web-admin ./apps/web-admin
RUN npm run build -w apps/web-admin

FROM nginx:1.27-alpine
COPY deployments/docker/nginx/web-admin.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/apps/web-admin/dist /usr/share/nginx/html
EXPOSE 80
