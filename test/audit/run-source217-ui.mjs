/**
 * 容器内运行：PLAYWRIGHT_MODULE=/path/to/playwright SOURCE_UI_URL=http://127.0.0.1:13212
 *   node test/audit/run-source217-ui.mjs
 * 复用既有 Playwright/Chromium，不安装仓库依赖。
 */
import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'
import { install, verify } from './source217-ui.mjs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) })
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1024 } })
  const baseURL = process.env.SOURCE_UI_URL || 'http://127.0.0.1:13212'
  console.log(await install(page, baseURL))
  console.log(await verify(page, baseURL))
  mkdirSync('output/playwright', { recursive: true })
  await page.screenshot({ path: 'output/playwright/source217-import-mobile.png', fullPage: true })
} finally { await browser.close() }
