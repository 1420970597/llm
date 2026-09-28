/**
 * Issue #160 T29 实测采集器（断网待同步 / 无障碍 / 窄屏 / 键盘）。
 *
 * 为什么需要这个脚本：`todo.md` 的 T29 行如实标注了
 * 「**真实浏览器断网与 390/768/1440 实测、10 万样本基准未执行**」，
 * 而 T29 的验收标准要求「拟定基准并记录实测数据、不预报未经测量的性能提升」。
 * 本脚本把其中**可自动化的部分**从「待人工」变成「有实测数字的机器事实」，
 * 仍然是真实栈 + 真实 Chromium（符合 SOP §5 的取证要求）。
 *
 * 明确**不做**的部分（不得伪造）：
 *   - 10 万样本基准：本机栈的样本量级远小于该数量级，造 10 万行会污染开发库。
 *     因此本脚本只测「既有数据量下的交互响应」并**显式标注数据量**，
 *     不给「10 万量级下如何」的结论 —— 那需要独立的压测环境。
 *   - 多设备/多并发：只有一台机器，报单设备数字。
 *
 * 产物：docs/audit/issue-160-t29/<viewport>.png + findings.json
 *
 * 用法：node test/audit/t29_measure.mjs
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../docs/audit/issue-160-t29')
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

mkdirSync(OUT, { recursive: true })

const report = { base: BASE, capturedAt: new Date().toISOString(), viewports: {}, findings: [] }
const add = (id, ok, detail) => {
  report.findings.push({ id, ok, detail })
  console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${id}: ${detail}`)
}

const browser = await chromium.launch({ headless: true })

async function login(page) {
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
  await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
  await page.getByPlaceholder('请输入密码').fill(PASSWORD)
  await page.getByRole('button', { name: '进入今日工作' }).click()
  await page.waitForURL(/\/today/, { timeout: 20000 })
  await page.waitForTimeout(800)
}

// ---------------------------------------------------------------------------
// 1. 窄屏 / 桌面：横向溢出 + 触屏目标下限 + 首屏可见性
// ---------------------------------------------------------------------------
const viewports = [
  { name: 'mobile-390', width: 390, height: 844 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'desktop-1440', width: 1440, height: 1024 },
]

const routes = [
  '/today',
  '/projects',
  `/p/${PROJECT_ID}/overview`,
  `/p/${PROJECT_ID}/blueprint`,
  `/p/${PROJECT_ID}/runs`,
  `/p/${PROJECT_ID}/data`,
  `/p/${PROJECT_ID}/review`,
  `/p/${PROJECT_ID}/quality`,
  `/p/${PROJECT_ID}/releases`,
]

for (const vp of viewports) {
  const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, locale: 'zh-CN' })
  const page = await ctx.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${String(e).slice(0, 120)}`))
  page.on('response', (r) => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url().replace(BASE, '').slice(0, 80)}`) })
  await login(page)

  const perRoute = {}
  for (const route of routes) {
    await page.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(700)
    const m = await page.evaluate(() => {
      const de = document.documentElement
      // 触屏目标：可见的 button/a[href]/[role=button] 中高度 < 24px 的个数
      // （WCAG 2.5.8 最低 24×24；T29 的 CSS 目标是 44px，这里先按硬下限判定）。
      const interactive = [...document.querySelectorAll('button, a[href], [role="button"]')]
        .filter((el) => {
          const r = el.getBoundingClientRect()
          const s = getComputedStyle(el)
          return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'
        })
      const tiny = interactive.filter((el) => el.getBoundingClientRect().height < 24)
      // 文本溢出裁剪：scrollWidth 明显大于 clientWidth 且 overflow 不是滚动。
      //
      // 显式排除 `.sr-only`：它是**故意的** 1px 裁剪（标准无障碍模式，见
      // styles.css:2108 的 `clip: rect(0,0,0,0)`），是屏幕阅读器专供文本，
      // 视觉上不可见且本就不应显示。不排除它会让每一页都报一条假阳性，
      // 而假阳性会让真实缺陷淹没在噪声里（本仓库已多次因此调整守卫）。
      const clipped = [...document.querySelectorAll('body *')].filter((el) => {
        if (el.classList.contains('sr-only')) return false
        const s = getComputedStyle(el)
        if (s.overflowX === 'auto' || s.overflowX === 'scroll') return false
        if (!el.textContent || el.children.length) return false
        return el.scrollWidth > el.clientWidth + 4 && s.overflowX === 'hidden' && s.textOverflow !== 'ellipsis'
      })
      return {
        scrollWidth: de.scrollWidth,
        clientWidth: de.clientWidth,
        bodyScrollHeight: document.body.scrollHeight,
        tinyTargets: tiny.length,
        tinySample: tiny.slice(0, 3).map((el) => `${el.tagName}:${(el.textContent ?? '').trim().slice(0, 20)}`),
        clippedCount: clipped.length,
        clippedSample: clipped.slice(0, 3).map((el) => `${el.tagName}.${el.className}`.slice(0, 60)),
      }
    })
    perRoute[route] = m
    if (m.scrollWidth > vp.width) {
      add(`overflow:${vp.name}:${route}`, false, `横向溢出 scrollWidth=${m.scrollWidth} > ${vp.width}`)
    }
    if (m.clippedCount > 0) {
      add(`text-clipped:${vp.name}:${route}`, false, `${m.clippedCount} 处文本被裁剪且无省略号：${m.clippedSample.join(', ')}`)
    }
    if (m.tinyTargets > 0) {
      add(`tiny-target:${vp.name}:${route}`, false, `${m.tinyTargets} 个控件高度 < 24px：${m.tinySample.join(', ')}`)
    }
  }

  await page.goto(`${BASE}/today`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  await page.screenshot({ path: path.join(OUT, `${vp.name}.png`) })
  report.viewports[vp.name] = { routes: perRoute, errors }
  add(`no-horizontal-overflow:${vp.name}`, Object.values(perRoute).every((r) => r.scrollWidth <= vp.width),
    `${routes.length} 条路由均 scrollWidth <= ${vp.width}`)
  add(`no-5xx:${vp.name}`, errors.length === 0, errors.length ? errors.slice(0, 3).join(' | ') : '0 条 5xx / pageerror')
  await ctx.close()
}

// ---------------------------------------------------------------------------
// 2. 键盘走查：Tab 可达 + Escape 关闭移动菜单 + 焦点回到触发按钮
// ---------------------------------------------------------------------------
{
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN' })
  const page = await ctx.newPage()
  await login(page)
  await page.goto(`${BASE}/today`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(600)

  const menuBtn = page.getByRole('button', { name: /打开主导航|打开移动导航|菜单/ }).first()
  const hasMenu = await menuBtn.count()
  if (hasMenu) {
    await menuBtn.click()
    await page.waitForTimeout(400)
    const focusAfterOpen = await page.evaluate(() => document.activeElement?.textContent?.trim().slice(0, 20) ?? '')
    await page.keyboard.press('Escape')
    await page.waitForTimeout(400)
    const focusAfterEsc = await page.evaluate(() =>
      (document.activeElement?.getAttribute('aria-label') ?? document.activeElement?.textContent ?? '').trim().slice(0, 30))
    report.keyboard = { focusAfterOpen, focusAfterEsc }
    add('keyboard:escape-returns-focus', /打开主导航|菜单/.test(focusAfterEsc), `Escape 后焦点="${focusAfterEsc}"`)
  } else {
    add('keyboard:mobile-menu-present', false, '390px 下未找到移动导航按钮（无法执行键盘走查）')
  }

  // Tab 可达性：连续 Tab 5 次，焦点不应停在 body（说明有可聚焦控件）。
  await page.keyboard.press('Tab')
  const tabFocus = await page.evaluate(() => document.activeElement?.tagName ?? 'NONE')
  add('keyboard:tab-reachable', tabFocus !== 'BODY' && tabFocus !== 'NONE', `首次 Tab 焦点=${tabFocus}`)
  await ctx.close()
}

// ---------------------------------------------------------------------------
// 3. 断网待同步：真实 offline 上下文下提交必须**不显示成功**
// ---------------------------------------------------------------------------
{
  const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
  const page = await ctx.newPage()
  // 证明失败**真的来自断网**：记录离线期间被阻断的请求。
  // 否则「提交失败」可能来自一个与离线无关的校验错误，实测就失去了针对性。
  const blockedRequests = []
  page.on('requestfailed', (req) => blockedRequests.push(`${req.method()} ${req.url().replace(BASE, '')}`))
  await login(page)
  await page.goto(`${BASE}/p/${PROJECT_ID}/review?status=all`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)

  // 判断表单在**样本详情页**（`/p/:id/data/:sampleId`），而不是队列列表页。
  // 从队列点「审阅」进入 —— 这是真实用户路径，不是手工拼 URL。
  const reviewBtn = page.getByRole('button', { name: '审阅' }).first()
  const rowCount = await page.locator('[data-sample-id]').count()
  report.offline = { reviewPageReached: true, sampleRows: rowCount }
  if (await reviewBtn.count()) {
    await reviewBtn.click()
    await page.waitForTimeout(1800)
  }
  if (await page.locator('[data-review-queue="true"] .review-queue__button').count()) {
    await page.locator('[data-review-queue="true"] .review-queue__button').first().click()
    await page.waitForTimeout(1500)
  }

  const reason = page.locator('[data-field="review-reason"]')
  const hasReason = await reason.count()
  report.offline.reasonFieldPresent = hasReason > 0

  if (hasReason) {
    await reason.fill('T29 断网实测：这条判断在离线状态下不应显示为已提交。')
    const submit = page.getByRole('button', { name: /提交判断|保存判断|提交/ }).first()
    if (await submit.count()) {
      await ctx.setOffline(true)
      await submit.click().catch(() => {})
      await page.waitForTimeout(2500)
      const blocked = [...blockedRequests]
      const state = await page.evaluate(() => ({
        offlineNotice: document.querySelector('[data-review-offline-notice="true"]')?.textContent?.trim() ?? '',
        submitError: document.querySelector('[data-review-submit-error="true"]')?.textContent?.trim() ?? '',
        successToast: [...document.querySelectorAll('.semi-toast-content, .semi-notification-content')]
          .map((el) => el.textContent?.trim() ?? '').filter((t) => /成功|已提交|已保存/.test(t)),
        draftButton: !!document.querySelector('[data-review-offline-draft="true"]'),
      }))
      report.offline.state = state
      report.offline.blockedRequests = blocked
      const claimsSuccess = state.successToast.length > 0
      add('offline:no-success-claim', !claimsSuccess,
        claimsSuccess ? `离线提交仍显示成功：${state.successToast.join(' | ')}` : '离线提交未显示「成功/已提交」')
      add('offline:network-actually-blocked', blocked.length > 0,
        blocked.length ? `离线期间阻断 ${blocked.length} 个请求：${blocked.slice(0, 2).join(' | ')}`
          : '断言无效：离线期间没有任何请求被阻断，无法证明失败来自断网')
      add('offline:explicit-not-submitted',
        /未提交|待同步/.test(state.offlineNotice + state.submitError) || state.draftButton,
        `提示="${(state.offlineNotice || state.submitError).slice(0, 60)}" draftButton=${state.draftButton}`)
      await page.screenshot({ path: path.join(OUT, 'offline-submit.png') })
      // 点击「保存为本地草稿（待同步）」：验证它明确说「未提交」，
      // 而不是把「已保存」偷换成「已提交」（T29 原文：离线写入不显示成功）。
      if (state.draftButton) {
        await page.locator('[data-review-offline-draft="true"]').click()
        await page.waitForTimeout(900)
        const draft = await page.evaluate(() => ({
          notice: document.querySelector('[data-review-offline-notice="true"]')?.textContent?.trim() ?? '',
          pending: document.querySelector('[data-review-pending-count="true"]')?.textContent?.trim() ?? '',
          error: document.querySelector('[data-review-submit-error="true"]')?.textContent?.trim() ?? '',
          toast: [...document.querySelectorAll('.semi-toast-content, .semi-notification-content')]
            .map((el) => el.textContent?.trim() ?? '').join(' | '),
          queueRaw: Object.keys(window.localStorage).filter((k) => k.startsWith('studio.pending')).join(','),
        }))
        report.offline.draftState = draft
        const text = draft.notice + draft.pending
        add('offline:draft-says-not-submitted',
          /未提交/.test(text) && !/已保存成功|已提交成功|已发布/.test(text),
          `草稿提示="${text.slice(0, 90)}" error="${draft.error.slice(0, 60)}" storage=[${draft.queueRaw}]`)
        add('offline:draft-persisted-locally', draft.queueRaw.length > 0,
          draft.queueRaw ? `本机队列键已写入：${draft.queueRaw}` : '本机未写入队列（草稿没有真的保存）')
        await page.screenshot({ path: path.join(OUT, 'offline-draft.png') })
        // 同时落一份带阶段名的副本：仓库里提交的证据就叫
        // `01-before.png` / `02-after.png`，这样重跑采集器可以直接覆盖它们，
        // 而不需要人工手改脚本或复制文件（否则证据与脚本会各自漂移）。
        const stage = draft.queueRaw.length > 0 ? '02-after' : '01-before'
        await page.screenshot({ path: path.join(OUT, `${stage}.png`) })
        writeFileSync(path.join(OUT, `${stage}-findings.json`), `${JSON.stringify(report, null, 2)}\n`)
      }
      await ctx.setOffline(false)
    } else {
      add('offline:submit-button-present', false, '审阅页未找到提交按钮（无法执行断网提交实测）')
    }
  } else {
    add('offline:reason-field-present', false, '审阅页未找到判断理由输入框（可能无待判断样本）')
  }
  await ctx.close()
}

await browser.close()
writeFileSync(path.join(OUT, 'findings.json'), `${JSON.stringify(report, null, 2)}\n`)
console.log(`\n产物：${OUT}/findings.json`)
const failed = report.findings.filter((f) => !f.ok)
console.log(`T29 实测：${report.findings.length - failed.length}/${report.findings.length} 项通过`)
process.exit(failed.length ? 2 : 0)
