import type { Gateway } from './gateway.js'
import { itemRef } from './onepassword.js'

/*
 * Web API 客户端。
 *
 * 用来**准备**用例的前置状态（连接身份之类），断言仍然打在界面与出站流量上。
 * 每次都带齐三道检查要的东西：Origin、X-Requested-By、Bearer 令牌 ——
 * 少任何一个都会被 `internal/transport/httpapi/auth.go` 挡下。
 */

/** ApiResponse 是一次调用的结果，保留状态码供断言拒绝路径。 */
export interface ApiResponse<T> {
  readonly status: number
  readonly body: T
}

/** WebAPI 是绑定到某个 Gateway 实例的客户端。 */
export interface WebAPI {
  get<T>(path: string): Promise<ApiResponse<T>>
  post<T>(path: string, body: unknown): Promise<ApiResponse<T>>
  /** connectIdentity 用假 op 里的一份引用连接一个身份。 */
  connectIdentity(spec: ConnectSpec): Promise<ApiResponse<unknown>>
}

/** ConnectSpec 是一次连接身份的坐标。 */
export interface ConnectSpec {
  /** service 是已声明 Adapter 的服务名，例如 github。 */
  readonly service: string
  /** item 是假 op 保险库里的条目名。 */
  readonly item: string
  /** field 是条目里的字段名，决定取到哪个哨兵（见 onepassword.ts）。 */
  readonly field: string
  readonly accountLabel: string
  readonly environment?: 'production' | 'non-production'
  readonly isDefault?: boolean
}

export function webAPI(gateway: Gateway): WebAPI {
  const call = async <T>(method: string, path: string, body?: unknown): Promise<ApiResponse<T>> => {
    const response = await fetch(`${gateway.consoleURL}${path}`, {
      method,
      headers: {
        Origin: gateway.consoleURL,
        'X-Requested-By': 'opendelo-console',
        Authorization: `Bearer ${gateway.sessionToken}`,
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    const text = await response.text()
    return { status: response.status, body: (text === '' ? {} : JSON.parse(text)) as T }
  }

  return {
    get: (path) => call('GET', path),
    post: (path, body) => call('POST', path, body),
    connectIdentity: (spec) =>
      call('POST', '/v1/identities/connect', {
        provider_kind: '1password',
        provider_label: 'E2E 保险库',
        provider_item_ref: itemRef(spec.item),
        field: spec.field,
        service: spec.service,
        account_label: spec.accountLabel,
        environment: spec.environment ?? 'non-production',
        is_default: spec.isDefault ?? true,
      }),
  }
}
