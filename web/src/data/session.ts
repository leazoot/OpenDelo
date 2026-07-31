/**
 * 会话令牌是 Console 访问 /v1 的唯一凭证（REQ-API-005）。
 *
 * Gateway 在下发入口文档时把它注入 <head> 的 meta —— 静态资源本身不要求令牌，
 * 否则 Console 还没拿到令牌就连首屏都取不到。CSP 里没有 'unsafe-inline'，
 * meta 是把令牌交到前端手上的唯一位置。
 */
const SESSION_TOKEN_META = 'opendelo-session-token'

/** 读取入口文档里的会话令牌。未注入时返回空串，由调用方决定如何呈现。 */
export function readSessionToken(from: Document = document): string {
  const meta = from.querySelector(`meta[name="${SESSION_TOKEN_META}"]`)
  return meta?.getAttribute('content') ?? ''
}
