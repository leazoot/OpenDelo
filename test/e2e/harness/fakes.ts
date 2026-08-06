import http from 'node:http'
import type { AddressInfo } from 'node:net'

/*
 * 假的外部服务（GitHub / Cloudflare / 模型）。
 *
 * `.claude/rules/security.md` §13.5 禁止对真实外部账户发起任何操作，
 * `.claude/rules/testing.md` §5 要求外部 HTTP 服务用 fake 而不是 mock 断言。
 * 这里两条一起满足：形状照着真实响应写，行为是确定的，而且**每一次到达的请求
 * 都被完整记下来** —— S8「Secret 不可见」要比对的正是出站请求的内容。
 *
 * 它们只监听 127.0.0.1。E2E 因此不需要、也不允许有任何外部网络访问。
 */

/** RecordedRequest 是一次到达假服务的请求，含请求头与正文。 */
export interface RecordedRequest {
  readonly method: string
  readonly path: string
  readonly headers: Readonly<Record<string, string>>
  readonly body: string
}

/** FakeService 是一个已经在监听的假服务。 */
export interface FakeService {
  /** baseURL 是交给 Gateway 的出站地址。 */
  readonly baseURL: string
  /** received 是到目前为止收到的全部请求，按到达顺序。 */
  received(): readonly RecordedRequest[]
  close(): Promise<void>
}

/** Route 把一次请求翻译成响应。返回 undefined 表示这条路由不认这次请求。 */
type Route = (request: RecordedRequest) => Reply | undefined

interface Reply {
  readonly status: number
  readonly body: unknown
}

/**
 * startFake 起一个假服务。
 *
 * 认不出的请求返回 404 而不是兜底的 200：一个「什么都答应」的假服务会让
 * 「Adapter 拼错了路径」这类缺陷在用例里看起来是通过的。
 */
async function startFake(pathPrefix: string, routes: readonly Route[]): Promise<FakeService> {
  const received: RecordedRequest[] = []

  const server = http.createServer((request, response) => {
    collect(request)
      .then((body) => {
        const arrived: RecordedRequest = {
          method: request.method ?? '',
          path: request.url ?? '',
          headers: headersOf(request),
          body,
        }
        received.push(arrived)

        const reply = answer(routes, arrived)
        response.writeHead(reply.status, { 'content-type': 'application/json' })
        response.end(JSON.stringify(reply.body))
      })
      .catch((cause: unknown) => {
        response.writeHead(500, { 'content-type': 'application/json' })
        response.end(JSON.stringify({ fake_error: String(cause) }))
      })
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })

  const address = server.address() as AddressInfo
  return {
    baseURL: `http://127.0.0.1:${String(address.port)}${pathPrefix}`,
    received: () => received,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.closeAllConnections()
        server.close((err) => (err ? reject(err) : resolve()))
      }),
  }
}

function answer(routes: readonly Route[], request: RecordedRequest): Reply {
  for (const route of routes) {
    const reply = route(request)
    if (reply !== undefined) {
      return reply
    }
  }
  return { status: 404, body: { fake_error: `假服务没有 ${request.method} ${request.path} 这条路由` } }
}

function collect(request: http.IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    request.on('data', (chunk: Buffer) => chunks.push(chunk))
    request.on('error', reject)
    request.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
  })
}

function headersOf(request: http.IncomingMessage): Record<string, string> {
  const flattened: Record<string, string> = {}
  for (const [name, value] of Object.entries(request.headers)) {
    if (value === undefined) {
      continue
    }
    flattened[name] = Array.isArray(value) ? value.join(', ') : value
  }
  return flattened
}

/** on 构造一条按方法与路径正则匹配的路由。 */
function on(method: string, pattern: RegExp, body: (matched: RegExpMatchArray) => unknown, status = 200): Route {
  return (request) => {
    if (request.method !== method) {
      return undefined
    }
    const matched = pathOf(request.path).match(pattern)
    return matched === null ? undefined : { status, body: body(matched) }
  }
}

function pathOf(target: string): string {
  return target.split('?')[0] ?? target
}

/**
 * startGitHub 起 GitHub REST API 的假服务。
 *
 * 覆盖的是 E2E 会用到的几条：读仓库、读 Issue、建 Issue、建 PR、删仓库。
 * 其余能力落到 404，需要时按同样的形状补。
 */
