/**
 * #217 真实 Chromium 交互验收。使用生产页面 + API 契约夹具，不替代后端集成测试。
 * Playwright CLI 的 run-code 加载本模块后调用 install(page)、verify(page)。
 * 不需要 @playwright/test，也不向共享 node_modules 新增依赖。
 */
import assert from 'node:assert/strict'

const chunks = Array.from({ length: 12 }, (_, index) => ({ id: index + 1, projectId: 7,
  sourceDocumentStableId: 'manual', headingPath: `手册 / 章节 ${index + 1}`, ordinal: index + 1,
  content: `冷链运输素材第 ${index + 1} 块。${'证据说明。'.repeat(12)}`, contentHash: `chunk-${index}`, createdAt: '2026-10-08T00:00:00Z' }))
const chunking = { algorithm: 'recursive', separator: '\n\n', minLength: 200, maxLength: 2000, keepHeadingPath: true }
const capabilities = { canEdit: true, canRun: true, canReview: true, canPublish: true, canDownload: true, canManageMembers: true }
const counts = { sourceItems: 3, importedVersions: 1, skippedExisting: 1, skippedNoContent: 0, failedItems: 1 }
const fixture = { uploaded: false, polls: 0, product: false, ledgerReads: 0, sourceRequests: [], savedCoverage: null, chunksFailOnce: false }

function sourceVersion(version = 2) {
  const ids = version === 1 ? [1, 2] : chunks.map((chunk) => chunk.id)
  const documents = [{ stableId: 'manual', fileName: `${'冷链运输手册'.repeat(8)}.md`, kind: 'markdown', contentHash: 'manual', chunkCount: ids.length, chunkIds: ids, parsedAt: '2026-10-08T00:00:00Z' }]
  if (fixture.uploaded) documents.push({ stableId: 'uploaded', fileName: '新增资料.md', kind: 'markdown', contentHash: 'new', chunkCount: fixture.polls > 2 ? 1 : 0, chunkIds: fixture.polls > 2 ? [12] : [], ...(fixture.polls > 2 ? { parsedAt: '2026-10-08T00:00:00Z' } : {}) })
  return { id: version, version, documentId: 1, projectId: 7, contentHash: 'sourcehash', changeReason: '来源版本', kind: 'source', createdAt: '2026-10-08T00:00:00Z', payload: { schemaVersion: 'source.v1', documents, chunking } }
}

export async function install(page, baseURL = 'http://127.0.0.1:13212') {
  await page.unrouteAll({ behavior: 'wait' })
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname
    let body = {}, status = 200
    if (path.endsWith('/auth/me')) body = { user: { id: 1, email: 'review@example.test', role: 'user' } }
    else if (path.endsWith('/datasets')) body = []
    else if (path.endsWith('/runtime')) body = {}
    else if (/\/projects\/7$/.test(path)) body = { data: { id: 7, name: '素材接入验收', domainCount: 2, directionsPerDomain: 3, questionsPerDirection: 4 }, capabilities }
    else if (path.endsWith('/overview')) body = { data: {}, capabilities }
    else if (/\/source-versions(?:\/\d+)?$/.test(path)) {
      const number = Number(path.split('/').at(-1))
      body = Number.isFinite(number) ? { version: sourceVersion(number) } : { items: [sourceVersion(2), sourceVersion(1)], document: { revision: 2, currentVersion: 2 }, canEdit: true }
    } else if (path.endsWith('/coverage-versions')) {
      if (request.method() === 'POST') { fixture.savedCoverage = request.postDataJSON(); body = { version: fixture.savedCoverage } }
      else body = { items: [{ id: 8, version: 1 }], document: { revision: 1 }, canEdit: true }
    } else if (path.endsWith('/coverage-versions/1')) body = { version: { id: 8, version: 1, contentHash: 'coveragehash', payload: fixture.savedCoverage?.payload ?? { schemaVersion: 'coverage.v1', domains: [{ stableId: 'cold', name: '冷链', directions: [{ stableId: 'risk', name: '风险处置', quota: 3, source: 'document', sourceChunkIds: [12] }] }] } } }
    else if (path.endsWith('/source-imports') && request.method() === 'POST') {
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
      const input = request.postDataJSON()
      if (input.content === '{broken') { status = 422; body = { error: { message: 'JSON 格式无效' } } }
      else body = { sourceItems: 3, validItems: 1, duplicateItems: 1, failedItems: 1, failures: [{ sourceId: 3, reason: '缺少答案' }] }
    } else if (path.endsWith('/source-import-products')) { fixture.product = true; body = { importId: 88, status: 'completed', replay: true, counts, warnings: [] } }
    else if (path.endsWith('/source-imports/88')) { fixture.ledgerReads++; body = { id: 88, status: 'completed', counts, failures: [{ sourceId: 3, reason: '缺少答案' }], errorMessage: '' } }
    else if (/\/(activity|events|samples)$/.test(path)) body = { items: [] }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.goto(`${baseURL}/p/7/sources`)
  await page.locator('[data-source-chunk-id="1"]').waitFor()
  return 'Production sources page mounted with API contract fixtures.'
}

export async function verify(page, baseURL = 'http://127.0.0.1:13212') {
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
  await page.getByLabel('选择 Markdown 或 TXT 素材').setInputFiles({ name: '新增资料.md', mimeType: 'text/markdown', buffer: Buffer.from('# 新增资料\n\n运输温度应按政策记录。') })
  await page.getByText('新增资料.md', { exact: true }).waitFor()
  await page.getByText('已完成', { exact: true }).waitFor({ timeout: 12000 })
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
  await page.getByRole('textbox', { name: '导入名称', exact: true }).fill('Easy Dataset 导出')
  await page.getByRole('textbox', { name: '外部数据集内容', exact: true }).fill('{broken')
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.getByRole('alert').filter({ hasText: 'JSON 格式无效' }).waitFor()
  await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'oversized.json', mimeType: 'application/json', buffer: Buffer.alloc(21 * 1024 * 1024) })
  await page.getByRole('alert').filter({ hasText: '最多 20 MB' }).waitFor()
  await page.getByLabel('选择外部数据集文件').setInputFiles({ name: 'alpaca.json', mimeType: 'application/json', buffer: Buffer.from('[{"instruction":"冷链处置","output":"隔离并评估"}]') })
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.locator('[data-source-import-preview]').waitFor()
  await page.getByRole('textbox', { name: '导入名称', exact: true }).fill('改名后预览应失效')
  assert.equal(await page.locator('[data-source-import-preview]').count(), 0)
  await page.getByRole('button', { name: '校验并预览', exact: true }).click()
  await page.getByRole('button', { name: '确认导入 1 条有效记录' }).click()
  await page.getByText('第 3 条：缺少答案', { exact: true }).waitFor()
  assert.equal(fixture.ledgerReads, 1, 'completed replay must read failure details')
  for (const path of ['/p/7/sources', '/p/7/coverage', '/p/7/sources/import']) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${baseURL}${path}`)
    await page.locator('[data-studio-page]').waitFor()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    assert.ok(overflow <= 1, `${path}: horizontal overflow ${overflow}px`)
  }
  assert.deepEqual(failures, [])
  return 'PASS: frozen history, chunk pagination/retry, invalid and async upload, cross-page association/save, malformed JSON, oversized/file product import, preview invalidation, completed replay failures, 390px layouts; no page errors.'
}
