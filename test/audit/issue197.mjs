/**
 * Issue #197「用户测试问题汇总」17 条问题的逐条取证采集器。
 *
 * 为什么单独写一个而不是复用 capture.mjs：capture.mjs 采集的是「全路由长什么样」，
 * 而 #197 的 17 条是对**具体控件与具体 DOM 事实**的质疑（有没有分页按钮、
 * 连接行有没有编辑按钮、质量页用的是"分子/分母"还是"被评测数据集"…）。
 * 因此这里每一条都要同时产出：①全页截图 ②对该条主张的结构化 DOM 统计。
 *
 * 产物：docs/audit/issue-197/<n>-<key>.png 与 docs/audit/issue-197/evidence.json
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const OUT_DIR = path.join(REPO_ROOT, 'docs/audit/issue-197')
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT = process.env.PROJECT_ID ?? 'p_1'

mkdirSync(OUT_DIR, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

page.on('console', (m) => { if (m.type() === 'error') console.log('  [console]', m.text().slice(0, 200)) })
page.on('pageerror', (e) => console.log('  [pageerror]', String(e).slice(0, 200)))

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1500)

const findings = []

/**
 * 采集一个页面：截图 + 执行 probe 收集该条问题的 DOM 事实。
 * probe 在浏览器上下文里运行，返回可序列化对象。
 */
async function shot(key, routePath, probe, opts = {}) {
  const url = `${BASE}${routePath}`
  const record = { key, routePath, url, ok: true }
  try {
    await page.goto(url, { waitUntil: 'networkidle', timeout: 40000 })
  } catch (error) {
    record.ok = false
    record.navError = String(error).slice(0, 200)
  }
  await page.waitForTimeout(opts.settle ?? 2000)

  const file = path.join(OUT_DIR, `${key}.png`)
  await page.screenshot({ path: file, fullPage: opts.fullPage !== false })
  record.screenshot = path.relative(REPO_ROOT, file)

  // 截图前必须解除内部滚动容器的固定高度：
  // 布局是 `body(100vh) > main.app-layout__content(overflow:auto)`，因此
  // Playwright 的 fullPage 只能截到视口高度（1000px），长页面会被截断。
  // 这里把滚动容器临时展开成自然高度，截完再还原 —— 否则「页面超长」这类问题
  // （#197 第 4 条）在证据里会**消失**，而那是错误的证据。
  record.layout = await page.evaluate(() => {
    const scroller = document.querySelector('main.app-layout__content')
    if (!scroller) return { expanded: false }
    const naturalHeight = scroller.scrollHeight
    // 只改 main 不够：外层 .app-layout 与 html/body 仍被锁在 100vh，
    // Chromium 的 fullPage 会按最外层算，于是只能截到 1000px。三层一起放开。
    for (const el of [document.documentElement, document.body, scroller.parentElement, scroller]) {
      if (!el) continue
      el.style.height = 'auto'
      el.style.maxHeight = 'none'
      el.style.overflow = 'visible'
    }
    return { expanded: true, naturalHeight, viewportHeight: window.innerHeight }
  })
  await page.waitForTimeout(300)

  record.page = await page.evaluate(() => {
    const visible = (el) => {
      if (!el) return false
      const r = el.getBoundingClientRect()
      const s = getComputedStyle(el)
      return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'
    }
    const buttons = [...document.querySelectorAll('button, a[role="button"], [role="button"]')]
      .filter(visible).map((b) => (b.innerText || b.textContent || '').trim()).filter(Boolean)
    return {
      url: location.pathname + location.search,
      docHeight: document.documentElement.scrollHeight,
      viewportHeight: window.innerHeight,
      inputs: document.querySelectorAll('input, select, textarea').length,
      visibleButtons: [...new Set(buttons)],
      headings: [...document.querySelectorAll('h1,h2,h3,h4,h5')].filter(visible)
        .map((h) => (h.innerText || '').trim()).filter(Boolean).slice(0, 25),
      bodyText: (document.body.innerText || '').replace(/\n{2,}/g, '\n').slice(0, 6000),
    }
  })

  if (probe) {
    try {
      record.probe = await page.evaluate(probe)
    } catch (error) {
      record.probeError = String(error).slice(0, 300)
    }
  }
  findings.push(record)
  console.log(`[shot] ${key} -> ${record.screenshot} (docHeight=${record.page.docHeight})`)
  return record
}

