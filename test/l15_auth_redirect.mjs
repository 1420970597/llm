#!/usr/bin/env node

/**
 * Execute the same-origin login redirect helper against hostile and normal
 * values. The frontend CI uses Node 22, which can strip types for this small
 * dependency without introducing a second test runner.
 */
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const helperPath = path.join(repoRoot, 'apps/web-user/src/lib/authRedirect.ts')
const helperUrl = pathToFileURL(helperPath).href
const probe = String.raw`
  import { buildLoginPath, resolveLoginRedirect } from ${JSON.stringify(helperUrl)}

  const cases = [
    ['?next=%2Fp%2F1%2Fdata%3Fstatus%3Dreview', '/p/1/data?status=review'],
    ['?next=%2Fp%2F1%2Fdata%23focus', '/p/1/data#focus'],
    ['?next=https%3A%2F%2Fevil.test', '/today'],
    ['?next=%2F%2Fevil.test', '/today'],
    ['?next=%2Fp%2F1%2Fdata%5C%5Cevil', '/today'],
    ['?next=%2Flogin', '/today'],
    ['', '/today'],
  ]

  for (const [search, expected] of cases) {
    const actual = resolveLoginRedirect(search)
    if (actual !== expected) {
      throw new Error('resolveLoginRedirect(' + search + ') => ' + actual + '; expected ' + expected)
    }
  }

  const built = buildLoginPath('/p/1/data', '?status=review', '#focus')
  if (built !== '/login?next=%2Fp%2F1%2Fdata%3Fstatus%3Dreview%23focus') {
    throw new Error('buildLoginPath returned ' + built)
  }

  console.log('auth redirect cases passed')
`

const result = spawnSync(process.execPath, ['--experimental-strip-types', '--input-type=module', '--eval', probe], {
  cwd: repoRoot,
  encoding: 'utf8',
  env: { ...process.env },
})

if (result.status !== 0) {
  process.stderr.write(result.stderr || result.stdout)
  process.exit(result.status ?? 1)
}

process.stdout.write(result.stdout)
