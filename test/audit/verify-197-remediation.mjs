import { createRequire } from 'node:module'
// playwright 装在本机的 agent 目录（仓库未把它列为依赖）。用 createRequire
// 是为了让这个脚本保持 ESM（需要顶层 await），同时不把绝对路径写进源码 ——
// 换成环境变量后 CI 也可以指向自己的安装位置。
const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')
const BASE = 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const errors = []
page.on('pageerror', (e) => errors.push(String(e).slice(0, 160)))
page.on('response', (r) => { if (r.status() >= 400) errors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

const report = {}
// #197-10 今日工作总览磁贴
report.todayOverviewTiles = await page.locator('[data-today-overview="true"] a').count()
report.todayOverviewText = (await page.locator('[data-today-overview="true"]').innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 200)

// #197-1 项目列表分页常显
await page.goto(`${BASE}/projects`, { waitUntil: 'networkidle' })
await page.waitForTimeout(900)
report.projectsPaginationVisible = await page.locator('[data-projects-pagination="true"]').isVisible().catch(() => false)
report.projectsPaginationText = (await page.locator('[data-projects-pagination="true"]').innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 160)
report.projectsStatusTagText = (await page.locator('.project-card, .atelier-project-card').first().innerText().catch(() => '')).slice(0, 120)

// #197-2/#197-4/#197-5/#197-6 蓝图
await page.goto(`${BASE}/p/1/blueprint?node=standard`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
report.blueprintStepsEditor = await page.locator('[data-steps-editor="true"]').count()
report.blueprintPurpose = await page.locator('[data-node-purpose="true"]').count()
report.blueprintNodeSteps = await page.locator('[data-node-steps="true"]').count()
report.blueprintCanvasFlex = await page.locator('.blueprint-nodes').evaluate((el) => getComputedStyle(el).flexDirection).catch(() => 'n/a')
report.blueprintHistoryIsDetails = await page.locator('details.blueprint-history').count()
report.blueprintPageHeight = await page.evaluate(() => document.querySelector('.app-layout__content')?.scrollHeight ?? 0)

// #197-13 生产批次分析 + #190 缺口
await page.goto(`${BASE}/p/1/runs`, { waitUntil: 'networkidle' })
await page.waitForTimeout(900)
await page.goto(`${BASE}/p/1/runs/b_1`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
report.batchAnalysisPresent = await page.locator('[data-batch-analysis="true"]').count()
report.batchAnalysisText = (await page.locator('[data-batch-analysis="true"]').innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 260)
report.shortfallBanner = await page.locator('[data-batch-shortfall="true"]').count()

// #197-12 数据 vs 审阅
await page.goto(`${BASE}/p/1/data`, { waitUntil: 'networkidle' })
await page.waitForTimeout(900)
report.dataTitle = (await page.locator('h4').first().innerText().catch(() => '')).trim()
report.dataDesc = (await page.locator('.console-page__header').first().innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 200)
report.dataPreviewToggle = await page.locator('[data-payload-raw-toggle="true"]').count()
await page.goto(`${BASE}/p/1/review`, { waitUntil: 'networkidle' })
await page.waitForTimeout(900)
report.reviewTitle = (await page.locator('h4').first().innerText().catch(() => '')).trim()
report.reviewDesc = (await page.locator('.console-page__header').first().innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 200)

// #197-14 质量页术语 + 模型下拉
await page.goto(`${BASE}/p/1/quality`, { waitUntil: 'networkidle' })
await page.waitForTimeout(900)
report.qualityHeader = (await page.locator('.console-page__header').first().innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 200)
report.qualityHasFenmuWording = (await page.locator('.console-page').first().innerText().catch(() => '')).includes('分母')
await page.goto(`${BASE}/p/1/quality/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
report.qualityJudgeSelect = await page.locator('[data-judge-connection-select="true"]').count()

// #197-7/#197-8 连接设置
await page.goto(`${BASE}/settings/connections`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1000)
report.connectionRowActions = await page.locator('[data-connection-edit]').count()
report.connectionManageBtn = await page.locator('[data-connection-manage="true"]').count()
report.connectionStillOnPage = page.url().includes('/settings/connections')
// 点新增 -> 应就地弹窗，不离开页面
if (await page.locator('[data-connection-manage="true"]').count()) {
  await page.locator('[data-connection-manage="true"]').click()
  await page.waitForTimeout(700)
  report.connectionFormOpened = await page.locator('[data-connection-form="true"]').count()
  report.urlAfterAdd = page.url()
}

// #197-15 建号表单
await page.goto(`${BASE}/settings/team`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1000)
report.teamCreateEmail = await page.locator('[data-team-create-email="true"]').count()
report.teamCreatePassword = await page.locator('[data-team-create-password="true"]').count()
report.teamCreateSubmit = await page.locator('[data-team-create-submit="true"]').count()

// #197-16 迁移状态
await page.goto(`${BASE}/legacy/history`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
report.migrationStatusEl = await page.locator('[data-legacy-migration-status]').count()
report.migrationStatusText = (await page.locator('[data-legacy-migration-status]').innerText().catch(() => '')).replace(/\n+/g, ' ').slice(0, 220)
report.migrationStatusAttr = await page.locator('[data-legacy-migration-status]').getAttribute('data-legacy-migration-status').catch(() => null)

// #197-9 工作台与蓝图连接
await page.goto(`${BASE}/tools/evaluation`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
report.toolScopePresent = await page.locator('[data-tool-scope="true"]').count()
report.toolScopeText = (await page.locator('[data-tool-scope="true"]').innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 200)

// #194 移动端连接表
const mctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN' })
const mp = await mctx.newPage()
await mp.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await mp.getByPlaceholder('请输入邮箱').fill(EMAIL)
await mp.getByPlaceholder('请输入密码').fill(PASSWORD)
await mp.getByRole('button', { name: '进入今日工作' }).click()
await mp.waitForURL(/\/today/, { timeout: 20000 })
await mp.goto(`${BASE}/settings/connections`, { waitUntil: 'networkidle' })
await mp.waitForTimeout(1200)
report.mobileOverflowX = await mp.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
report.mobileFirstCellLabel = await mp.locator('[data-connection-id] > span[data-label]').first().evaluate((el) => getComputedStyle(el, '::before').content).catch(() => 'n/a')
report.mobileHeaderHidden = await mp.locator('.comparison-row--head').first().evaluate((el) => getComputedStyle(el).display).catch(() => 'n/a')

report.consoleErrors = errors.slice(0, 8)
console.log(JSON.stringify(report, null, 2))
await browser.close()