/* ------------------------------------------------------------------ */
/* 逐条问题定义（截图 + 事实探针）                                      */
/* ------------------------------------------------------------------ */

// 1 / 8 / 15 / 17：控件级事实
await shot('01-projects-list', '/projects', () => {
  const text = document.body.innerText
  const rows = document.querySelectorAll('[data-project-id], .project-card, .project-row').length
  const buttons = [...document.querySelectorAll('button')].map((b) => b.innerText.trim())
  return {
    // 「有没有分页」的判定：页面上是否出现任何分页控件/翻页文案
    paginationControls: [...document.querySelectorAll('[class*=pagination], [class*=Pagination], nav[aria-label*=页], nav[aria-label*=分页]')].length,
    loadMoreButton: buttons.filter((b) => /加载更多|下一页|上一页|第 \d+ 页/.test(b)),
    mentionsPageSize: /每页|共 \d+ 条|第 \d+ 页|总 \d+ 条/.test(text),
    tableRows: rows,
    allButtons: buttons,
  }
})

await shot('01b-projects-pagination-dom', '/projects', () => {
  // 找出「列表容器下方」是否有任何分页元素（哪怕不可见）
  const list = document.querySelector('[data-studio-page="projects"]')
  const paginationNodes = list ? list.querySelectorAll('[class*=pagination], [class*=Pagination], [class*=Pager]') : []
  return {
    hasListRoot: Boolean(list),
    paginationNodeCount: paginationNodes.length,
    listTextAfterLastRow: list ? (list.innerText || '').slice(-260) : '',
  }
})

