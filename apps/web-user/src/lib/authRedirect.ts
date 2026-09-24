/**
 * Resolve the post-login destination without allowing an open redirect.
 *
 * The authenticated Atelier shell records the current in-app URL as `next`
 * when it sends an anonymous user to `/login`.  Keep the validation here so
 * every login entry point applies the same-origin rule instead of each caller
 * implementing a subtly different string check.
 */
export const DEFAULT_LOGIN_REDIRECT = '/today'

const INTERNAL_ORIGIN = 'https://atelier.internal'

function isInternalPath(value: string): boolean {
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\')) return false

  try {
    const resolved = new URL(value, INTERNAL_ORIGIN)
    return resolved.origin === INTERNAL_ORIGIN && resolved.pathname !== '/login'
  } catch {
    return false
  }
}

/** Return a safe in-app path from a login query string. */
export function resolveLoginRedirect(search: string, fallback = DEFAULT_LOGIN_REDIRECT): string {
  const candidate = new URLSearchParams(search).get('next')
  if (!candidate || !isInternalPath(candidate)) return fallback

  const resolved = new URL(candidate, INTERNAL_ORIGIN)
  return `${resolved.pathname}${resolved.search}${resolved.hash}` || fallback
}

/** Build the login URL used when an anonymous user hits a protected route. */
export function buildLoginPath(pathname: string, search = '', hash = ''): string {
  const target = `${pathname}${search}${hash}`
  const safeTarget = isInternalPath(target) ? target : DEFAULT_LOGIN_REDIRECT
  return `/login?${new URLSearchParams({ next: safeTarget }).toString()}`
}
