/**
 * 构建版本标识。
 *
 * 为什么需要它（issue #88）：部署中的前端镜像曾落后于仓库 HEAD，而**用户无法察觉**。
 * 表现是五个阶段路由在已部署系统上全部被重定向（那是 #61 修复前的行为），
 * 功能验收因此得出与源码不一致的结论。
 *
 * 根因不是产品代码，而是**部署/运维层面缺少「自证版本」的能力**：
 *   1. `llm-web-user-1` 跑的是旧镜像（compose 工程名从 `compose` 改成 `llm` 之后，
 *      留下 `compose-web-user:latest` 与 `llm-web-user:latest` 两个名字，
 *      旧的构建时间停留在 2026-09-19T04:51:42Z，正是 issue 报的那个镜像）；
 *   2. 镜像本身不携带任何 git 版本信息，页面上也看不到，
 *      所以「我跑的是哪一版」只能靠比对镜像构建时间与提交时间这种间接推断。
 *
 * 修复思路：把 git commit 在**构建期**注入（Docker build arg → Vite → 产物），
 * 于是版本号跟着**产物**走而不是跟着「谁在什么时候构建的」走。
 * 部署后可以用两条命令在 5 秒内判定是否落后：
 *   curl -s http://<host>:3210/version.json
 *   git rev-parse HEAD
 * 也可以用 scripts/check-deployed-version.sh 一条命令完成比对。
 *
 * 取值约定：未注入时返回 `unknown`，**不伪造**一个看起来像 commit 的值 ——
 * 「不知道自己是哪一版」本身就是需要被看见的信息。
 */

declare global {
  interface ImportMetaEnv {
    readonly VITE_APP_VERSION?: string
    readonly VITE_APP_BUILD_TIME?: string
  }
  interface ImportMeta {
    readonly env: ImportMetaEnv
  }
}

// 读取构建期注入值的**防御式**入口。
//
// 为什么不能直接写 `import.meta.env.VITE_APP_VERSION`：
// `import.meta.env` 是 Vite 注入的对象，**只在 Vite 构建里存在**。
// 本仓库的 UI 测试（test/l15_stage_routes.mjs、test/l15_capability_entries.mjs 等）
// 用 esbuild + React 渲染来验证真实的组件函数体，而 esbuild **不注入**
// `import.meta.env` —— 它是 `undefined`，于是 `.VITE_APP_VERSION` 抛
// `Cannot read properties of undefined`，把整个渲染腿打挂。
//
// 这在合并 PR #112 时确实发生了：两个此前通过的 SSR 测试（R1 的阶段路由、
// R10 的能力入口）开始失败。`?? ''` 只能兜住「属性不存在」，兜不住「宿主对象不存在」，
// 因此必须在访问前判断宿主是否存在。
//
// 语义不变：拿不到就返回空串，由调用方按既有约定回退为 'unknown'（不伪造版本号）。
function readInjectedEnv(key: 'VITE_APP_VERSION' | 'VITE_APP_BUILD_TIME'): string {
  // `import.meta.env` 在 Vite 下是对象，在 esbuild/Node 下可能是 undefined。
  const env = (import.meta as { env?: Record<string, string | undefined> }).env
  if (!env || typeof env !== 'object') return ''
  return (env[key] ?? '').trim()
}

/** 构建期注入的 git commit SHA；未注入时为 'unknown'。 */
export const APP_VERSION: string = readInjectedEnv('VITE_APP_VERSION') || 'unknown'

/** 构建期注入的构建时间（UTC ISO）；未注入时为空串。 */
export const APP_BUILD_TIME: string = readInjectedEnv('VITE_APP_BUILD_TIME')

/** 短版本号（前 7 位），用于界面展示。 */
export const APP_VERSION_SHORT: string = APP_VERSION === 'unknown' ? 'unknown' : APP_VERSION.slice(0, 7)

/** 是否为「未注入版本」的构建。用于在界面上给出明确的「无法自证」提示。 */
export const APP_VERSION_UNKNOWN: boolean = APP_VERSION === 'unknown'

/**
 * 版本摘要文案。
 *
 * `unknown` 时不写「v0.0.0」这类假版本，而是明确说「未注入」并给出原因 ——
 * 否则用户会以为这是当前源码构建的，正是 issue #88 要消除的那种误判。
 */
export function versionSummary(): string {
  if (APP_VERSION_UNKNOWN) {
    return '未注入构建版本（构建时未传入 GIT_SHA，无法自证与源码的对应关系）'
  }
  return APP_BUILD_TIME ? `${APP_VERSION_SHORT}（构建于 ${APP_BUILD_TIME}）` : APP_VERSION_SHORT
}
