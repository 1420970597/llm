/**
 * #217 真实 Chromium 交互验收。使用生产页面 + API 契约夹具，不替代后端集成测试。
 * Playwright CLI 的 run-code 加载本模块后调用 install(page)、verify(page)。
 * 不需要 @playwright/test，也不向共享 node_modules 新增依赖。
 */
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const chunks = Array.from({ length: 12 }, (_, index) => ({ id: index + 1, projectId: 7,
  sourceDocumentStableId: 'manual', headingPath: `手册 / 章节 ${index + 1}`, ordinal: index + 1,
  content: `冷链运输素材第 ${index + 1} 块。${'证据说明。'.repeat(12)}`, contentHash: `chunk-${index}`, createdAt: '2026-10-08T00:00:00Z' }))
const chunking = { algorithm: 'recursive', separator: '\n\n', minLength: 200, maxLength: 2000, keepHeadingPath: true }
const capabilities = { canEdit: true, canRun: true, canReview: true, canPublish: true, canDownload: true, canManageMembers: true }
const counts = { sourceItems: 3, importedVersions: 1, skippedExisting: 1, skippedNoContent: 0, failedItems: 1 }
const fixture = { uploaded: false, polls: 0, product: false, ledgerReads: 0, sourceRequests: [], previewRequests: [], productRequests: [], uploadRequests: 0, savedCoverage: null, chunksFailOnce: false, documentState: 'default', documentGate: null, previewGate: null, previewObserver: null, canRun: true, capabilityError: false }

function sourceVersion(version = 2) {
  const ids = version === 1 ? [1, 2] : chunks.map((chunk) => chunk.id)
  const documents = [{ stableId: 'manual', fileName: `${'冷链运输手册'.repeat(8)}.md`, kind: 'markdown', contentHash: 'manual', chunkCount: ids.length, chunkIds: ids, parsedAt: '2026-10-08T00:00:00Z' }]
  if (fixture.uploaded) documents.push({ stableId: 'uploaded', fileName: '新增资料.md', kind: 'markdown', contentHash: 'new', chunkCount: fixture.polls > 2 ? 1 : 0, chunkIds: fixture.polls > 2 ? [12] : [], ...(fixture.polls > 2 ? { parsedAt: '2026-10-08T00:00:00Z' } : {}) })
  return { id: version, version, documentId: 1, projectId: 7, contentHash: 'sourcehash', changeReason: '来源版本', kind: 'source', createdAt: '2026-10-08T00:00:00Z', payload: { schemaVersion: 'source.v1', documents, chunking } }
}

