#!/usr/bin/env bash
# R22 lane 的候选前端（真浏览器验证用）。把 lane 的 dist 挂到 nginx，端口 18122。
# 只用静态前端 + 复用既有 API 容器，因此不需要重建 api/worker。
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=18122
npm run build -w apps/web-user >/dev/null
docker rm -f l15-r22-web >/dev/null 2>&1 || true
# nginx 需要把 /api/ 代理到 llm-api-1，因此用 llm_default 网络 + 自定义 conf
cat > /tmp/l15-r22-nginx.conf <<'CONF'
server {
  listen 80;
  server_name _;
  resolver 127.0.0.11 ipv6=off valid=30s;
  root /usr/share/nginx/html;
  index index.html;
  location /api/ {
    set $api_upstream llm-api-1:8080;
    proxy_pass http://$api_upstream;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
  }
  location / { try_files $uri $uri/ /index.html; }
}
CONF
docker run -d --name l15-r22-web --network llm_default -p "$PORT:80" \
  -v "$PWD/apps/web-user/dist:/usr/share/nginx/html:ro" \
  -v /tmp/l15-r22-nginx.conf:/etc/nginx/conf.d/default.conf:ro nginx:alpine >/dev/null
for _ in $(seq 1 30); do
  c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/login" || true)
  [ "$c" = "200" ] && { echo "web ready ($PORT)"; exit 0; }
  sleep 1
done
echo "web not ready" >&2; exit 1
