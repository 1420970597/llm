import {readFileSync,writeFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const dir=path.dirname(fileURLToPath(import.meta.url));
const css=readFileSync(path.join(dir,'styles.css'),'utf8');
const js=['catalog.js','app.js'].map(f=>readFileSync(path.join(dir,f),'utf8')).join('\n');
writeFileSync(path.join(dir,'../prototype.html'),`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="favicon.svg"><title>Atelier · 数据项目工作室</title><style>${css}</style></head><body><div id="app"></div><dialog id="dialog" aria-labelledby="dialog-title"></dialog><div id="toast" role="status" hidden></div><script>${js}</script></body></html>\n`);
console.log('Built standalone Data Studio prototype');
