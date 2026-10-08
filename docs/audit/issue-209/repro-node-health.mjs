/**
 * #209 补充取证：已保存蓝图引用**配置不完整**的连接时，节点健康必须提前判 blocked。
 *
 * 为什么单独一次取证：主复现脚本覆盖的是「下拉不出现无字选项」；
 * 而 issue 的核心损失是「错误在批次已开跑才出现」。历史蓝图可能已经存了一条
 * 不完整连接的引用，此时**保存/执行之前**就必须给出可操作提示。
 *
 * 这个状态需要一个引用坏连接的蓝图版本，因此取证脚本会：
 *   1. 读取当前最新蓝图版本；
 *   2. 把 generation.modelConnectionId 指向一条已知不完整的连接；
 *   3. 存为新版本（服务端允许草稿存不完整配置，这正是要复现的前提）；
 *   4. 打开设计页，读取生成节点的健康状态。
 *
 * 用法：
 *   node docs/audit/issue-209/repro-node-health.mjs \
 *     [--project 2] [--connection 19] [--stage after]
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const arg = (name, fallback) => {
  const index = process.argv.indexOf(`--${name}`)
  return index >= 0 && process.argv[index + 1] ? process.argv[index + 1] : fallback
}
const PROJECT = arg('project', '2')
const CONNECTION = Number(arg('connection', '19'))
const STAGE = arg('stage', 'after')
const PREFIX = STAGE === 'before' ? '01' : '02'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const VIEWPORT = { width: 1600, height: 1000 }

mkdirSync(HERE, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: VIEWPORT, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

const consoleErrors = []
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)) })
page.on('pageerror', (e) => consoleErrors.push(String(e).slice(0, 200)))

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
await page.getByPlaceholder('请输入密码').fill('admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

// ---- 1. 让最新蓝图引用一条不完整的连接 ----
const prepared = await page.evaluate(async ({ project, connection }) => {
  const readDocument = async () => {
    const response = await fetch(`/api/v1/projects/${project}/blueprint-versions`, { credentials: 'include' })
    return response.ok ? await response.json() : { error: `list ${response.status}` }
  }
  const listed = await readDocument()
  if (!listed.items || listed.items.length === 0) return { error: '该项目没有蓝图版本' }
  const latest = listed.items[0]
  const payload = latest.payload
  payload.nodes = payload.nodes ?? {}
  payload.nodes.generation = { ...(payload.nodes.generation ?? {}), modelConnectionId: connection }
  const saved = await fetch(`/api/v1/projects/${project}/blueprint-versions`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': `audit-209-nh-${Date.now()}` },
    body: JSON.stringify({
      payload,
      changeReason: '审计取证：让生成节点引用一条配置不完整的连接',
      expectedRevision: listed.document?.revision ?? 0,
    }),
  })
  return { savedStatus: saved.status, savedBody: (await saved.text()).slice(0, 200), connection }
}, { project: PROJECT, connection: CONNECTION })

// ---- 2. 读取生成节点的健康状态 ----
await page.goto(`${BASE}/p/${PROJECT}/blueprint?node=generation`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)

const health = await page.evaluate(() => {
  const inspector = document.querySelector('[data-field="blueprint-generation-modelConnectionId"]')
  const panel = inspector?.closest('.console-card') ?? document.body
  return {
    inspectorText: (panel?.innerText ?? '').replace(/\n+/g, ' | ').slice(0, 600),
    selectedLabel: document.querySelector('[data-field="blueprint-generation-modelConnectionId"] .semi-select-selection-text')?.innerText?.trim() ?? null,
  }
})
await page.screenshot({ path: path.join(HERE, `${PREFIX}-node-health.png`), fullPage: true })

const evidence = { stage: STAGE, project: PROJECT, connection: CONNECTION, viewport: VIEWPORT, prepared, health, consoleErrors }
writeFileSync(path.join(HERE, `${PREFIX}-node-health.json`), JSON.stringify(evidence, null, 2) + '\n')
console.log(JSON.stringify(evidence, null, 2))
await browser.close()
