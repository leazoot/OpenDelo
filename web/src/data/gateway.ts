import { readSessionToken } from './session'

/**
 * Gateway 要求 Console 带上这个自定义头。
 *
 * 简单请求发不出自定义头，浏览器会先发预检；Gateway 不下发任何 CORS 响应头，
 * 预检因此不会被放行。<img>、<script> 这类资源加载同样带不上它。
 */
const REQUESTED_BY_HEADER = 'X-Requested-By'
const REQUESTED_BY = 'opendelo-console'

/** 所有 Web API 都在这个前缀之下（REQ-API-001）。 */
const API_PREFIX = '/v1/'

/** Gateway 返回的错误（REQ-API-003）。 */
export class GatewayError extends Error {
  readonly status: number
  readonly code: string
  readonly operationId: string
  /**
   * 校验失败时出问题的字段名（REQ-CAP-001 AC1）。
   *
   * 只有 400 会带。表单据此把提示指到那一行 —— 错误正文本身是脱敏后的
   * 通用句子，它说不出是哪一项填错了。
   */
  readonly fields: readonly string[]

  constructor(
    status: number,
    code: string,
    message: string,
    operationId: string,
    fields: readonly string[] = [],
  ) {
    super(message)
    this.name = 'GatewayError'
    this.status = status
    this.code = code
    this.operationId = operationId
    this.fields = fields
  }
}

export interface GatewayRequestOptions {
  /** 默认 GET。状态变更一律用 POST / PATCH / DELETE。 */
  readonly method?: 'GET' | 'POST' | 'PATCH' | 'DELETE'
  readonly signal?: AbortSignal
  /**
   * 请求体，序列化为 JSON。
   *
   * 只在确实需要时给出：Gateway 拒绝未知字段，多带一个键会让整个请求 400。
   * 值是 unknown 而不是 string —— 偏好那个端点收的是一层嵌套的键值表。
   */
  readonly body?: Readonly<Record<string, unknown>>
  /** 覆盖会话令牌，只在测试里用。 */
  readonly token?: string
  /** 覆盖 fetch，只在测试里用。 */
  readonly fetchImpl?: typeof fetch
}

/**
 * 向 Gateway 发一个请求，带上会话令牌与自定义头。
 *
 * 令牌只走请求头：URL 会进浏览器历史、Referer 与服务端访问日志（REQ-API-005）。
 * 返回值是未经校验的 JSON，调用方负责用 schema 解析后再使用
 */
export async function requestGateway(path: string, options: GatewayRequestOptions = {}): Promise<unknown> {
  if (!path.startsWith(API_PREFIX)) {
    throw new GatewayError(0, 'invalid_request', `Gateway 请求路径必须以 ${API_PREFIX} 开头，收到 ${path}。`, '')
  }

  const response = await sendGateway(path, options)
  const body = await readJson(response, path)

  if (!response.ok) {
    throw toGatewayError(response.status, body)
  }
  return body
}

/**
 * 发一个带凭证的 Gateway 请求，返回原始响应。
 *
 * SSE 需要的是流本身而不是解析后的 JSON（见 `eventStream.ts`），因此这一段
 * 从 requestGateway 里分出来 —— 让两条路各带一份请求头是漏掉其中一个头的开始。
 */
export async function sendGateway(path: string, options: GatewayRequestOptions = {}): Promise<Response> {
  const token = options.token ?? readSessionToken()
  if (token === '') {
    // 没有令牌就不发请求：让它撞上 401 只会把「令牌没注入」伪装成「认证失败」。
    throw new GatewayError(0, 'unauthenticated', '入口文档里没有会话令牌，请重新打开 Console。', '')
  }

  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
    [REQUESTED_BY_HEADER]: REQUESTED_BY,
  }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  const request: RequestInit = {
    method: options.method ?? 'GET',
    headers,
    // 认证只有令牌一条路径。同时带上 Cookie 会让「哪一套在生效」说不清楚。
    credentials: 'omit',
  }
  if (options.signal !== undefined) {
    request.signal = options.signal
  }
  if (options.body !== undefined) {
    request.body = JSON.stringify(options.body)
  }

  const send = options.fetchImpl ?? globalThis.fetch
  return send(path, request)
}

async function readJson(response: Response, path: string): Promise<unknown> {
  const text = await response.text()
  if (text === '') {
    return null
  }
  try {
    const parsed: unknown = JSON.parse(text)
    return parsed
  } catch (cause) {
    throw new GatewayError(
      response.status,
      'internal',
      `${path} 的响应不是合法 JSON：${cause instanceof Error ? cause.message : String(cause)}`,
      '',
    )
  }
}

function toGatewayError(status: number, body: unknown): GatewayError {
  if (isErrorEnvelope(body)) {
    return new GatewayError(
      status,
      body.error.code,
      body.error.message,
      body.error.operation_id,
      fieldsOf(body),
    )
  }
  return new GatewayError(status, 'internal', `Gateway 返回了 ${String(status)}，响应结构不符合错误契约。`, '')
}

interface ErrorEnvelope {
  readonly error: {
    readonly code: string
    readonly message: string
    readonly operation_id: string
  }
  readonly fields?: unknown
}

/**
 * 取出错误体里的字段名。
 *
 * fields 与 error 平级而不是嵌在里面：apperr 的对外 message 只能取自码表，
 * 往 error 对象里加键会让「错误体长什么样」出现第二种形状。
 * 缺失或形状不对时当作没有，不让它把整个错误吞掉。
 */
function fieldsOf(body: ErrorEnvelope): readonly string[] {
  if (!('fields' in body) || !Array.isArray(body.fields)) {
    return []
  }
  return body.fields.filter((name): name is string => typeof name === 'string')
}

/** 用类型守卫而不是断言：断言只是让类型检查闭嘴，运行时的形状并没有被验证。 */
function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== 'object' || value === null || !('error' in value)) {
    return false
  }
  const { error } = value
  return (
    typeof error === 'object' &&
    error !== null &&
    'code' in error &&
    typeof error.code === 'string' &&
    'message' in error &&
    typeof error.message === 'string' &&
    'operation_id' in error &&
    typeof error.operation_id === 'string'
  )
}
