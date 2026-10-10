/** #197-9: production workbenches and commands, with controlled API responses. */
import assert from 'node:assert/strict'

export async function verifyProjectTools(page, baseURL) {
  // 来源 suite 以移动端结束；本 suite 的菜单验收明确使用桌面导航。
  await page.setViewportSize({ width: 1440, height: 1024 })
  await page.unrouteAll({ behavior: 'wait' })
  const capabilities = { canEdit: true, canRun: true, canReview: true, canPublish: true, canDownload: true, canManageMembers: true }
  let mode = 'default', targetKind = 'sft', migrationMode = 'partial', experiment, preview, projectReads = 0
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  const document = (id, version, policy) => ({ id, version, projectId: 7, documentId: 1, kind: 'blueprint', schemaVersion: 'blueprint.v1', changeReason: `配置 ${version}`, payload: { nodes: { evaluation: { judgeConnectionIds: [21, 22], samplingSeed: 88, missingScorePolicy: 'exclude', weights: { accuracy: 0.6, reasoning: 0.4 } }, rules: { qualityPolicyVersionId: policy } } } })
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname
    let body = {}, status = 200
    if (path.endsWith('/auth/me')) body = { user: { id: 1, email: 'review@example.test', role: 'user' } }
    else if (path.endsWith('/datasets')) body = []
    else if (path.endsWith('/legacy/migration-status')) {
      if (migrationMode === 'error') { status = 503; body = { error: { message: '对账读取失败' } } }
      else body = { scope: migrationMode === 'unscoped' ? undefined : 'visible_legacy_assets', legacyDatasets: migrationMode === 'empty' ? 0 : 2, migrationComplete: migrationMode !== 'partial', pendingDatasets: migrationMode === 'partial' ? 1 : 0, importedRecords: 2, boundProjects: 2, note: '受控账号范围对账' }
    }
    else if (path.endsWith('/projects')) {
      projectReads++
      if (mode === 'error') { status = 503; body = { error: { message: '项目读取暂时失败' } } }
      else body = { items: mode === 'empty' ? [] : [{ resourceId: 'p_7', data: { id: 7, name: '原生蓝图项目' }, capabilities }], nextCursor: '' }
    } else if (/\/projects\/7$/.test(path)) body = { data: { id: 7, name: '原生蓝图项目' }, capabilities }
    else if (path.endsWith('/overview')) body = { data: { targetKind }, capabilities }
    else if (path.endsWith('/blueprint-versions')) body = { items: [document(102, 2, 66), document(101, 1, 55)] }
    else if (path.endsWith('/blueprint-versions/1')) body = { version: document(101, 1, 55) }
    else if (path.endsWith('/blueprint-versions/2')) body = { version: document(102, 2, 66) }
    // 冻结策略 55 已不在最近一页中；有效历史引用仍应提交给服务端校验。
    else if (path.endsWith('/quality-policy-versions')) body = { items: [{ id: 66, version: 2 }], nextCursor: '2', document: { revision: 2 }, canEdit: true }
    else if (path.endsWith('/quality-policy-versions/2')) body = { version: { id: 66, version: 2, projectId: 7, documentId: 2, kind: 'quality_policy', schemaVersion: 'quality_policy.v1', contentHash: 'policy66', changeReason: '策略 2', createdAt: '2026-10-08T00:00:00Z', payload: { schemaVersion: 'quality_policy.v1', rules: [] } } }
    else if (path.endsWith('/settings/connection-options')) body = { providers: [21, 22].map((id) => ({ id, name: `裁判 ${id}`, model: 'judge', isActive: true, configIssues: [] })) }
    else if (path.endsWith('/samples')) body = { items: [{ sampleId: 1, resourceId: 's_1', sampleKey: 'sample', title: '蓝图样本', latestVersion: 1, latestVersionId: 301, reviewStatus: 'accepted', capabilities }] }
    else if (path.endsWith('/experiments') && request.method() === 'POST') {
      experiment = request.postDataJSON(); status = 422; body = { error: { message: '验收请求已确认，未创建实验' } }
    } else if (path.endsWith('/rule-previews')) { preview = request.postDataJSON(); body = { scannedCount: 1, hits: [], sideEffects: false, truncated: false } }
    else if (path.endsWith('/batches')) body = { items: [] }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })
  for (const kind of ['evaluation', 'cleaning']) {
    console.log(`Project tools: ${kind} states, frozen blueprint and requests`)
    mode = 'empty'
    await page.goto(`${baseURL}/tools/${kind}`)
    await page.getByText('还没有项目。创建项目后配置蓝图，再运行评估或规则预览。', { exact: true }).waitFor()
    mode = 'error'
    await page.reload()
    await page.locator('[data-tool-error]').waitFor()
    mode = 'default'
    await page.getByRole('button', { name: '重试', exact: true }).click()
    await page.getByRole('combobox', { name: '工作台项目', exact: true }).selectOption('7')
    await page.locator('[data-tool-blueprint-context]').waitFor()
    await page.getByRole('combobox', { name: '工作台蓝图版本', exact: true }).selectOption('101')
    await page.getByText('蓝图 v1 · 配置 1', { exact: true }).waitFor()
    const link = page.getByRole('link', { name: kind === 'evaluation' ? '使用此蓝图创建质量实验' : '使用此蓝图预览清洗规则', exact: true })
    const href = await link.getAttribute('href')
    assert.match(href, /projectId=7/); assert.match(href, /blueprintVersionId=101/)
    await link.click()
    if (kind === 'evaluation') {
      await page.locator('[data-quality-blueprint-context]').getByText(/已带入 2 名裁判/).waitFor()
      await page.getByRole('checkbox', { name: '选择 蓝图样本', exact: true }).check()
      const blueprintExperiment = page.waitForResponse((response) => response.url().endsWith('/experiments') && response.request().method() === 'POST')
      await page.getByRole('button', { name: '创建并冻结实验', exact: true }).click()
      await blueprintExperiment
      await page.getByRole('alert').waitFor()
      console.log('Project tools: manual judge after SPA context removal')
      assert.deepEqual(experiment.judgeConnectionIds, [21, 22])
      assert.equal(experiment.samplingSeed, 88)
      assert.equal(experiment.missingScorePolicy, 'exclude')
      assert.deepEqual(experiment.rubric.dimensions.map(({ key, weight }) => [key, weight]), [['accuracy', 0.6], ['reasoning', 0.4]])
      // SPA navigation retains the component; removing the context must clear its values.
      await page.evaluate(() => { history.pushState({}, '', '/p/7/quality/new'); window.dispatchEvent(new PopStateEvent('popstate')) })
      await page.locator('[data-quality-blueprint-context]').waitFor({ state: 'detached' })
      await page.getByRole('button', { name: '创建并冻结实验', exact: true }).click()
      await page.getByRole('alert').getByText('请先选择裁判模型，再创建实验（独立性由服务端校验）', { exact: true }).waitFor()
      await page.locator('[data-judge-connection-select]').click()
      await page.getByText('裁判 21（judge）', { exact: true }).last().click()
      await page.locator('[data-field="judge-connection"][data-selected-judge="21"]').waitFor()
      const manualExperiment = page.waitForResponse((response) => response.url().endsWith('/experiments') && response.request().method() === 'POST')
      await page.getByRole('button', { name: '创建并冻结实验', exact: true }).click()
      await manualExperiment
      await page.getByRole('alert').waitFor()
      assert.deepEqual(experiment.judgeConnectionIds, [21])
      assert.equal(experiment.samplingSeed, 42)
      assert.deepEqual(experiment.rubric.dimensions.map(({ key, weight }) => [key, weight]), [['accuracy', 1]])
    } else {
      await page.locator('[data-rule-blueprint-context]').waitFor()
      await page.getByLabel('样本版本 ID（逗号分隔）').fill('301')
      await page.getByRole('button', { name: '预览命中', exact: true }).click()
      await page.locator('[data-preview-result]').waitFor()
      assert.equal(preview.qualityPolicyVersionId, 55, 'use historical blueprint reference, not latest policy 66')
      assert.deepEqual(preview.sampleVersionIds, [301])
    }
  }
  for (const state of ['empty', 'partial', 'complete', 'error', 'unscoped']) {
    console.log(`Project tools: migration menu ${state}`)
    migrationMode = state
    const statusRead = page.waitForResponse((response) => response.url().endsWith('/legacy/migration-status'))
    await page.goto(`${baseURL}/tools/evaluation?projectId=7&blueprintVersionId=101`)
    await statusRead
    await page.getByRole('button', { name: '工具与设置', exact: true }).click()
    await page.locator('[data-studio-tools-menu]').waitFor()
    const historyItem = page.getByRole('menuitem', { name: '历史资产', exact: true })
    if (state === 'complete') assert.equal(await historyItem.count(), 0)
    else await historyItem.waitFor({ state: 'visible' })
    await page.keyboard.press('Escape')
  }
  migrationMode = 'complete'
  await page.goto(`${baseURL}/legacy/history`)
  await page.getByText('历史资产（只读）', { exact: true }).waitFor()
  await page.getByRole('button', { name: '工具与设置', exact: true }).click()
  await page.locator('[data-studio-tools-menu]').waitFor()
  assert.equal(await page.getByRole('menuitem', { name: '历史资产', exact: true }).count(), 0)
  await page.keyboard.press('Escape')
  targetKind = 'grpo'; experiment = undefined
  await page.goto(`${baseURL}/p/7/quality/new?projectId=7&blueprintVersionId=101`)
  await page.getByRole('alert').getByText(/GRPO 实验仅支持服务端内置量表/).waitFor()
  await page.getByRole('checkbox', { name: '选择 蓝图样本', exact: true }).check()
  await page.getByRole('button', { name: '创建并冻结实验', exact: true }).click()
  assert.equal(experiment, undefined, 'unsupported GRPO weights cannot be ignored silently')
  targetKind = 'sft'
  await page.goto(`${baseURL}/tools/evaluation?projectId=7&blueprintVersionId=999`)
  await page.getByRole('alert').filter({ hasText: '所选蓝图版本不在当前项目中，请重新选择。' }).waitFor()
  assert.equal(await page.locator('[data-tool-blueprint-context]').count(), 0)
  for (const kind of ['evaluation', 'cleaning']) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${baseURL}/tools/${kind}?projectId=7&blueprintVersionId=101`)
    await page.locator('[data-tool-blueprint-context]').waitFor()
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth) <= 1)
  }
  assert.ok(projectReads >= 8)
  assert.deepEqual(errors, [])
  return 'PASS: native project selection, immutable blueprint selection/deep links, empty/error/retry, unknown blueprint rejection, both judge configurations frozen in experiment request, SPA context reset, incompatible GRPO weights rejected, historical policy reference in rule preview, empty/partial/complete/error/unscoped migration menu and read-only deep link, 390px layout; no page errors.'
}