export async function install(page, baseURL = 'http://127.0.0.1:13212') {
  Object.assign(fixture, { uploaded: false, polls: 0, product: false, ledgerReads: 0, sourceRequests: [], previewRequests: [], productRequests: [], uploadRequests: 0, savedCoverage: null, chunksFailOnce: false, documentState: 'default', documentGate: null, previewGate: null, previewObserver: null, canRun: true, capabilityError: false })
  await page.unrouteAll({ behavior: 'wait' })
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname
    let body = {}, status = 200
    if (path.endsWith('/auth/me')) body = { user: { id: 1, email: 'review@example.test', role: 'user' } }
    else if (path.endsWith('/datasets')) body = []
    else if (path.endsWith('/runtime')) body = {}
    else if (/\/projects\/7$/.test(path)) body = { data: { id: 7, name: '素材接入验收', domainCount: 2, directionsPerDomain: 3, questionsPerDirection: 4 }, capabilities }
    else if (path.endsWith('/overview')) {
      if (fixture.capabilityError) { status = 503; body = { error: { message: '权限服务暂时不可用' } } }
      else body = { data: {}, capabilities: { ...capabilities, canRun: fixture.canRun } }
    }
    else if (/\/(source|coverage)-versions$/.test(path) && fixture.documentState !== 'default') {
      if (fixture.documentState === 'loading') await fixture.documentGate
      if (fixture.documentState === 'error') { status = 503; body = { error: { message: '文档读取暂时失败' } } }
      else if (fixture.documentState === 'empty') body = { items: [], document: { revision: 0, currentVersion: 0 }, canEdit: true }
      else if (path.endsWith('/source-versions')) body = { items: [sourceVersion(2)], document: { revision: 2, currentVersion: 2 }, canEdit: true }
      else body = { items: [{ id: 8, version: 1 }], document: { revision: 1 }, canEdit: true }
    }
    else if (/\/source-versions(?:\/\d+)?$/.test(path)) {
      const number = Number(path.split('/').at(-1))
      body = Number.isFinite(number) ? { version: sourceVersion(number) } : { items: [sourceVersion(2), sourceVersion(1)], document: { revision: 2, currentVersion: 2 }, canEdit: true }
    } else if (path.endsWith('/coverage-versions')) {
      if (request.method() === 'POST') { fixture.savedCoverage = request.postDataJSON(); body = { version: fixture.savedCoverage } }
      else body = { items: [{ id: 8, version: 1 }], document: { revision: 1 }, canEdit: true }
    } else if (path.endsWith('/coverage-versions/1')) body = { version: { id: 8, version: 1, contentHash: 'coveragehash', payload: fixture.savedCoverage?.payload ?? { schemaVersion: 'coverage.v1', domains: [{ stableId: 'cold', name: '冷链', directions: [{ stableId: 'risk', name: '风险处置', quota: 3, source: 'document', sourceChunkIds: [12] }] }] } } }
    else if (path.endsWith('/source-imports') && request.method() === 'POST') {
      fixture.uploadRequests++
      assert.match(request.headers()['content-type'], /^multipart\/form-data; boundary=/)
      assert.match(request.postData(), /name="expectedRevision"\r\n\r\n2/)
      fixture.uploaded = true; status = 202; body = { importId: 77, status: 'pending', counts, warnings: [], replay: false }
    } else if (path.endsWith('/source-imports')) {
      if (fixture.uploaded) fixture.polls++
      body = { items: fixture.uploaded ? [{ id: 77, sourceKind: 'source_document', status: fixture.polls > 2 ? 'completed' : 'running', counts, failures: [], errorMessage: '' }] : [], total: fixture.uploaded ? 1 : 0, offset: 0, limit: 10 }
    } else if (path.endsWith('/source-chunks')) {
      fixture.sourceRequests.push(Object.fromEntries(url.searchParams))
      if (fixture.chunksFailOnce) { fixture.chunksFailOnce = false; status = 503; body = { error: { message: '素材读取暂时失败' } } }
      else {
        const version = Number(url.searchParams.get('sourceVersionId')), offset = Number(url.searchParams.get('offset') || 0), q = url.searchParams.get('q') || ''
        const items = (version === 1 ? chunks.slice(0, 2) : chunks).filter((chunk) => chunk.content.includes(q))
        body = { items: items.slice(offset, offset + 10), total: items.length, offset, limit: 10 }
      }
    } else if (/\/source-chunks\/\d+$/.test(path)) body = chunks.find((chunk) => chunk.id === Number(path.split('/').at(-1)))
    else if (path.endsWith('/source-import-products/preview')) {
      fixture.previewRequests.push(request.postDataJSON())
      fixture.previewObserver?.()
      if (fixture.previewGate) await fixture.previewGate
      const input = request.postDataJSON()
      if (input.content === '{broken') { status = 422; body = { error: { message: 'JSON 格式无效' } } }
      else body = { sourceItems: 3, validItems: 1, duplicateItems: 1, failedItems: 1, failures: [{ sourceId: 3, reason: '缺少答案' }] }
    } else if (path.endsWith('/source-import-products')) { fixture.product = true; fixture.productRequests.push(request.postDataJSON()); body = { importId: 88, status: 'completed', replay: true, counts, warnings: [] } }
    else if (path.endsWith('/source-imports/88')) { fixture.ledgerReads++; body = { id: 88, status: 'completed', counts, failures: [{ sourceId: 3, reason: '缺少答案' }], errorMessage: '' } }
    else if (/\/(activity|events|samples)$/.test(path)) body = { items: [] }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.goto(`${baseURL}/p/7/sources`)
  await page.locator('[data-source-chunk-id="1"]').waitFor()
  return 'Production sources page mounted with API contract fixtures.'
}

