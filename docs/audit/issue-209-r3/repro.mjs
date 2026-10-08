/**
 * Issue #209 复现取证：连接设置的「配置不完整」连接 + 蓝图「模型服务」下拉的空选项。
 *
 * 复现步骤（与 issue 正文一致）：
 *   1. 打开 /settings/connections → 模型连接表里多行「名称 / 模型 / 状态」几乎为空
 *   2. 打开 /p/1/blueprint?node=generation → 展开「模型服务」下拉 → 出现空选项
 *   3. GET /api/v1/admin/providers → 统计空记录与 not-a-url 记录
 *
 * 同条件重跑：同账号、同视口、同路由、同一组选择器，产出 01-before / 02-after 对比。
 *
 * 用法：
 *   node docs/audit/issue-209/repro.mjs before
 *   node docs/audit/issue-209/repro.mjs after
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const STAGE = process.argv[2] === 'after' ? 'after' : 'before'
const PREFIX = STAGE === 'after' ? '02' : '01'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT = process.env.PROJECT_ID ?? '1'
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

const evidence = { stage: STAGE, base: BASE, project: PROJECT, viewport: VIEWPORT, consoleErrors }

// ------------------------------------------------------------ 1. 连接列表 ----
await page.goto(`${BASE}/settings/connections`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2200)

evidence.connections = await page.evaluate(() => {
  const rows = [...document.querySelectorAll('[data-connection-id]')]
  return rows.map((row) => {
    const label = (name) => row.querySelector(`[data-label="${name}"]`)?.innerText.trim() ?? ''
    return {
      id: row.getAttribute('data-connection-id'),
      // 直接读单元格内部结构：空名字在界面上表现为「没有文字」。
      nameText: label('名称'),
      nameHasFallbackMark: /未命名|配置不完整|不可用于生成/.test(row.innerText),
      modelText: label('模型'),
      statusText: label('状态'),
      configWarning: row.querySelector('[data-connection-config-issues]')?.innerText.trim() ?? null,
    }
  })
})
// 「空名字」的行数——#209 的实测形态之一是名称为空字符串。
evidence.unnamedConnectionRows = evidence.connections.filter((row) => row.nameText === '').length
evidence.incompleteMarkedRows = evidence.connections.filter((row) => row.nameHasFallbackMark).length

await page.screenshot({ path: path.join(HERE, `${PREFIX}-connections.png`), fullPage: true })

// ------------------------------------------------- 2. 蓝图模型服务下拉 ----
await page.goto(`${BASE}/p/${PROJECT}/blueprint?node=generation`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)

const selector = page.locator('[data-field="blueprint-generation-modelConnectionId"] .semi-select')
if ((await selector.count()) === 0) {
  evidence.dropdown = { error: '未找到生成节点的「模型服务」下拉（选择器失效，证据不可用）' }
} else {
  await selector.first().click()
  await page.waitForTimeout(900)
  evidence.dropdown = await page.evaluate(() => {
    const options = [...document.querySelectorAll('.semi-select-option')]
    return {
      optionTexts: options.map((o) => o.innerText.trim()),
      // 空标签选项 = 用户无法辨认该选哪个（#209 的实测形态）。
      blankOptions: options.filter((o) => o.innerText.trim() === '').length,
      disabledOptions: options.filter((o) => o.className.includes('disabled')).length,
    }
  })
  await page.screenshot({ path: path.join(HERE, `${PREFIX}-blueprint-dropdown.png`), fullPage: true })
  await page.keyboard.press('Escape')
}

// ------------------------------------------------- 3. 接口读数（同一次运行） ----
const providers = await page.evaluate(async (project) => {
  const response = await fetch('/api/v1/admin/providers', { credentials: 'include' })
  return response.ok ? await response.json() : { error: response.status }
}, PROJECT)
if (Array.isArray(providers)) {
  evidence.providers = {
    total: providers.length,
    emptyNameAndBaseUrl: providers.filter((p) => p.name === '' && p.baseUrl === '').length,
    emptyActive: providers.filter((p) => p.name === '' && p.baseUrl === '' && p.isActive).length,
    invalidBaseUrl: providers.filter((p) => p.baseUrl !== '' && !/^https?:\/\/.+/.test(p.baseUrl)).length,
    withConfigIssues: providers.filter((p) => Array.isArray(p.configIssues) && p.configIssues.length > 0).length,
    rows: providers.map((p) => ({
      id: p.id, name: p.name, baseUrl: p.baseUrl, model: p.model, isActive: p.isActive,
      configIssues: p.configIssues ?? null,
    })),
  }
} else {
  evidence.providers = providers
}

writeFileSync(path.join(HERE, `${PREFIX}-evidence.json`), JSON.stringify(evidence, null, 2) + '\n')
console.log(JSON.stringify(evidence, null, 2))
await browser.close()
