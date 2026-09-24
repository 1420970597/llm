/* 把 styles.css + catalog.js + app.js 打包成单文件 prototype.html。
 *
 * 与 docs/design/2026-09-21-data-studio/prototype/build.mjs 同一做法：
 * 原型必须是**可以直接下载、双击打开**的单个 HTML（评审者不应该需要 npm）。
 * 用容器执行，因为宿主机没有 Node（AGENTS.md §1）。
 *
 *   docker run --rm -v "$PWD":/w -w /w node:20-alpine node build.mjs
 */
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const dir = path.dirname(fileURLToPath(import.meta.url))
const css = readFileSync(path.join(dir, 'styles.css'), 'utf8')
const js = ['app.js'].map((f) => readFileSync(path.join(dir, f), 'utf8')).join('\n')

writeFileSync(
  path.join(dir, 'prototype.html'),
  `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">` +
    `<meta name="viewport" content="width=device-width,initial-scale=1">` +
    `<title>Atelier · 蓝图工作流化改造原型（#165 × #197）</title>` +
    `<style>${css}</style></head><body><div id="app"></div><script>${js}</script></body></html>\n`,
)
console.log('Built standalone blueprint-workflow-rearchitecture prototype')
