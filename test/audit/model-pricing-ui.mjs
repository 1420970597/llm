/** 在容器内运行；复用现有 Playwright/Chromium，并以真实页面验证价格 API 契约。 */
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const baseURL = process.env.PRICING_UI_URL || 'http://127.0.0.1:13213'
const price = { id: 1, priceVersion: 'current-price', connectionId: 5, endpointFingerprint: 'fp', modelName: 'risk-model', currency: 'CNY', inputPriceMinorPerMillion: 100, outputPriceMinorPerMillion: 300, isFree: false, isEstimated: true, effectiveFrom: '2026-10-08T00:00:00Z', note: '现有报价' }
const fixture = { current: price, failRead: true, failSave: true, saves: [], role: 'admin', errors: [] }
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) })
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1024 } })
  page.setDefaultTimeout(60000)
  page.on('pageerror', (error) => fixture.errors.push(error.message))
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname
    let body = [], status = 200
    if (path.endsWith('/auth/me')) body = { user: { id: 1, email: 'price@example.test', role: fixture.role } }
    else if (path.endsWith('/connection-options')) body = { providers: [{ id: 5, name: '价格验收连接', model: 'risk-model', providerType: 'openai-compatible', isActive: true, apiKeyMasked: '***', configIssues: [] }], storageProfiles: [], notes: [] }
    else if (path.endsWith('/model-prices/5')) {
      if (request.method() === 'GET') {
        if (fixture.failRead) { fixture.failRead = false; status = 503; body = { error: { message: '价格暂不可读取' } } }
        else body = { price: fixture.current }
      } else {
        const input = request.postDataJSON(); fixture.saves.push(input)
        if (fixture.failSave) { fixture.failSave = false; status = 503; body = { error: { message: '价格保存暂时失败' } } }
        else { fixture.current = { ...price, ...input, id: fixture.saves.length }; body = { price: fixture.current } }
      }
    } else if (path.endsWith('/runtime')) body = {}
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.goto(`${baseURL}/settings/connections`)
  await page.locator('[data-studio-page="settings-connections"]').waitFor()
  await page.getByRole('button', { name: '价格配置', exact: true }).click()
  await page.getByRole('button', { name: '重新读取价格', exact: true }).click()
  await page.locator('[data-model-price-form]').waitFor()
  assert.equal(await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).inputValue(), '100')
  assert.equal(await page.getByRole('checkbox', { name: '使用保守估算价格' }).isChecked(), true)
  const save = () => page.getByRole('button', { name: '保存价格新版本', exact: true }).click()
  await page.getByRole('textbox', { name: '新价格版本名', exact: true }).fill('')
  await save(); await page.getByRole('alert').filter({ hasText: '请填写价格版本名' }).waitFor()
  await page.getByRole('textbox', { name: '新价格版本名', exact: true }).fill('new-price')
  await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).fill('-1')
  await save(); await page.getByRole('alert').filter({ hasText: '非负整数' }).waitFor()
  await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).fill('1.5')
  await save(); await page.getByRole('alert').filter({ hasText: '非负整数' }).waitFor()
  await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).fill('0')
  await page.getByRole('spinbutton', { name: '输出单价（分/百万 token）' }).fill('0')
  await save(); await page.getByRole('alert').filter({ hasText: '显式确认免费模型' }).waitFor()
  assert.equal(fixture.saves.length, 0)
  await page.getByRole('checkbox', { name: '显式确认免费模型' }).check()
  assert.equal(await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).isDisabled(), true)
  await save(); await page.getByRole('alert').filter({ hasText: '价格保存暂时失败' }).waitFor()
  assert.equal(await page.getByRole('textbox', { name: '新价格版本名', exact: true }).inputValue(), 'new-price')
  await save(); await page.locator('[data-model-price-form]').waitFor({ state: 'hidden' })
  assert.equal(fixture.current.isFree, true); assert.equal(fixture.current.isEstimated, false)
  await page.getByRole('button', { name: '价格配置', exact: true }).click()
  await page.locator('[data-model-price-form]').waitFor()
  await page.getByRole('checkbox', { name: '显式确认免费模型' }).uncheck()
  await page.getByRole('spinbutton', { name: '输入单价（分/百万 token）' }).fill('123')
  await page.getByRole('spinbutton', { name: '输出单价（分/百万 token）' }).fill('456')
  await page.getByRole('checkbox', { name: '使用保守估算价格' }).check()
  await page.setViewportSize({ width: 390, height: 844 })
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth) <= 1)
  mkdirSync('output/playwright', { recursive: true })
  await page.screenshot({ path: 'output/playwright/model-pricing-mobile.png', fullPage: true })
  await save(); await page.locator('[data-model-price-form]').waitFor({ state: 'hidden' })
  assert.equal(fixture.current.inputPriceMinorPerMillion, 123); assert.equal(fixture.current.outputPriceMinorPerMillion, 456); assert.equal(fixture.current.isEstimated, true)
  fixture.current = null
  await page.getByRole('button', { name: '价格配置', exact: true }).click()
  await page.getByText('当前没有价格配置，请填写实际单价或明确标记免费。', { exact: true }).waitFor()
  await page.getByRole('button', { name: '取消价格配置', exact: true }).click()
  fixture.role = 'user'
  await page.reload(); await page.locator('[data-connection-id="5"]').waitFor()
  assert.equal(await page.getByRole('button', { name: '价格配置', exact: true }).count(), 0)
  assert.deepEqual(fixture.errors, [])
  console.log('PASS: current price load/retry, empty config, missing version, negative/fractional/zero paid prices, explicit free, estimated quote, save failure preserving draft, admin-only entrance, 390px modal; no page errors.')
} finally { await browser.close() }