await shot('02-blueprint-standard-node', `/p/${PROJECT}/blueprint?node=standard`, () => {
  const label = [...document.querySelectorAll('label')].map((l) => l.innerText.trim())
  const textareas = [...document.querySelectorAll('textarea')].map((t) => ({
    value: (t.value || '').slice(0, 240),
    startsWithBrace: (t.value || '').trim().startsWith('{') || (t.value || '').trim().startsWith('['),
  }))
  return {
    fieldLabels: label,
    jsonLikeTextareas: textareas,
    jsonFieldMarkers: [...document.querySelectorAll('[data-kind="json"]')].map((e) => e.getAttribute('data-field')),
    bodyHasBrace: /\{\s*"/.test(document.body.innerText),
  }
})

await shot('03-data-no-preview', `/p/${PROJECT}/data`, () => {
  const buttons = [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean)
  return {
    previewButtons: buttons.filter((b) => /预览|查看内容|展开|详情/.test(b)),
    tableRows: document.querySelectorAll('[data-sample-id]').length,
    bodyText: document.body.innerText.slice(0, 900),
  }
})

await shot('04-blueprint-long-page', `/p/${PROJECT}/blueprint`, () => {
  // 位置必须相对**真实内容容器**测量，而不是 window：
  // 布局里滚动的是 main.app-layout__content，window.scrollY 永远是 0，
  // 用它算出来的「首屏外」结论是假的。
  const scroller = document.querySelector('main.app-layout__content')
  const contentTop = scroller ? scroller.getBoundingClientRect().top : 0
  const offsetIn = (el) => Math.round(el.getBoundingClientRect().top - contentTop)
  const measure = (selector) => {
    const el = document.querySelector(selector)
    if (!el) return null
    return { top: offsetIn(el), height: Math.round(el.getBoundingClientRect().height) }
  }
  const sections = [...document.querySelectorAll('[data-studio-page="blueprint"] > *')].map((el) => ({
    tag: el.tagName.toLowerCase(),
    cls: (el.className || '').toString().slice(0, 80),
    top: offsetIn(el),
    height: Math.round(el.getBoundingClientRect().height),
  }))
  const firstControl = document.querySelector('.blueprint-inspector input, .blueprint-inspector textarea, .blueprint-inspector .semi-select')
  return {
    naturalContentHeight: scroller ? scroller.scrollHeight : document.documentElement.scrollHeight,
    viewportHeight: window.innerHeight,
    screensTall: scroller ? +(scroller.scrollHeight / window.innerHeight).toFixed(2) : 1,
    sections,
    canvas: measure('.blueprint-canvas'),
    inspector: measure('.blueprint-inspector'),
    history: measure('.blueprint-history'),
    firstControlTop: firstControl ? offsetIn(firstControl) : null,
    // 右侧检查器里控件是否落在首屏之外（> viewportHeight 即需要滚动才看得到）
    firstControlBelowFold: firstControl ? offsetIn(firstControl) > window.innerHeight : null,
  }
})

await shot('05-blueprint-evaluation-node', `/p/${PROJECT}/blueprint?node=evaluation`, () => {
  const inspector = document.querySelector('.blueprint-inspector')
  return {
    inspectorText: inspector ? inspector.innerText.slice(0, 1800) : '',
    fieldLabels: [...document.querySelectorAll('.blueprint-inspector label')].map((l) => l.innerText.trim()),
    selectCount: document.querySelectorAll('.blueprint-inspector .semi-select').length,
    inputCount: document.querySelectorAll('.blueprint-inspector input').length,
  }
})

await shot('06-blueprint-version-coupling', `/p/${PROJECT}/blueprint?node=generation`, () => {
  const inspector = document.querySelector('.blueprint-inspector')
  const text = inspector ? inspector.innerText : ''
  return {
    requiresChangeReason: /变更理由/.test(text),
    changeReasonRequiredHint: /必填/.test(text),
    hasVersionSelector: document.querySelectorAll('.blueprint-inspector .semi-select').length,
    hasSaveBar: Boolean(document.querySelector('.blueprint-save-bar')),
    saveBarText: document.querySelector('.blueprint-save-bar')?.innerText ?? '',
    historyBlock: Boolean(document.querySelector('.blueprint-history')),
    bodyMentionsVersion: (document.body.innerText.match(/版本/g) || []).length,
  }
})

await shot('07-connections-entry', '/settings/connections', () => {
  const rows = document.querySelectorAll('[data-connection-id]')
  const rowButtons = [...rows].flatMap((r) => [...r.querySelectorAll('button')].map((b) => b.innerText.trim()))
  const pageButtons = [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean)
  return {
    connectionRows: rows.length,
    perRowButtons: rowButtons,
    pageButtons,
    inputCount: document.querySelectorAll('input, select, textarea').length,
    adminEntry: [...document.querySelectorAll('[data-connection-admin-action], [data-connection-manage]')].map((b) => b.innerText.trim()),
    storageRows: document.querySelectorAll('[data-storage-id]').length,
  }
})

// 07 续：点击「管理模型连接」后落到哪里
await shot('07b-connections-after-click', '/settings/connections', null)
{
  const before = page.url()
  const adminButton = page.locator('[data-connection-admin-action], [data-connection-manage]').first()
  let clicked = null
  let afterUrl = ''
  if (await adminButton.count() > 0) {
    clicked = (await adminButton.innerText()).trim()
    await adminButton.click()
    await page.waitForTimeout(3000)
    afterUrl = page.url()
    await page.screenshot({ path: path.join(OUT_DIR, '07b-connections-after-click.png'), fullPage: true })
    findings[findings.length - 1].probe = {
      clickedButton: clicked,
      urlBefore: before,
      urlAfter: afterUrl,
      isLegacyConsole: /\/console\//.test(afterUrl),
      afterBodyText: (await page.locator('body').innerText()).slice(0, 1200),
    }
    console.log(`[shot] 07b click -> ${afterUrl}`)
  } else {
    findings[findings.length - 1].probe = { clickedButton: null, note: '连接设置页没有管理入口' }
  }
}

await shot('08-connections-row-edit', '/settings/connections', () => {
  const rows = [...document.querySelectorAll('[data-connection-id]')]
  return {
    rowCount: rows.length,
    rowsWithEditButton: rows.filter((r) => [...r.querySelectorAll('button')].some((b) => /编辑|修改|删除/.test(b.innerText))).length,
    rowCellTexts: rows.map((r) => r.innerText.replace(/\n/g, ' | ')),
  }
})

await shot('09a-tools-evaluation', '/tools/evaluation', () => {
  return {
    bodyText: document.body.innerText.slice(0, 2500),
    datasetSelectors: document.querySelectorAll('.semi-select').length,
    mentionsBlueprint: /蓝图/.test(document.body.innerText),
    mentionsStandard: /思维标准|标准版本/.test(document.body.innerText),
    mentionsCoverage: /覆盖/.test(document.body.innerText),
  }
})

await shot('09b-tools-cleaning', '/tools/cleaning', () => {
  return {
    bodyText: document.body.innerText.slice(0, 2500),
    mentionsBlueprint: /蓝图/.test(document.body.innerText),
    mentionsCoverage: /覆盖/.test(document.body.innerText),
  }
})

await shot('10-today-overview', '/today', () => {
  const text = document.body.innerText
  return {
    // 问题 10 的判定：今日工作是否给出「工作台总览」口径的聚合数字
    hasAggregateMetrics: /总样本|累计|产出总数|总数|已完成\s*\d|通过率/.test(text),
    numericTokens: (text.match(/\d+/g) || []).slice(0, 40),
    projectLabels: [...new Set((text.match(/项目\s*#?\d+/g) || []))],
    sections: [...document.querySelectorAll('h1,h2')].map((h) => h.innerText.trim()),
    mentionsPriority: /继续项目|需要你的决定|交付日历/.test(text),
    noteText: (text.match(/未读[^\n]*/) || [])[0] ?? '',
  }
})

await shot('11a-blueprint-canvas', `/p/${PROJECT}/blueprint`, () => {
  const canvas = document.querySelector('.blueprint-canvas')
  const nodes = [...document.querySelectorAll('[data-node-key]')].map((n) => ({
    key: n.getAttribute('data-node-key'),
    label: n.innerText.split('\n')[0],
    status: n.innerText.split('\n').slice(-1)[0],
  }))
  return {
    canvasWidth: canvas ? Math.round(canvas.getBoundingClientRect().width) : null,
    canvasHeight: canvas ? Math.round(canvas.getBoundingClientRect().height) : null,
    canvasIsFlexColumn: canvas ? getComputedStyle(canvas.querySelector('.blueprint-nodes') || canvas).flexDirection : '',
    nodes,
    isDraggable: [...document.querySelectorAll('[data-node-key]')].some((n) => n.getAttribute('draggable') === 'true'),
    isVerticalChain: canvas ? getComputedStyle(canvas.querySelector('.blueprint-nodes') || canvas).display : '',
    hasTreeStructure: /m\s*[×x*]\s*n|领域.*方向.*数量/.test(document.body.innerText),
  }
})

await shot('11b-coverage-tree', `/p/${PROJECT}/coverage`, () => {
  const text = document.body.innerText
  return {
    hasDomainEditor: Boolean(document.querySelector('.coverage-payload-editor, [data-coverage-editor]')),
    mentionsMNZ: /m\s*[×x*]\s*n|领域.{0,6}方向.{0,6}(配额|数量)/.test(text),
    text: text.slice(0, 2200),
    treeishNodes: document.querySelectorAll('[class*=coverage-]').length,
  }
})

await shot('12a-project-data', `/p/${PROJECT}/data`, () => {
  const page = document.querySelector('[data-studio-page]')
  return {
    pageKey: page?.getAttribute('data-studio-page'),
    title: document.querySelector('h4, h1, h2')?.innerText ?? '',
    headerText: document.querySelector('.console-page__header')?.innerText.replace(/\n/g, ' | ') ?? '',
    primaryActions: [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean),
  }
})

await shot('12b-project-review', `/p/${PROJECT}/review`, () => {
  const page = document.querySelector('[data-studio-page]')
  return {
    pageKey: page?.getAttribute('data-studio-page'),
    title: document.querySelector('h4, h1, h2')?.innerText ?? '',
    headerText: document.querySelector('.console-page__header')?.innerText.replace(/\n/g, ' | ') ?? '',
    primaryActions: [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean),
  }
})

await shot('13-runs-no-preview', `/p/${PROJECT}/runs`, () => {
  const text = document.body.innerText
  const buttons = [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean)
  return {
    hasPreview: /预览|结构|样本内容/.test(text),
    hasAutoAnalysis: /长度|字符|占比|分布|统计/.test(text),
    buttons,
    columns: [...document.querySelectorAll('.batch-row--head span')].map((s) => s.innerText.trim()),
    bodyText: text.slice(0, 1800),
  }
})

await shot('14a-quality-list', `/p/${PROJECT}/quality`, () => {
  const text = document.body.innerText
  return {
    headerText: document.querySelector('.console-page__header')?.innerText.replace(/\n/g, ' | ') ?? '',
    usesNumeratorDenominatorWords: /分子|分母/.test(text),
    numeratorDenominatorHits: (text.match(/分子|分母/g) || []).length,
    usesEvaluatedDatasetWords: /被评测数据集|被评测样本/.test(text),
    columns: [...document.querySelectorAll('.batch-row--head span')].map((s) => s.innerText.trim()),
    bodyText: text.slice(0, 2200),
  }
})

await shot('14b-quality-new', `/p/${PROJECT}/quality/new`, () => {
  const text = document.body.innerText
  const judgeField = document.querySelector('[data-field="judge-connection"]')
  return {
    judgeFieldExists: Boolean(judgeField),
    judgeFieldText: judgeField ? judgeField.innerText.replace(/\n/g, ' | ') : '',
    judgeFieldInputType: judgeField ? judgeField.querySelector('input')?.getAttribute('type') ?? 'none' : 'none',
    judgeIsSelect: judgeField ? Boolean(judgeField.querySelector('.semi-select')) : false,
    hasModelListSelect: [...document.querySelectorAll('.semi-select')].length,
    // 问题 14 第二半：能不能从模型列表里选，而不是手填 ID
    judgePlaceholder: judgeField ? (judgeField.querySelector('input')?.getAttribute('placeholder') ?? '') : '',
    bodyText: text.slice(0, 2200),
  }
})

await shot('15-team-new-user', '/settings/team', () => {
  const text = document.body.innerText
  return {
    hasCreateUserForm: /新建用户|创建用户|用户名|设置密码|初始密码/.test(text),
    emailOnlyInvite: /按邮箱添加已有账号|邀请/.test(text),
    mentionsUsername: /用户名/.test(text),
    mentionsPassword: /密码/.test(text),
    formLabels: [...document.querySelectorAll('label')].map((l) => l.innerText.trim()),
    inputs: [...document.querySelectorAll('input')].map((i) => ({ type: i.getAttribute('type'), placeholder: i.getAttribute('placeholder') })),
    bodyText: text.slice(0, 2000),
  }
})

await shot('16-legacy-history', '/legacy/history', () => {
  const text = document.body.innerText
  return {
    mentionsMigration: /迁移|导入/.test(text),
    bodyText: text.slice(0, 2600),
    buttons: [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter(Boolean),
    rowCount: document.querySelectorAll('[data-dataset-id], .legacy-history-row, table tbody tr').length,
  }
})

await shot('17a-styles-help', '/help', () => {
  const overflow = [...document.querySelectorAll('*')].filter((el) => {
    const s = getComputedStyle(el)
    return el.scrollWidth > el.clientWidth + 4 && el.clientWidth > 0 && s.overflow !== 'visible'
  }).slice(0, 25).map((el) => ({
    tag: el.tagName.toLowerCase(),
    cls: (el.className || '').toString().slice(0, 60),
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
    text: (el.innerText || '').slice(0, 60),
  }))
  const clipped = [...document.querySelectorAll('*')].filter((el) => {
    const s = getComputedStyle(el)
    return (s.overflow === 'hidden' || s.textOverflow === 'ellipsis') && el.scrollHeight > el.clientHeight + 4 && el.clientHeight > 0
  }).slice(0, 20).map((el) => ({
    tag: el.tagName.toLowerCase(),
    cls: (el.className || '').toString().slice(0, 60),
    text: (el.innerText || '').slice(0, 60),
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
  }))
  return { horizontalOverflow: overflow, verticalClipping: clipped, docWidth: document.documentElement.scrollWidth }
})

await shot('17b-styles-blueprint', `/p/${PROJECT}/blueprint`, () => {
  // 展开内部滚动容器后，所有元素都在同一次布局里，可以逐对比较真实矩形。
  const overlapping = []
  const boxes = [...document.querySelectorAll('.blueprint-related-config strong, .blueprint-related-config button, .blueprint-related-config span')]
    .filter((el) => el.getBoundingClientRect().width > 0)
    .map((el) => ({ text: (el.innerText || '').slice(0, 40), r: el.getBoundingClientRect() }))
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      const a = boxes[i].r
      const b = boxes[j].r
      const overlapX = Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left))
      const overlapY = Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top))
      if (overlapX > 2 && overlapY > 2) {
        overlapping.push({ a: boxes[i].text, b: boxes[j].text, overlapX: Math.round(overlapX), overlapY: Math.round(overlapY) })
      }
    }
  }
  const clipped = [...document.querySelectorAll('.blueprint-page *')].filter((el) => {
    const s = getComputedStyle(el)
    return (s.overflow === 'hidden' || s.textOverflow === 'ellipsis') && el.scrollWidth > el.clientWidth + 4 && el.clientWidth > 0
  }).slice(0, 25).map((el) => ({
    tag: el.tagName.toLowerCase(),
    cls: (el.className || '').toString().slice(0, 60),
    text: (el.innerText || '').slice(0, 70),
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
  }))
  return { overlapping, clipped }
})

