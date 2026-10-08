/**
 * 容器内运行：PLAYWRIGHT_MODULE=/path/to/playwright SOURCE_UI_URL=http://127.0.0.1:13212
 *   node test/audit/run-source217-ui.mjs
 * 复用既有 Playwright/Chromium，不安装仓库依赖。
 */
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'
import { install, verify } from './source217-ui.mjs'
import { verifyProjectTools } from './project-tools-ui.mjs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const output = 'output/playwright'
mkdirSync(output, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) })
const context = await browser.newContext({ viewport: { width: 1440, height: 1024 } })
context.setDefaultTimeout(30000)
context.setDefaultNavigationTimeout(20000)
await context.tracing.start({ screenshots: true, snapshots: true, sources: true })
const page = await context.newPage()
const baseURL = process.env.SOURCE_UI_URL || 'http://127.0.0.1:13212'
const report = { status: 'running', baseURL, controlledDocumentAPIs: true, browser: browser.version(), startedAt: new Date().toISOString() }
try {
  console.log(await install(page, baseURL))
  report.result = await verify(page, baseURL)
  await page.screenshot({ path: `${output}/source217-import-mobile.png`, fullPage: true })
  report.projectTools = await verifyProjectTools(page, baseURL)
  report.status = 'passed'
  console.log(report.result)
  await page.screenshot({ path: `${output}/source217-project-tools-mobile.png`, fullPage: true })
} catch (error) {
  report.status = 'failed'
  report.error = error.stack || error.message
  await page.screenshot({ path: `${output}/source217-failure.png`, fullPage: true }).catch(() => {})
  throw error
} finally {
  report.finishedAt = new Date().toISOString()
  writeFileSync(`${output}/source217-report.json`, JSON.stringify(report, null, 2) + '\n')
  await context.tracing.stop({ path: `${output}/source217-trace.zip` })
  await browser.close()
}
