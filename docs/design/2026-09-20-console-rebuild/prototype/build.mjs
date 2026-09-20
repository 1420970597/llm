import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
const dir = path.dirname(fileURLToPath(import.meta.url));
const css = readFileSync(path.join(dir, "styles.css"), "utf8");
const js = ["catalog.js", "app.js"]
  .map((f) => readFileSync(path.join(dir, f), "utf8"))
  .join("\n");
const html = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><link rel="icon" href="data:,"><meta name="description" content="LLM 数据工厂：产品调研后的完整交互原型。演示数据，不连接业务接口。"><title>序列数据工厂 · 交互原型</title><style>${css}</style></head>
<body><div id="app"></div><footer class="prototype-bar"><span><strong>交互原型 / 演示数据</strong> · <span id="screen-id"></span></span><div class="prototype-controls"><a href="#/catalog">全站目录</a><label>场景 <select id="scenario"><option value="normal">正常</option><option value="empty">空数据</option><option value="loading">加载中</option><option value="error">请求失败</option><option value="offline">断网</option><option value="denied">无权限</option><option value="missing">404</option></select></label><label>身份 <select id="role"><option value="admin">管理员</option><option value="member">成员</option></select></label><button id="reset-demo" class="quiet" style="font-size:11px">重置演示</button></div></footer><div class="toast" id="toast" role="status" aria-live="polite" hidden></div><dialog id="modal" aria-labelledby="modal-title"><div class="modal-head"><h2 id="modal-title"></h2><button id="modal-close" aria-label="关闭对话框">×</button></div><div class="modal-body" id="modal-body"></div><div class="modal-foot"><button id="modal-cancel">取消</button><button id="modal-confirm" class="primary">确认</button></div></dialog><script>${js}</script></body></html>`;
writeFileSync(path.join(dir, "..", "prototype.html"), html);
console.log(
  "Built standalone prototype.html; no external assets or API requests.",
);