await shot('03b-sample-review', `/p/${PROJECT}/data/s_1`, () => {
  const panes = [...document.querySelectorAll('.review-pane, [data-review-pane]')].map((el) => ({
    cls: (el.className || '').toString().slice(0, 70),
    text: (el.innerText || '').slice(0, 700),
  }))
  return {
    bodyText: document.body.innerText.slice(0, 2600),
    paneCount: panes.length,
    panes,
  }
})

await shot('03c-sample-history', `/p/${PROJECT}/data/s_1/history`, () => ({
  bodyText: document.body.innerText.slice(0, 2200),
  versionRows: document.querySelectorAll('[data-version-id], .document-history__item').length,
}))

await shot('13b-run-detail', `/p/${PROJECT}/runs/b_2`, () => {
  const text = document.body.innerText
  const scroller = document.querySelector('main.app-layout__content')
  const contentTop = scroller ? scroller.getBoundingClientRect().top : 0
  return {
    naturalHeight: scroller ? scroller.scrollHeight : document.documentElement.scrollHeight,
    screensTall: scroller ? +(scroller.scrollHeight / window.innerHeight).toFixed(2) : 1,
    // 问题 13 的判定：批次详情里有没有「数据集预览 / 长度 / 占比」这类分析
    hasPreview: /预览|样本内容|查看内容/.test(text),
    hasAnalysis: /长度|字符数|占比|分布|平均/.test(text),
    hasPlannedVsActual: /计划|完成|失败|在途/.test(text),
    mentionsDatasetStructure: /数据集结构|结构/.test(text),
    bodyText: text.slice(0, 2600),
    contentTop,
  }
})