export async function verify(page, baseURL = 'http://127.0.0.1:13212') {
  console.log('Source UI: history, upload and chunk selection')
  const failures = []
  page.on('pageerror', (error) => failures.push(error.message))
  await page.getByRole('button', { name: '下一页', exact: true }).first().click()
  await page.locator('[data-source-chunk-id="11"]').waitFor()
  assert.equal(await page.locator('[data-source-chunk-id]').count(), 2)
  await page.goto(`${baseURL}/p/7/sources?version=1`)
  await page.locator('[data-source-chunk-id="1"]').waitFor()
  assert.equal(await page.locator('[data-source-chunk-id]').count(), 2)
  assert.equal(fixture.sourceRequests.at(-1).sourceVersionId, '1')
  assert.equal(await page.getByRole('button', { name: '上传素材', exact: true }).isDisabled(), true)
  fixture.chunksFailOnce = true
  await page.goto(`${baseURL}/p/7/sources`)
  await page.getByRole('button', { name: '重新读取素材块' }).click()
  await page.locator('[data-source-chunk-id="1"]').waitFor()
  await page.getByLabel('选择 Markdown 或 TXT 素材').setInputFiles({ name: 'wrong.pdf', mimeType: 'application/pdf', buffer: Buffer.from('invalid') })
  await page.getByRole('alert').filter({ hasText: '目前支持 Markdown/TXT' }).waitFor()
  const uploadsBeforeEmpty = fixture.uploadRequests
  await page.getByLabel('选择 Markdown 或 TXT 素材').setInputFiles({ name: 'empty.txt', mimeType: 'text/plain', buffer: Buffer.alloc(0) })
  await page.getByRole('alert').filter({ hasText: '请选择非空文件' }).waitFor()
  assert.equal(fixture.uploadRequests, uploadsBeforeEmpty, 'empty source files must not enter the upload API')
  await page.getByLabel('选择 Markdown 或 TXT 素材').setInputFiles({ name: '新增资料.md', mimeType: 'text/markdown', buffer: Buffer.from('# 新增资料\n\n运输温度应按政策记录。') })
  await page.getByText('新增资料.md', { exact: true }).waitFor()
  // 解析完成会重新加载来源版本，details 会随页面重挂载恢复折叠。
  await page.getByText('已完成', { exact: true }).waitFor({ state: 'attached', timeout: 12000 })
  const importRecords = page.locator('summary').filter({ hasText: /^导入记录 ·/ })
  if (!await importRecords.evaluate((summary) => summary.parentElement.open)) await importRecords.click()
  await page.getByText('已完成', { exact: true }).waitFor()
  console.log('Source UI: cancel stage navigation retains chunking draft')
  await page.locator('summary').filter({ hasText: /^切分策略/ }).click()
  await page.getByLabel('最小块长度', { exact: true }).fill('201')
  assert.equal(await page.getByRole('button', { name: '上传素材', exact: true }).isDisabled(), true)
  const cancelledLeave = new Promise((resolve, reject) => page.once('dialog', async (dialog) => {
    try {
      assert.equal(dialog.type(), 'confirm')
      assert.match(dialog.message(), /未保存修改/)
      await dialog.dismiss()
      resolve()
    } catch (error) { reject(error) }
  }))
  await page.getByRole('link', { name: '目标结构', exact: true }).first().click()
  await cancelledLeave
  assert.equal(new URL(page.url()).pathname, '/p/7/sources')
  assert.equal(await page.getByLabel('最小块长度', { exact: true }).inputValue(), '201', 'cancelled navigation must retain the changed source draft')
  await page.getByLabel('最小块长度', { exact: true }).fill('200')
  await page.goto(`${baseURL}/p/7/coverage`)
  await page.getByRole('button', { name: '选择素材块', exact: true }).click()
  await page.getByRole('checkbox', { name: '关联素材块 1', exact: true }).check()
  await page.getByRole('button', { name: '下一页素材', exact: true }).click()
  await page.getByRole('checkbox', { name: '关联素材块 12', exact: true }).waitFor()
  assert.equal(await page.getByRole('checkbox', { name: '关联素材块 12', exact: true }).isChecked(), true)
  await page.getByRole('button', { name: '保存覆盖方案新版本' }).click()
  await page.getByRole('button', { name: '保存覆盖方案新版本' }).waitFor()
  assert.deepEqual(fixture.savedCoverage.payload.domains[0].directions[0].sourceChunkIds, [12, 1])
  assert.equal(fixture.savedCoverage.changeReason, '更新设计配置')
  await page.goto(`${baseURL}/p/7/sources/import`)
  console.log('Source UI: external product validation and import')
  assert.equal(await page.locator('[data-source-import-state="empty"]').count(), 1)
  await page.getByRole('textbox', { name: '导入名称', exact: true }).fill('Easy Dataset 导出')
  await page.locator('summary').filter({ hasText: '粘贴或编辑数据' }).click()
  await page.getByRole('textbox', { name: '外部数据集内容', exact: true }).fill('{broken')
  assert.equal(await page.locator('[data-source-import-state="default"]').count(), 1)
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.getByRole('alert').filter({ hasText: 'JSON 格式无效' }).waitFor()
  const largeFileDir = mkdtempSync(join(tmpdir(), 'source-ui-oversized-'))
  const largeFile = join(largeFileDir, 'oversized.json')
  writeFileSync(largeFile, Buffer.alloc(21 * 1024 * 1024))
  try { await page.getByLabel('选择外部数据集文件').setInputFiles(largeFile, { timeout: 60000 }) }
  finally { rmSync(largeFileDir, { recursive: true, force: true }) }
  await page.getByRole('alert').filter({ hasText: '最多 20 MB' }).waitFor()
  let releasePreview
  fixture.previewGate = new Promise((resolve) => { releasePreview = resolve })
  const pendingPreview = page.waitForRequest((request) => request.url().endsWith('/source-import-products/preview'))
  const interceptedPreview = new Promise((resolve) => { fixture.previewObserver = resolve })
  const previewCountBeforeFile = fixture.previewRequests.length
  await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'alpaca.json', mimeType: 'application/json', buffer: Buffer.from('[{"instruction":"冷链处置","output":"隔离并评估"}]') })
  await pendingPreview
  await interceptedPreview
  fixture.previewObserver = null
  assert.equal(fixture.previewRequests.length, previewCountBeforeFile + 1, 'choosing a file automatically starts one preview without clicking check')
  assert.equal(fixture.previewRequests.at(-1).format, 'alpaca')
  assert.equal(await page.getByRole('button', { name: '校验并预览', exact: true }).isDisabled(), true)
  assert.equal(await page.getByRole('textbox', { name: '外部数据集内容', exact: true }).isDisabled(), true)
  releasePreview(); fixture.previewGate = null
  await page.locator('[data-source-import-preview]').waitFor({ state: 'attached' })
  await page.getByRole('textbox', { name: '导入名称', exact: true }).fill('改名后预览应失效')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0)
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.locator('[data-source-import-preview]').waitFor()
  await page.getByRole('textbox', { name: '外部数据集内容', exact: true }).fill('[{"instruction":"新输入必须重新校验","output":"回答"}]')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0, 'editing content must invalidate the previous preview')
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.locator('[data-source-import-preview]').waitFor()
  await page.locator('summary').filter({ hasText: '格式映射与导入说明' }).click()
  await page.getByRole('textbox', { name: '导入说明', exact: true }).fill('来源说明已变更')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0, 'editing change reason must invalidate the previous preview')
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.locator('[data-source-import-preview]').waitFor()
  const previewsBeforeSameFormat = fixture.previewRequests.length
  await page.getByRole('combobox').click()
  await page.getByRole('option', { name: /Alpaca/ }).click()
  await page.getByRole('option', { name: /Alpaca/ }).waitFor({ state: 'hidden' })
  assert.equal(await page.locator('[data-source-import-preview]').count(), 1, 'selecting the same format must retain a valid preview')
  assert.equal(fixture.previewRequests.length, previewsBeforeSameFormat, 'same format selection must not revalidate')
  await page.getByRole('combobox').click()
  await page.getByRole('option', { name: /ShareGPT/ }).click()
  await page.locator('[data-source-import-preview]').waitFor({ state: 'detached' })
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0, 'changing format must invalidate the previous preview')
  await page.getByRole('combobox').click()
  await page.getByRole('option', { name: /Alpaca/ }).click()
  await page.waitForFunction(() => document.querySelector('[role="combobox"] .semi-select-selection-text')?.textContent === 'Alpaca')
  await page.getByRole('option', { name: /Alpaca/ }).waitFor({ state: 'hidden' })
  const formatPreviewResponse = page.waitForResponse((response) => response.url().endsWith('/source-import-products/preview') && response.status() === 200)
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await formatPreviewResponse
  await page.locator('[data-source-import-preview]').waitFor()
  // The preview card is replaced after the request resolves; yield one frame so
  // Semi's button is attached before exercising the confirmation action.
  await page.waitForTimeout(100)
  await page.getByRole('button', { name: '确认导入 1 条有效记录' }).click()
  // 预览也显示同名失败，必须确认实际账本已读到结果页，避免命中尚未卸载的预览。
  await page.locator('[data-source-import-state="result"]').getByText('第 3 条：缺少答案', { exact: true }).waitFor()
  assert.equal(fixture.ledgerReads, 1, 'completed replay must read failure details')
  assert.equal(fixture.productRequests.at(-1).content, '[{"instruction":"新输入必须重新校验","output":"回答"}]')
  assert.equal(fixture.productRequests.at(-1).changeReason, '来源说明已变更')
  console.log('Source UI: auto format detection, file-name default and empty file guard')
  for (const [fileName, content, format] of [
    ['sharegpt.json', '[{"conversations":[{"from":"human","value":"问题"},{"from":"gpt","value":"回答"}]}]', 'sharegpt'],
    ['records.ndjson', '{"question":"问题","answer":"回答"}\n', 'jsonl'],
    ['records.json', '{"question":"问题","answer":"回答"}', 'jsonl'],
  ]) {
    await page.goto(`${baseURL}/p/7/sources/import`)
    await page.getByRole('button', { name: '校验并预览', exact: true }).waitFor()
    await page.waitForFunction(() => !document.querySelector('input[aria-label="选择外部数据集文件"]')?.disabled)
    const previousCount = fixture.previewRequests.length
    await page.getByLabel('选择外部数据集文件').setInputFiles({ name: fileName, mimeType: 'application/json', buffer: Buffer.from(content) })
    await page.locator('[data-source-import-preview]').waitFor()
    assert.equal(fixture.previewRequests.length, previousCount + 1)
    assert.equal(fixture.previewRequests.at(-1).format, format, `${fileName}: expected detected format ${format}`)
    assert.equal(fixture.previewRequests.at(-1).sourceKey, fileName, 'file name supplies import name when the field is empty')
    assert.equal(await page.getByRole('textbox', { name: '导入名称', exact: true }).inputValue(), fileName)
  }
  // 已有效预览后选择无效文件也必须取消旧确认，不能误导入上一个文件。
  for (const [fileName, buffer, expectedError] of [
    ['empty.json', Buffer.alloc(0), '非空的 JSON/JSONL'],
    ['oversized.json', Buffer.alloc(21 * 1024 * 1024), '最多 20 MB'],
  ]) {
    const countBeforeInvalid = fixture.previewRequests.length
    await page.getByLabel('选择外部数据集文件').setInputFiles({ name: fileName, mimeType: 'application/json', buffer })
    await page.getByRole('alert').filter({ hasText: expectedError }).waitFor()
    assert.equal(fixture.previewRequests.length, countBeforeInvalid)
    assert.equal(await page.locator('[data-source-import-preview]').count(), 0, `${fileName}: old preview must be removed`)
    assert.equal(await page.getByRole('button', { name: /确认导入/ }).count(), 0, `${fileName}: old file cannot remain importable`)
    if (fileName === 'empty.json') {
      await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'valid.json', mimeType: 'application/json', buffer: Buffer.from('[{"instruction":"问题","output":"回答"}]') })
      await page.locator('[data-source-import-preview]').waitFor()
    }
  }
  await page.goto(`${baseURL}/p/7/sources/import`)
  await page.waitForFunction(() => !document.querySelector('input[aria-label="选择外部数据集文件"]')?.disabled)
  const previewsBeforeEmpty = fixture.previewRequests.length
  await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'empty.json', mimeType: 'application/json', buffer: Buffer.alloc(0) })
  await page.getByRole('alert').filter({ hasText: '非空的 JSON/JSONL' }).waitFor()
  assert.equal(fixture.previewRequests.length, previewsBeforeEmpty, 'empty files must not request preview')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0)
  assert.equal(await page.getByRole('button', { name: /确认导入/ }).count(), 0)
  for (const path of ['/p/7/sources', '/p/7/coverage']) {
    console.log(`Source UI: five states ${path}`)
    let releaseDocument
    fixture.documentState = 'loading'
    fixture.documentGate = new Promise((resolve) => { releaseDocument = resolve })
    await page.goto(`${baseURL}${path}`)
    await page.locator('[data-source-state="loading"]').waitFor()
    fixture.documentState = 'default'; releaseDocument()
    await page.locator('[data-source-state="default"]').waitFor()
    fixture.documentState = 'empty'
    await page.goto(`${baseURL}${path}`)
    await page.locator('[data-source-state="empty"]').waitFor()
    if (path.endsWith('/sources')) assert.equal(await page.getByRole('button', { name: '上传第一份素材', exact: true }).isDisabled(), false)
    fixture.documentState = 'error'
    await page.goto(`${baseURL}${path}`)
    await page.locator('[data-source-state="error"]').waitFor()
    fixture.documentState = 'default'
    await page.getByRole('button', { name: path.endsWith('/sources') ? '重新加载' : '重试', exact: true }).click()
    await page.locator('[data-source-state="default"]').waitFor()
  }
  console.log('Source UI: permission read failure is recoverable and not access denial')
  fixture.capabilityError = true
  await page.goto(`${baseURL}/p/7/sources/import`)
  await page.getByRole('alert').filter({ hasText: '权限读取失败：权限服务暂时不可用' }).waitFor()
  assert.equal(await page.locator('[data-studio-page="source-import"]').getAttribute('data-source-import-state'), 'error', 'permission-read failures must expose the page error state')
  assert.equal(await page.getByText('当前项目没有导入权限，请联系项目负责人。', { exact: true }).count(), 0)
  assert.equal(await page.getByLabel('选择外部数据集文件').isDisabled(), true)
  assert.equal(await page.getByRole('button', { name: '校验并预览', exact: true }).isDisabled(), true)
  fixture.capabilityError = false
  await page.getByRole('button', { name: '重试读取权限', exact: true }).click()
  await page.waitForFunction(() => !document.querySelector('input[aria-label="选择外部数据集文件"]')?.disabled)
  assert.equal(await page.locator('[data-studio-page="source-import"]').getAttribute('data-source-import-state'), 'empty', 'successful permission retry must clear the page error state')
  const previewsBeforePermissionRetry = fixture.previewRequests.length
  await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'retry.json', mimeType: 'application/json', buffer: Buffer.from('[{"instruction":"问题","output":"回答"}]') })
  await page.locator('[data-source-import-preview]').waitFor()
  assert.equal(fixture.previewRequests.length, previewsBeforePermissionRetry + 1, 'permission retry must restore real import preview interaction')
  console.log('Source UI: upload and import permissions')
  fixture.canRun = false
  const previewCountBeforeDenied = fixture.previewRequests.length
  const productCountBeforeDenied = fixture.productRequests.length
  await page.goto(`${baseURL}/p/7/sources`)
  await page.locator('[data-source-state="default"]').waitFor()
  assert.equal(await page.getByRole('button', { name: '上传素材', exact: true }).isDisabled(), true)
  await page.goto(`${baseURL}/p/7/sources/import`)
  await page.getByText('当前项目没有导入权限，请联系项目负责人。', { exact: true }).waitFor()
  assert.equal(await page.getByLabel('选择外部数据集文件').isDisabled(), true)
  assert.equal(await page.getByRole('button', { name: '校验并预览', exact: true }).isDisabled(), true)
  await page.locator('summary').filter({ hasText: '粘贴或编辑数据' }).click()
  await page.getByRole('textbox', { name: '导入名称', exact: true }).fill('无权限粘贴')
  await page.getByRole('textbox', { name: '外部数据集内容', exact: true }).fill('[{"instruction":"问题","output":"回答"}]')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0)
  assert.equal(fixture.previewRequests.length, previewCountBeforeDenied)
  assert.equal(fixture.productRequests.length, productCountBeforeDenied)
  fixture.canRun = true
  for (const path of ['/p/7/sources', '/p/7/coverage', '/p/7/sources/import']) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${baseURL}${path}`)
    await page.locator('[data-studio-page]').waitFor()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    assert.ok(overflow <= 1, `${path}: horizontal overflow ${overflow}px`)
  }
  assert.deepEqual(failures, [])
  return 'PASS: Default/Loading/Empty/Error/Edge-Case states, frozen history, chunk pagination/retry, invalid/empty and async upload, collapsed import ledger, cancelled stage navigation retains draft, cross-page association/save, malformed JSON, oversized product file, automatic Alpaca/ShareGPT/JSONL detection and preview, file-name default, name/content/format/change-reason preview invalidation, existing-preview empty/oversized-file and permission guards, permission-read failure/retry without false denial, completed replay failures, 390px layouts; no page errors.'
}
