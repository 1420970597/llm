/**
 * 部署版本注入守卫（Issue #175）。
 *
 * 这条守卫不启动 Docker，只冻结「标准入口必须注入版本」与「Stack CI 必须
 * 校验 version.json」两条运维契约，避免文档和 CI 悄悄退回 unknown。
 */
import { readFileSync } from 'node:fs'

const makefile = readFileSync('Makefile', 'utf8')
const ci = readFileSync('.github/workflows/ci.yml', 'utf8')
const readme = readFileSync('README.md', 'utf8')
const compose = readFileSync('deployments/compose/docker-compose.yml', 'utf8')

const failures = []
function check(name, condition, detail) {
  console.log(`[${condition ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!condition) failures.push(name)
}

check(
  'make compose-up 注入 Git SHA 与构建时间',
  /GIT_SHA\s*\?=/.test(makefile) &&
    /BUILD_TIME\s*\?=/.test(makefile) &&
    /GIT_SHA="\$\(GIT_SHA\)" BUILD_TIME="\$\(BUILD_TIME\)"[^\n]*\$\(COMPOSE\) up -d --build/.test(makefile),
  '标准 Makefile 入口在 compose 前注入两个 build args',
)
check(
  'Stack CI 注入并校验部署版本',
  /export GIT_SHA=.*git rev-parse HEAD/.test(ci) &&
    /export BUILD_TIME=.*date -u/.test(ci) &&
    /scripts\/check-deployed-version\.sh/.test(ci),
  'CI 构建使用 checkout HEAD，并执行 version.json 一致性检查',
)
check(
  'README 主流程使用可验收启动命令',
  /在项目根目录执行：\s*```bash\s*make compose-up/.test(readme) &&
    /GIT_SHA="\$\(git rev-parse HEAD\)"/.test(readme),
  '文档主流程不会再把不注入版本的 raw compose 当作默认路径',
)
check(
  'Compose 保留 unknown 的诚实兜底',
  /GIT_SHA: \$\{GIT_SHA:-unknown\}/.test(compose),
  '未由入口注入时仍显式报告无法自证，而不是伪造 SHA',
)

if (failures.length > 0) {
  console.error(`部署版本守卫失败：${failures.join('、')}`)
  process.exit(1)
}
console.log('部署版本守卫全部通过。')