await shot('13c-run-failures', `/p/${PROJECT}/runs/b_1/failures`, () => ({
  bodyText: document.body.innerText.slice(0, 2200),
  rows: document.querySelectorAll('[data-item-id], .failure-row').length,
}))

await shot('11c-blueprint-filled', `/p/${PROJECT}/blueprint?node=generation`, () => {
  const inspector = document.querySelector('.blueprint-inspector')
  const text = inspector ? inspector.innerText : ''
  return {
    inspectorText: text.slice(0, 1600),
    // 已配置节点是否还有「待补齐」
    stillIncomplete: /待补齐/.test(text),
    selectValues: [...document.querySelectorAll('.blueprint-inspector .semi-select-selection-text')].map((e) => e.innerText.trim()),
    inputValues: [...document.querySelectorAll('.blueprint-inspector input')].map((e) => e.value),
  }
})

await shot('05b-evaluation-node-filled', `/p/${PROJECT}/blueprint?node=evaluation`, () => {
  const inspector = document.querySelector('.blueprint-inspector')
  const text = inspector ? inspector.innerText : ''
  return {
    inspectorText: text.slice(0, 1600),
    stillIncomplete: /待补齐/.test(text),
    selectValues: [...document.querySelectorAll('.blueprint-inspector .semi-select-selection-text')].map((e) => e.innerText.trim()),
  }
})

