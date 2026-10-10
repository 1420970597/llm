/** Read-only UI layout acceptance against a real backend. No API responses or DOM fixtures are substituted. */
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const baseURL = process.env.REAL_LAYOUT_URL
assert(baseURL, 'REAL_LAYOUT_URL must identify the real preview/deployed stack')
assert(process.env.AUDIT_EMAIL && process.env.AUDIT_PASSWORD, 'Provide the audit account in environment variables')
const projectName = process.env.REAL_LAYOUT_PROJECT || '真实导入审阅发布验收-20261010'
const artifacts = 'output/playwright/real-layout'
mkdirSync(artifacts, { recursive: true })
const report = { baseURL, viewports: [], checks: [] }
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.setDefaultTimeout(15000)
async function screenshot(name) {
  await page.screenshot({ path: `${artifacts}/${name}.png`, mask: [page.locator('.sidebar-account')] })
}
async function checkCanvas(name) {
  const canvas = await page.locator('.app-layout__content').evaluate(el => {
    const rect = el.getBoundingClientRect()
    return { x: rect.x, y: rect.y, width: rect.width, height: rect.height, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }
  })
  const width = page.viewportSize().width
  assert.equal(canvas.y, width <= 960 ? 0 : 64, `${name}: short page must start at the correct shell position`)
  assert(canvas.scrollWidth <= canvas.clientWidth + 1, `${name}: page content must not overflow horizontally`)
  report.checks.push(`${width}:${name}:canvas-position-and-width`)
  return canvas
}
async function assertClickable(locator, label) {
  await locator.scrollIntoViewIfNeeded()
  await locator.click({ trial: true })
  const geometry = await locator.evaluate(el => {
    const r = el.getBoundingClientRect()
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
    return { x: r.x, y: r.y, width: r.width, height: r.height, hittable: hit === el || el.contains(hit) }
  })
  assert(geometry.hittable, `${label}: action center is covered`)
  report.checks.push(`${page.viewportSize().width}:${label}:visible-action-hit`)
  return geometry
}
async function clickStage(label) {
  await page.getByRole('navigation', { name: '项目工作区' }).getByRole('link').filter({ hasText: label }).click()
  await page.waitForTimeout(350)
}
async function designPage(label) {
  await clickStage('设计')
  if (label !== '设计') await page.getByRole('navigation', { name: '设计任务' }).getByRole('link', { name: label, exact: true }).click()
  await page.waitForTimeout(350)
}

try {
  await page.goto(baseURL)
  await page.locator('input[name="email"]').fill(process.env.AUDIT_EMAIL)
  await page.locator('input[name="password"]').fill(process.env.AUDIT_PASSWORD)
  await page.locator('button[type="submit"]').click()
  await page.waitForURL('**/projects')
  await page.waitForTimeout(1200)
  const card = page.getByRole('button', { name: `打开项目 ${projectName}`, exact: true })
  await card.waitFor()
  const cardLayout = await card.evaluate(el => {
    const r = el.getBoundingClientRect(), tag = el.querySelector('.semi-tag').getBoundingClientRect()
    return { padding: Number.parseFloat(getComputedStyle(el).paddingLeft), tagWidth: tag.width, tagRight: tag.right, right: r.right }
  })
  assert(cardLayout.padding >= 16 && cardLayout.tagWidth >= 35 && cardLayout.tagRight < cardLayout.right, 'Project cards must contain padded text and an uncompressed type badge')
  report.checks.push('projects:padded-card-and-complete-type-label')
  await card.click()

  for (const width of [1440, 768, 390]) {
    await page.setViewportSize({ width, height: 900 })
    const result = { width, pages: [] }
    if (width <= 960) {
      await page.getByRole('button', { name: '打开主导航' }).click()
      const nav = page.getByRole('navigation', { name: '主导航' })
      for (const name of ['数据项目', '待处理', '方案库', '交付库']) {
        const link = nav.getByRole('link', { name, exact: true })
        const box = await assertClickable(link, `main-nav-${name}`)
        assert(box.height >= 44 && box.height <= 60, 'Drawer navigation must use compact clickable rows')
      }
      await screenshot(`drawer-${width}-verified`)
      await page.keyboard.press('Escape')
      await nav.waitFor({ state: 'hidden' })
    } else {
      for (const name of ['数据项目', '待处理', '方案库', '交付库']) {
        await assertClickable(page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name, exact: true }), `main-nav-${name}`)
      }
    }

    await designPage('设计')
    const rail = page.locator('.blueprint-configuration-steps')
    const railGeometry = await rail.evaluate(el => ({ height: el.clientHeight, scrollHeight: el.scrollHeight, overflow: getComputedStyle(el).overflowY }))
    if (width > 760) assert(railGeometry.height <= 560 && railGeometry.scrollHeight > railGeometry.height, 'Desktop step list must scroll instead of stretching the form row')
    await rail.getByRole('button').last().click()
    const selectedStep = rail.locator('button[aria-current]')
    await assertClickable(selectedStep, 'last-design-step')
    const saveButton = page.getByRole('button', { name: '保存为新版本', exact: true }).first()
    if (await saveButton.count() && !(await saveButton.isDisabled())) await assertClickable(saveButton, 'design-save')
    result.pages.push({ page: 'design', canvas: await checkCanvas('design'), rail: railGeometry })
    await screenshot(`design-${width}-verified`)

    for (const label of ['目标结构', '素材来源', '思维标准', '外部数据集导入']) {
      await designPage(label)
      result.pages.push({ page: label, canvas: await checkCanvas(label) })
      await screenshot(`${{ '目标结构': 'coverage', '素材来源': 'sources', '思维标准': 'standard', '外部数据集导入': 'import' }[label]}-${width}-verified`)
      if (label === '外部数据集导入') {
        const field = page.getByRole('textbox', { name: '导入名称', exact: true })
        const fits = await field.evaluate(el => { const r = el.getBoundingClientRect(), card = el.closest('.semi-card').getBoundingClientRect(); return r.right <= card.right && r.left >= card.left })
        assert(fits, 'Import name field must stay inside its card')
        report.checks.push(`${width}:import-field-contained`)
      }
    }

    for (const stage of ['生产', '审阅', '发布']) {
      await clickStage(stage)
      result.pages.push({ page: stage, canvas: await checkCanvas(stage) })
      await screenshot(`${{ '生产': 'production', '审阅': 'review', '发布': 'release' }[stage]}-${width}-verified`)
      if (stage === '审阅') {
        const queue = page.locator('[data-review-queue-row]').first()
        if (await queue.count()) {
          await queue.click()
          await page.waitForTimeout(350)
          await checkCanvas('review-detail')
          await screenshot(`review-detail-${width}-verified`)
        }
      }
      if (stage === '发布') {
        await page.getByRole('button', { name: '新建发布', exact: true }).click()
        await page.waitForTimeout(350)
        await checkCanvas('release-new')
        await screenshot(`release-new-${width}-verified`)
        await assertClickable(page.locator('[data-release-next]'), 'release-next')
      }
    }
    report.viewports.push(result)
  }
  console.log(`Real layout acceptance passed: ${report.checks.length} checks, ${report.viewports.length} viewports`)
} catch (error) {
  report.error = error.message
  await screenshot('verification-failure')
  throw error
} finally {
  writeFileSync(`${artifacts}/verification.json`, JSON.stringify(report, null, 2))
  await browser.close()
}
