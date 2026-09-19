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

/** 构建期注入的 git commit SHA；未注入时为 'unknown'。 */
export const APP_VERSION: string = (import.meta.env.VITE_APP_VERSION ?? '').trim() || 'unknown'

/** 构建期注入的构建时间（UTC ISO）；未注入时为空串。 */
export const APP_BUILD_TIME: string = (import.meta.env.VITE_APP_BUILD_TIME ?? '').trim()

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