await shot('02b-standard-steps-json', `/p/${PROJECT}/standard`, () => ({
  bodyText: document.body.innerText.slice(0, 2400),
  // 思维标准页：步骤是否被渲染成自然语言，而不是 JSON
  stepCards: document.querySelectorAll('.document-editor__section').length,
  hasJSONBraces: /\"id\"\s*:/.test(document.body.innerText),
  textareas: [...document.querySelectorAll('textarea')].map((t) => ({ value: (t.value || '').slice(0, 160), brace: (t.value || '').trim().startsWith('{') })),
}))

await shot('12c-data-with-samples', `/p/${PROJECT}/data`, () => {
  const rows = [...document.querySelectorAll('.sample-row')]
  return {
    sampleRows: document.querySelectorAll('[data-sample-id]').length,
    firstRowText: rows[1] ? rows[1].innerText.replace(/\n/g, ' | ') : '',
    // 列表本身能不能看到内容（问题 3）
    showsContentText: rows.slice(1).some((r) => (r.innerText || '').length > 80),
  }
})

/* ------------------------------------------------------------------ */
/* 附加：移动端 390px 视口（17 条里的样式问题在窄屏更明显）            */
/* ------------------------------------------------------------------ */
const mobileCtx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN', isMobile: true, hasTouch: true })
const mobile = await mobileCtx.newPage()
await mobile.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await mobile.getByPlaceholder('请输入邮箱').fill(EMAIL)
await mobile.getByPlaceholder('请输入密码').fill(PASSWORD)
await mobile.getByRole('button', { name: '进入今日工作' }).click()
await mobile.waitForTimeout(3000)
for (const [key, route] of [
  ['17c-mobile-connections', '/settings/connections'],
  ['17d-mobile-quality', `/p/${PROJECT}/quality`],
  ['17e-mobile-blueprint', `/p/${PROJECT}/blueprint`],
]) {
  await mobile.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
  await mobile.waitForTimeout(1800)
  await mobile.screenshot({ path: path.join(OUT_DIR, `${key}.png`), fullPage: true })
  console.log(`[shot] ${key} (mobile)`)
}

writeFileSync(path.join(OUT_DIR, 'evidence.json'), JSON.stringify({ base: BASE, project: PROJECT, findings }, null, 2))
console.log(`\n写入 ${OUT_DIR}/evidence.json（${findings.length} 条）`)

await browser.close()
await mobileCtx.close()
