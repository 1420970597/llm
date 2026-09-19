/**
 * downloadTarget 的单元测试。
 *
 * 位置说明：本目录不在 apps/web-user/tsconfig.json 的 `include`（仅 `src`）内，
 * 因此不会参与 `tsc --noEmit` 与 vite 构建，也就不受
 * `allowImportingTsExtensions: false` 的限制——可以直接以 .ts 后缀导入被测文件。
 *
 * 运行：node --experimental-strip-types tests/downloadTarget.test.ts
 * 不依赖任何 npm 包，只用 Node 内置的 node:test 与 node:assert。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  resolveDownloadTarget,
  resolveDownloadPath,
  DOWNLOAD_PATH_PREFIX,
} from '../src/views/flow/downloadTarget.ts'

test('接受合法的正整数 id', () => {
  assert.deepEqual(resolveDownloadTarget(3, 11), { datasetId: 3, artifactId: 11 })
  assert.deepEqual(resolveDownloadTarget('3', '11'), { datasetId: 3, artifactId: 11 })
})

test('拒绝会产生无效路径的值（这是本次加固的核心动机）', () => {
  // 递归：这些都会拼出 /api/v1/datasets/NaN/... 之类的请求
  assert.equal(resolveDownloadTarget(NaN, 1), null)
  assert.equal(resolveDownloadTarget(1, NaN), null)
  assert.equal(resolveDownloadTarget(undefined, 1), null)
  assert.equal(resolveDownloadTarget(null, 1), null)
  assert.equal(resolveDownloadTarget('abc', 1), null)
  assert.equal(resolveDownloadTarget({}, 1), null)
  assert.equal(resolveDownloadTarget([], 1), null)
  assert.equal(resolveDownloadTarget(true, 1), null)
})

test('拒绝非正整数', () => {
  assert.equal(resolveDownloadTarget(0, 1), null)
  assert.equal(resolveDownloadTarget(1, 0), null)
  assert.equal(resolveDownloadTarget(-1, 1), null)
  assert.equal(resolveDownloadTarget(1.5, 1), null)
  assert.equal(resolveDownloadTarget(Infinity, 1), null)
})

test('下载路径必须是同源相对路径', () => {
  assert.equal(resolveDownloadPath('/api/v1/datasets/3/export/download?artifactId=11'),
    '/api/v1/datasets/3/export/download?artifactId=11')
})

test('拒绝跨域与协议相对等绝对 URL（防重构引入 SSRF 面）', () => {
  assert.equal(resolveDownloadPath('https://evil.example.com/steal'), null)
  assert.equal(resolveDownloadPath('http://evil.example.com/steal'), null)
  assert.equal(resolveDownloadPath('//evil.example.com/steal'), null)
  assert.equal(resolveDownloadPath('javascript:alert(1)'), null)
  assert.equal(resolveDownloadPath('data:text/html,x'), null)
  assert.equal(resolveDownloadPath('/etc/passwd'), null)
})

test('白名单前缀是显式常量，便于审查', () => {
  assert.equal(DOWNLOAD_PATH_PREFIX, '/api/')
})