export function startGitHub(): Promise<FakeService> {
  return startFake('', [
    on('GET', /^\/repos\/([^/]+)\/([^/]+)$/, (matched) => ({
      id: 1296269,
      name: matched[2],
      full_name: `${matched[1] ?? ''}/${matched[2] ?? ''}`,
      private: false,
      default_branch: 'main',
      description: '假服务返回的仓库',
    })),
    on('GET', /^\/repos\/([^/]+)\/([^/]+)\/issues\/(\d+)$/, (matched) => ({
      number: Number(matched[3]),
      title: '假服务返回的 Issue',
      state: 'open',
      body: '正文',
    })),
    on(
      'POST',
      /^\/repos\/([^/]+)\/([^/]+)\/issues$/,
      () => ({ number: 42, state: 'open', html_url: 'https://example.invalid/issues/42' }),
      201,
    ),
    on(
      'POST',
      /^\/repos\/([^/]+)\/([^/]+)\/pulls$/,
      () => ({ number: 7, state: 'open', html_url: 'https://example.invalid/pull/7' }),
      201,
    ),
    on('DELETE', /^\/repos\/([^/]+)\/([^/]+)$/, () => ({}), 204),
  ])
}

/**
 * startCloudflare 起 Cloudflare API v4 的假服务。
 *
 * 真实 API 的成功响应统一包在 `{success, errors, messages, result}` 里，
 * 这里照抄那个外壳 —— 形状不对的话 Adapter 的解析路径就没被测到。
 */
export function startCloudflare(): Promise<FakeService> {
  const wrap = (result: unknown): unknown => ({ success: true, errors: [], messages: [], result })

  return startFake('/client/v4', [
    on('GET', /\/zones\/([^/]+)$/, (matched) =>
      wrap({ id: matched[1], name: 'example.invalid', status: 'active' }),
    ),
    on('GET', /\/zones\/([^/]+)\/dns_records$/, () =>
      wrap([{ id: 'record-1', type: 'A', name: 'www.example.invalid', content: '203.0.113.10' }]),
    ),
    on('GET', /\/zones\/([^/]+)\/dns_records\/([^/]+)$/, (matched) =>
      wrap({ id: matched[2], type: 'A', name: 'www.example.invalid', content: '203.0.113.10' }),
    ),
    on('POST', /\/zones\/([^/]+)\/dns_records$/, () => wrap({ id: 'record-2', type: 'A' }), 201),
    on('PUT', /\/zones\/([^/]+)\/dns_records\/([^/]+)$/, (matched) => wrap({ id: matched[2], type: 'A' })),
    on('DELETE', /\/zones\/([^/]+)\/dns_records\/([^/]+)$/, (matched) => wrap({ id: matched[2] })),
    on('DELETE', /\/zones\/([^/]+)$/, (matched) => wrap({ id: matched[1] })),
  ])
}

/** startOpenAI 起 OpenAI Chat Completions 的假服务。 */
export function startOpenAI(): Promise<FakeService> {
  return startFake('/v1', [
    on('GET', /\/models$/, () => ({
      object: 'list',
      data: [{ id: 'gpt-4o-mini', object: 'model', owned_by: 'fake' }],
    })),
    on('POST', /\/chat\/completions$/, () => ({
      id: 'chatcmpl-fake',
      object: 'chat.completion',
      choices: [{ index: 0, message: { role: 'assistant', content: '假服务的回答' }, finish_reason: 'stop' }],
    })),
  ])
}

/** startAnthropic 起 Anthropic Messages 的假服务。 */
export function startAnthropic(): Promise<FakeService> {
  return startFake('/v1', [
    on('GET', /\/models$/, () => ({
      data: [{ id: 'claude-sonnet-5', type: 'model', display_name: 'Fake Sonnet' }],
    })),
    on('POST', /\/messages$/, () => ({
      id: 'msg_fake',
      type: 'message',
      role: 'assistant',
      content: [{ type: 'text', text: '假服务的回答' }],
      stop_reason: 'end_turn',
    })),
  ])
}

/** ExternalServices 是一整套已经在监听的假外部服务。 */
export interface ExternalServices {
  readonly github: FakeService
  readonly cloudflare: FakeService
  readonly openai: FakeService
  readonly anthropic: FakeService
  /** all 按服务名列出全部假服务，供逐个扫描出站内容。 */
  readonly all: ReadonlyMap<string, FakeService>
  close(): Promise<void>
}

/** startExternalServices 起全部四个假服务。 */
export async function startExternalServices(): Promise<ExternalServices> {
  const [github, cloudflare, openai, anthropic] = await Promise.all([
    startGitHub(),
    startCloudflare(),
    startOpenAI(),
    startAnthropic(),
  ])

  const all = new Map<string, FakeService>([
    ['github', github],
    ['cloudflare', cloudflare],
    ['openai', openai],
    ['anthropic', anthropic],
  ])

  return {
    github,
    cloudflare,
    openai,
    anthropic,
    all,
    close: async () => {
      await Promise.all([...all.values()].map((service) => service.close()))
    },
  }
}
