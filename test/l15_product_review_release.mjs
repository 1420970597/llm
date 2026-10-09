/**
 * 产品主线重构回归：审阅专注模式、保存后推进、三步发布与不可变下载。
 * Docker Node 中运行默认源码守卫；加 --with-browser 对真实页面执行操作。
 * 浏览器依赖由 PLAYWRIGHT_PATH 指向既有安装，不新增仓库依赖。
 * PRODUCT_UI_BASE_URL 指向 Vite preview；所有业务请求被隔离 mock，不改远程数据。
 */
import assert from 'node:assert/strict'
import { mkdirSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const review = readFileSync(path.join(root, 'apps/web-user/src/studio/pages/ReviewPages.tsx'), 'utf8')
const release = readFileSync(path.join(root, 'apps/web-user/src/studio/pages/ReleasePages.tsx'), 'utf8')
const passed = []
function check(name, condition) {
  assert.ok(condition, name)
  passed.push(name)
  console.log(`[PASS] ${name}`)
}

check('默认专注内容且判断区常驻', /focusContent, setFocusContent\] = useState\(true\)/.test(review) && /data-review-decision-panel="true"/.test(review))
check('接纳与隔离是显式按钮而非下拉菜单', /data-review-action="accepted"/.test(review) && /data-review-action="quarantined"/.test(review))
check('保存成功后推进且冲突留在当前样本', /autoNext && !result\.projection\.conflict/.test(review) && /goRelative\('next', true, nextBeforeSave\)/.test(review))
check('dirty 与正在保存的导航受到保护', /!draftDirty \|\| window\.confirm/.test(review) && /if \(submittingRef\.current\) return false/.test(review))
check('BrowserRouter Back / Forward 在 capture 阶段保护草稿', /addEventListener\('popstate', guardHistoryNavigation, true\)/.test(review) && /history\.pushState\(entryState, '', entryURL\)/.test(review))
check('快捷键不抢输入、组合键与 Shift', /target\.isContentEditable/.test(review) && /event\.shiftKey/.test(review))
check('评论、证据历史与离线草稿仍可访问', /<CommentPanel/.test(review) && /data-review-decision-history/.test(review) && /review_decision_draft/.test(review))
check('owner 协调使用独立端点并指向真实判断记录', /studioApi\.resolveConflict/.test(review) && /supersedes: coordinatedDecisionID/.test(review) && /canDecide = capabilities\.canReview && \(effective !== 'conflict' \|\| canCoordinate\)/.test(review))
check('数据列表默认明确查询全量而非服务端待审缺省', /showAllStatuses = reviewStatus === ''/.test(review) && /if \(showAllStatuses\) params\.set\('status', 'all'\)/.test(review))
check('发布采用范围、格式映射、确认三个实际步骤', /stepLabels = \['选择范围', '格式与映射', '检查确认'\]/.test(release) && /step === 0/.test(release) && /step === 1/.test(release) && /step === 2/.test(release))
check('映射未保存禁止继续且服务端字段错误跳回对应步骤', /mappingState\.dirty/.test(release) && /setStep\(firstField === 'range'/.test(release))
check('快照检查项目、用途与完整明细', /snapshot\?\.projectId !== numericProjectId/.test(release) && /snapshot\?\.purpose !== 'release'/.test(release) && /snapshot\.itemCount !== resolved\.count/.test(release))
check('全范围使用服务端冻结而非前端伪全选', /freezeAcceptedFilter/.test(release) && /fromFilter: \{ reviewStatus: 'accepted'/.test(release))
check('相同候选请求复用幂等键且同步防止重复提交', /fingerprint !== fingerprint/.test(release) && /idempotencyKey: command\.current\.key/.test(release) && /if \(commandBusy\.current\) return/.test(release))
check('权限读取失败与真实无权限是不同状态并可重试', /data-release-permissions-error/.test(release) && /!permissionsError && projectCapabilities/.test(release))
check('阻塞直链、验证后下载与清单折叠保留', /<BlockerLink/.test(release) && /artifact\.state === 'verified' && capabilities\.canDownload/.test(release) && /<details[^>]*data-manifest-panel="true"/.test(release))

// 变异自证：源码守卫确实能发现关闭自动推进及丢掉冲突保护的退化。
check('自动推进变异自证', !/goRelative\('next', true, nextBeforeSave\)/.test(review.replace("await goRelative('next', true, nextBeforeSave)", '/* removed */')))
check('冲突保护变异自证', !/autoNext && !result\.projection\.conflict/.test(review.replace('autoNext && !result.projection.conflict', 'autoNext')))

if (process.argv.includes('--with-browser')) {
  const require = createRequire(import.meta.url)
  const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright')
  const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined, args: ['--no-sandbox'] })
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
  const page = await context.newPage()
  page.setDefaultTimeout(10000)
  const base = process.env.PRODUCT_UI_BASE_URL || 'http://127.0.0.1:13310'
  const output = path.join(root, 'output/playwright/product-flow')
  mkdirSync(output, { recursive: true })
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const states = { s_1: 'pending', s_2: 'pending' }
  const postKeys = []
  const freezeRequests = []
  const rangeQueries = []
  let decisionMode = 'success'
  let releasePosts = 0
  let canReview = true
  let owner = true
  let overviewFailure = false
  let resolutionMode = '409'
  let resolutionCalls = 0
  const conflicts = new Set()
  const sample = (id) => ({ sampleId: Number(id.slice(2)), resourceId: id, sampleKey: id, title: id === 's_1' ? '样本一' : '样本二', targetKind: 'sft', latestVersion: 1, latestVersionId: 100 + Number(id.slice(2)), reviewStatus: states[id], aggregateReviewRevision: 0, reviewConflict: false, capabilities: { canReview, canViewHistory: true } })
  const projection = (id, conflict = conflicts.has(id)) => ({ sampleVersionId: 100 + Number(id.slice(2)), evidenceRevision: 1, decisionCount: states[id] === 'pending' ? 0 : 1, aggregateReviewRevision: 1, effectiveAction: conflict ? 'conflict' : states[id], conflict })
  const envelope = (data, capabilities = {}) => ({ data, capabilities, links: {}, warnings: [], id: 'test', revision: 1 })
  const mapping = { id: 7, documentId: 1, projectId: 1, version: 1, kind: 'mapping', schemaVersion: 'mapping.v1', contentHash: 'mapping-1234', createdAt: '2026-10-09', changeReason: '训练映射', payload: { schemaVersion: 'mapping.v1', format: 'jsonl', fields: [{ targetField: 'question', sourceField: 'question', required: true }] } }
  const releaseRecord = (id, published = false) => ({ id, projectId: 1, releaseName: 'v1.0', status: published ? 'published' : 'blocked', intendedUse: 'SFT 训练', format: 'jsonl', targetKind: 'sft', limitations: [], candidateRevision: 1, mappingVersionId: 7, createdAt: '2026-10-09', updatedAt: '2026-10-09' })
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const pathname = url.pathname.replace(/^\/api/, '')
    const send = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) })
    if (pathname === '/v1/auth/me') return send({ user: { id: 1, email: 'review@example.com', role: 'admin' } })
    if (/\/(datasets|providers|storage-profiles|strategies|prompts|audit-logs)$/.test(pathname)) return send([])
    if (pathname === '/v1/projects/1') return send(envelope({ name: '产品流程验收' }))
    if (pathname.endsWith('/overview')) return overviewFailure ? send({ error: { message: '项目权限读取暂时失败' } }, 503) : send(envelope({ targetKind: 'sft', versions: { mapping: { versionId: 7 } } }, { canPublish: owner, canReview: true }))
    if (pathname.endsWith('/mapping-versions/1')) return send(envelope(mapping))
    if (pathname.endsWith('/mapping-versions')) return send({ items: [mapping], document: { revision: 1 }, canEdit: true })
    if (pathname.endsWith('/batches')) return send({ items: [], nextCursor: '' })
    if (pathname.endsWith('/samples')) {
      const status = url.searchParams.get('status') || 'pending'
      if (status === 'accepted') rangeQueries.push(Object.fromEntries(url.searchParams))
      return send({ items: ['s_1', 's_2'].filter((id) => status === 'all' || states[id] === status).map(sample), nextCursor: '' })
    }
    const detailMatch = pathname.match(/\/samples\/(s_\d+)$/)
    if (detailMatch) {
      const id = detailMatch[1]
      return send(envelope({ sample: sample(id), version: { version: 1, versionId: sample(id).latestVersionId, contentHash: '0123456789abcdef', payload: { question: '如何判断训练数据质量？', reasoning: '检查内容并依据证据判断。', answer: '接纳满足标准的数据，隔离不合格的数据。' }, source: { blueprintContentHash: 'abcdef', sourceChunkIds: [] } } }, { canReview, canViewHistory: true }))
    }
    const decisionsMatch = pathname.match(/\/samples\/(s_\d+)\/versions\/1\/decisions$/)
    if (decisionsMatch) {
      const id = decisionsMatch[1]
      if (request.method() === 'GET') return send({ items: conflicts.has(id) ? [{ id: 9, action: 'accepted', reason: '判断依据', reviewerId: 2, reviewerRevision: 1 }] : [], projection: projection(id) })
      if (decisionMode === 'offline') return route.abort('failed')
      if (decisionMode === '409') return send({ message: '判断版本已过期，请重新加载后比较' }, 409)
      const body = request.postDataJSON()
      states[id] = body.action
      if (decisionMode === 'conflict') conflicts.add(id)
      return send(envelope({ decision: { id: 1 }, projection: projection(id, decisionMode === 'conflict'), blockers: [] }), 201)
    }
    const resolutionMatch = pathname.match(/\/samples\/(s_\d+)\/versions\/1\/resolve-conflict$/)
    if (resolutionMatch) {
      resolutionCalls += 1
      assert.equal(request.postDataJSON().supersedes, 9)
      if (resolutionMode === '409') return send({ error: { message: '冲突判断已更新，请重新核对' } }, 409)
      const id = resolutionMatch[1]
      states[id] = request.postDataJSON().action
      conflicts.delete(id)
      return send(envelope({ decision: { id: 10 }, projection: projection(id), blockers: [] }), 201)
    }
    if (pathname.endsWith('/selection-snapshots/99')) return send({ snapshot: { id: 99, projectId: 1, purpose: 'release', itemCount: 2 }, items: [101, 102], count: 2, composition: { accepted: 2, pending: 0, quarantined: 0, conflict: 0 } })
    if (pathname.endsWith('/selection-snapshots/98')) return send({ snapshot: { id: 98, projectId: 2, purpose: 'release', itemCount: 2 }, items: [101, 102], count: 2 })
    if (pathname.endsWith('/selection-snapshots') && request.method() === 'POST') {
      freezeRequests.push(request.postDataJSON())
      return send(envelope({ id: 99, projectId: 1, purpose: 'release', itemCount: 2 }), 201)
    }
    if (pathname.endsWith('/releases') && request.method() === 'POST') {
      postKeys.push(request.headers()['idempotency-key'])
      releasePosts += 1
      if (releasePosts === 1) return send({ message: '构建服务暂时不可用，请重试' }, 503)
      return send(envelope({ release: releaseRecord(31), blockers: [] }), 201)
    }
    if (/\/releases\/3[12]$/.test(pathname)) {
      const id = Number(pathname.split('/').at(-1))
      const published = id === 32
      return send(envelope({ release: releaseRecord(id, published), blockers: published ? [] : [{ code: 'review_pending', message: '样本一需要审阅', link: '/p/1/data/s_1' }], manifest: { items: [101, 102] }, manifestHash: 'manifest-1234', artifacts: published ? [{ id: 8, format: 'jsonl', state: 'verified', artifactHash: 'artifact-1234', sizeBytes: 2048, encoderVersion: 'v1' }] : [] }, { canPublish: true, canDownload: true, canCreateNext: true }))
    }
    if (pathname.endsWith('/legacy/migration-status')) return send({ migrationComplete: false, legacyDatasets: 0, pendingDatasets: 0 })
    return send({ items: [], data: {}, capabilities: {} })
  })
  const checkProjectShell = async (screen) => {
    await page.locator('.atelier-project-header').filter({ hasText: '产品流程验收' }).waitFor()
    check(`浏览器：${screen}项目标题与阶段壳存在`, await page.locator('.atelier-project-header').isVisible()
      && (await page.locator('.atelier-project-header').innerText()).includes('产品流程验收')
      && await page.locator('.atelier-project-tabs').isVisible()
      && await page.locator('.atelier-project-tabs [data-workflow-stage]').count() === 5)
    check(`浏览器：${screen}没有水平溢出`, await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth))
  }
  const capture = async (name) => {
    await page.evaluate(() => {
      window.scrollTo(0, 0)
      document.querySelectorAll('*').forEach((element) => { if (element.scrollTop > 0) element.scrollTop = 0 })
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    })
    await page.waitForTimeout(100)
    await page.screenshot({ path: path.join(output, name), fullPage: true })
  }
  try {
    states.s_2 = 'accepted'
    await page.goto(`${base}/p/1/data`, { waitUntil: 'networkidle' })
    await page.locator('[data-sample-id="s_2"]').waitFor()
    check('浏览器：数据默认包含已接纳与待审样本', await page.locator('[data-sample-id]').count() === 2)
    states.s_2 = 'pending'
    await page.goto(`${base}/p/1/review?status=pending&q=样本`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: '开始审阅', exact: true }).click()
    await page.waitForURL(/\/data\/s_1/)
    check('浏览器：队列入口直接开始第一条', /\/data\/s_1/.test(page.url()))
    check('浏览器：默认隐藏辅助区，常驻判断区', await page.locator('[data-review-decision-panel]').isVisible() && await page.locator('[data-review-queue]').count() === 0)
    await page.locator('#review-reason').fill('尚未保存的理由')
    await page.locator('#review-reason').press('j')
    check('浏览器：输入理由时 J 不触发导航', /\/s_1/.test(page.url()) && (await page.locator('#review-reason').inputValue()).endsWith('j'))
    await page.locator('#review-reason').fill('尚未保存的理由')
    page.once('dialog', (dialog) => dialog.dismiss())
    await page.getByRole('button', { name: '下一条', exact: true }).click()
    check('浏览器：dirty 取消导航保留理由', /\/s_1/.test(page.url()) && await page.locator('#review-reason').inputValue() === '尚未保存的理由')
    page.once('dialog', (dialog) => dialog.dismiss())
    await page.locator('.project-task-navigation a').first().click()
    check('浏览器：dirty 取消全局链接仍保留理由', /\/s_1/.test(page.url()) && await page.locator('#review-reason').inputValue() === '尚未保存的理由')
    await page.evaluate(() => {
      const state = window.history.state || {}
      window.history.pushState({ ...state, idx: (state.idx ?? 0) + 1 }, '', window.location.href)
    })
    const guardedHistory = await page.evaluate(() => ({ index: window.history.state?.idx, url: window.location.href }))
    const backDialog = page.waitForEvent('dialog', { timeout: 1500 }).catch(() => null)
    await page.evaluate(() => window.history.back())
    const dialog = await backDialog
    if (dialog) await dialog.dismiss()
    await page.waitForTimeout(250)
    check('浏览器：Back 取消保留 URL、当前样本与理由', /\/data\/s_1/.test(page.url()) && await page.locator('#review-reason').inputValue() === '尚未保存的理由')
    decisionMode = '409'
    await page.locator('[data-review-save]').click()
    await page.locator('[data-review-submit-error]').waitFor()
    check('浏览器：409 不推进且保留输入', /\/s_1/.test(page.url()) && await page.locator('#review-reason').inputValue() === '尚未保存的理由')
    decisionMode = 'offline'
    await page.locator('[data-review-save]').click()
    await page.locator('[data-review-offline-draft]').waitFor()
    check('浏览器：离线有草稿退路且不伪装成功', /\/s_1/.test(page.url()) && await page.locator('#review-reason').inputValue() !== '')
    decisionMode = 'success'
    await page.locator('[data-review-save]').click()
    await page.waitForURL(/\/data\/s_2/, { timeout: 10000 })
    check('浏览器：成功自动下一条并保留筛选', /\/data\/s_2/.test(page.url()) && new URL(page.url()).searchParams.get('status') === 'pending' && new URL(page.url()).searchParams.get('q') === '样本')
    await page.getByRole('button', { name: '队列与证据', exact: true }).click()
    check('浏览器：队列、证据、判断历史可展开', await page.locator('[data-review-queue]').isVisible() && await page.locator('[data-review-evidence-links]').isVisible() && await page.locator('[data-review-decision-history] summary').isVisible())
    await page.getByRole('button', { name: '收起辅助信息', exact: true }).click()
    await checkProjectShell('桌面审阅')
    await capture('review-desktop.png')
    await page.setViewportSize({ width: 390, height: 844 })
    await checkProjectShell('390px审阅')
    await capture('review-mobile.png')
    check('浏览器：移动审阅没有水平溢出', await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth))
    await page.setViewportSize({ width: 1440, height: 960 })
    decisionMode = 'conflict'
    await page.locator('#review-reason').fill('出现相反意见')
    await page.locator('[data-review-save]').click()
    await page.getByText('判断已保存，但存在相反判断。请先处理冲突。', { exact: true }).waitFor()
    check('浏览器：聚合冲突保存后不推进', /\/data\/s_2/.test(page.url()))
    await page.locator('#review-reason').fill('负责人复核，推理链符合标准')
    await page.locator('[data-review-coordinate]').click()
    await page.locator('[data-review-submit-error]').waitFor()
    check('浏览器：owner 协调 409 保留理由与样本', /\/data\/s_2/.test(page.url()) && await page.locator('#review-reason').inputValue() === '负责人复核，推理链符合标准')
    resolutionMode = 'success'
    await page.locator('[data-review-coordinate]').click()
    await page.getByText('冲突已协调', { exact: true }).waitFor()
    check('浏览器：owner 协调成功使用专用端点解除冲突', resolutionCalls === 2 && !conflicts.has('s_2'))
    owner = false; conflicts.add('s_2')
    await page.goto(`${base}/p/1/data/s_2`, { waitUntil: 'networkidle' })
    await page.getByText('请联系项目负责人处理冲突。', { exact: true }).waitFor()
    check('浏览器：reviewer 不可协调冲突', await page.locator('[data-review-coordinate]').count() === 0 && await page.getByText('请联系项目负责人处理冲突。', { exact: true }).isVisible())
    owner = true; conflicts.delete('s_2')
    canReview = false
    await page.goto(`${base}/p/1/data/s_2`, { waitUntil: 'networkidle' })
    await page.locator('[data-capability-readonly]').waitFor()
    check('浏览器：无权限只读且没有保存按钮', await page.locator('[data-capability-readonly]').isVisible() && await page.locator('[data-review-save]').count() === 0)
    canReview = true
    states.s_1 = 'accepted'; states.s_2 = 'accepted'
    overviewFailure = true
    await page.goto(`${base}/p/1/releases/new`, { waitUntil: 'networkidle' })
    await page.locator('[data-release-permissions-error]').waitFor()
    check('浏览器：权限网络错误不是无权限且有重试', await page.locator('[data-release-permissions-error]').isVisible() && await page.getByText('只读：当前账号没有发布权限。', { exact: true }).count() === 0)
    overviewFailure = false
    await page.getByRole('button', { name: '重试权限读取', exact: true }).click()
    await page.locator('[data-release-permissions-error]').waitFor({ state: 'hidden' })
    await page.goto(`${base}/p/1/releases/new?selection=98`, { waitUntil: 'networkidle' })
    await page.locator('[data-release-next]').click()
    check('浏览器：跨项目快照阻止进入下一步', await page.locator('[data-release-step]').getAttribute('data-release-step') === '1' && await page.locator('[data-range-error]').isVisible())
    await page.goto(`${base}/p/1/releases/new`, { waitUntil: 'networkidle' })
    await page.getByLabel('搜索发布范围', { exact: true }).fill('样本')
    await page.locator('[data-release-freeze-filter]').click()
    await page.waitForURL(/selection=99/)
    await page.locator('[data-selection-restored]').waitFor()
    check('浏览器：筛选范围由服务端冻结而非只取首屏', freezeRequests.length === 1 && freezeRequests[0].fromFilter.reviewStatus === 'accepted' && freezeRequests[0].fromFilter.search === '样本' && rangeQueries.some((query) => query.q === '样本'))
    await page.locator('[data-release-next]').click()
    check('浏览器：范围进入格式映射步骤', await page.locator('[data-release-step]').getAttribute('data-release-step') === '2')
    await page.locator('[data-mapping-editor] > summary').click()
    await page.locator('.document-editor--mapping input[aria-label="内部字段"]').fill('question_changed')
    await page.locator('[data-release-next]').click()
    check('浏览器：未保存映射阻止下一步', await page.locator('[data-release-step]').getAttribute('data-release-step') === '2' && await page.locator('#mapping-version-error').isVisible())
    await page.locator('.document-editor--mapping input[aria-label="内部字段"]').fill('question')
    await page.locator('[data-release-next]').click()
    check('浏览器：映射进入确认步骤', await page.locator('[data-release-step]').getAttribute('data-release-step') === '3' && await page.locator('[data-release-confirmation]').isVisible())
    await page.locator('#intended-use').fill('SFT 训练')
    await checkProjectShell('桌面发布确认')
    await capture('release-confirm-desktop.png')
    await page.setViewportSize({ width: 390, height: 844 })
    await checkProjectShell('390px发布确认')
    await capture('release-confirm-mobile.png')
    await page.setViewportSize({ width: 1440, height: 960 })
    await page.locator('[data-release-create]').click()
    await page.locator('[data-release-error]').waitFor()
    await page.locator('[data-release-create]').click()
    await page.waitForURL(/\/releases\/31$/)
    check('浏览器：失败重试复用同一个候选幂等键', postKeys.length === 2 && Boolean(postKeys[0]) && postKeys[0] === postKeys[1])
    check('浏览器：阻塞项前置直达具体样本并禁发', await page.locator('[data-release-blockers] a').getAttribute('href') === '/p/1/data/s_1' && await page.locator('[data-release-publish]').isDisabled())
    await page.goto(`${base}/p/1/releases/32`, { waitUntil: 'networkidle' })
    check('浏览器：已验证制品下载前置且清单默认折叠', await page.locator('[data-release-files] a').isVisible() && !(await page.locator('[data-manifest-panel]').evaluate((element) => element.open)))
    await checkProjectShell('桌面发布详情')
    await capture('release-desktop.png')
    await page.setViewportSize({ width: 390, height: 844 })
    await checkProjectShell('390px发布详情')
    await capture('release-mobile.png')
    check('浏览器：移动发布没有水平溢出', await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth))
    check('浏览器：无 React 运行错误', pageErrors.length === 0)
  } catch (error) {
    console.error('Browser failure context:', page.url(), (await page.locator('body').innerText()).slice(0, 1800), pageErrors)
    await page.screenshot({ path: path.join(output, 'review-release-failure.png'), fullPage: true })
    throw error
  } finally { await browser.close() }
}
console.log(`Review / release product regression: ${passed.length} checks passed.`)
