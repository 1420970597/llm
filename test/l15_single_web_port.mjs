#!/usr/bin/env node

import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

function read(relativePath) {
  return fs.readFileSync(path.join(root, relativePath), 'utf8')
}

const compose = read('deployments/compose/docker-compose.yml')
const rootCompose = read('docker-compose.yml')
const envExample = read('.env.example')
const packageJSON = read('apps/web-user/package.json')
const vite = read('apps/web-user/vite.config.ts')
const readme = read('README.md')

assert.match(compose, /\$\{WEB_USER_PORT:-3210\}:80/)
assert.match(envExample, /^WEB_USER_PORT=3210$/m)
assert.match(packageJSON, /vite(?: preview)?[^\n]*--port 3210/)
assert.match(vite, /port:\s*3210/)
assert.match(readme, /http:\/\/127\.0\.0\.1:3210/)
assert.doesNotMatch(rootCompose, /3212/)
assert.doesNotMatch(compose, /3212/)
assert.doesNotMatch(envExample, /3212/)
assert.doesNotMatch(packageJSON, /3212/)
assert.doesNotMatch(vite, /3212/)
assert.doesNotMatch(readme, /3212/)

console.log('single web port contract passed')
