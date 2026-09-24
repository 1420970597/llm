import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const dir = path.dirname(fileURLToPath(import.meta.url));
const css = readFileSync(path.join(dir, 'styles.css'), 'utf8');
const js = readFileSync(path.join(dir, 'app.js'), 'utf8');

// 输出到与本目录同级的 prototype.html：不向上一级写文件。
// 理由：构建在容器内执行（宿主机无 Node），挂载点是本目录；写 `..`
// 会落到容器根而不是仓库，产物会静默丢失。
writeFileSync(
  path.join(dir, 'prototype.html'),
  `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Atelier · 素材来源原型</title><style>${css}</style></head><body><div id="app"></div><script>${js}</script></body></html>\n`,
);
console.log('Built standalone Atelier source prototype');
