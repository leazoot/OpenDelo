import type { Gateway } from './gateway.js'

/*
 * Agent 侧的入口（8789 · MCP over HTTP）。
 *
 * 两件事：
 *
 *   1. **注册会话。** 九项身份绑定由一个可信的父进程如实提交
 *      （`internal/cli/agentsession.go` 的理由：Agent 自报等于没有绑定）。
 *      E2E 扮演的正是那个父进程，因此这一步走 8787 的会话令牌。
 *   2. **调用工具。** Session Key 走 `Mcp-Session-Id`，与真实客户端一致。
 *
 * 这里**不发 Origin**：MCP 面对带 Origin 的请求一律拒绝（浏览器发得出的东西
 * 都要挡住），真实客户端也不发。
 */

/** AgentIdentity 是九项绑定里 E2E 会变动的那几项。 */
export interface AgentIdentity {
  /** name 是 Agent 的名字，只用于展示 —— 单靠它不足以识别身份。 */
  readonly name?: string
  /** workspacePath 是工作区路径，决定这次请求属于哪个项目。 */
  readonly workspacePath?: string
  /** projectFingerprint 是项目指纹。它变了意味着同一路径下换了项目。 */
  readonly projectFingerprint?: string
}

/** AgentSession 是一次已注册的会话。 */
export interface AgentSession {
  readonly agentId: string
  /** sessionKey 是 Agent 的身份凭证，不是任何外部服务的凭据。 */
  readonly sessionKey: string
  /** call 发起一次工具调用。 */
  call(tool: string, args: Record<string, unknown>): Promise<ToolOutcome>
  /** listTools 取回这台网关声明的工具清单。 */
  listTools(): Promise<{ name: string }[]>
  /** disconnect 结束会话，收回名下「到任务结束」的授权。 */
  disconnect(): Promise<void>
  /**
   * confirm 把这个 Agent 标记为人已确认过的。
   *
   * 未确认的 Agent 发起写操作时**不允许自动放行**，即使已经学到过一条覆盖
   * 本次范围的记忆 —— 那道门与风险等级无关，学习也打不开它。
   * 因此凡是要验证「学过之后不再询问」的用例，都得先走这一步。
   */
  confirm(): Promise<void>
}

/** ToolOutcome 是一次工具调用的结论。 */
export interface ToolOutcome {
  /** refused 为真表示按策略拒绝 —— 那是产品的正常输出，不是故障。 */
  readonly refused: boolean
  /** text 是回给模型的那段文字。 */
  readonly text: string
  /** rpcError 在协议层失败时非空（离线、认证不通）。 */
  readonly rpcError: string
}

interface RPCResult {
  result?: {
    isError?: boolean
    content?: { type: string; text?: string }[]
    tools?: { name: string }[]
  }
  error?: { code: number; message: string }
}

/** registerAgent 注册一个 Agent 会话并返回它的 MCP 入口。 */
export async function registerAgent(
  gateway: Gateway,
  identity: AgentIdentity = {},
): Promise<AgentSession> {
  const workspacePath = identity.workspacePath ?? '/work/telecall'

  const response = await fetch(`${gateway.consoleURL}/v1/agents/register`, {
    method: 'POST',
    headers: {
      Origin: gateway.consoleURL,
      'X-Requested-By': 'opendelo-console',
      Authorization: `Bearer ${gateway.sessionToken}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      name: identity.name ?? 'e2e-agent',
      type: 'generic',
      version: '1.0.0',
      executable_hash: 'sha256:e2e0000000000000000000000000000000000000000000000000000000000000',
      executable_path: '/usr/local/bin/e2e-agent',
      pid: 4242,
      parent_pid: 4241,
      os_user: 'e2e',
      device_fingerprint: 'e2e-device',
      device_name: 'E2E Machine',
      workspace_path: workspacePath,
      project_fingerprint: identity.projectFingerprint ?? fingerprintOf(workspacePath),
      started_at: '2026-08-01T00:00:00.000Z',
    }),
  })

  if (response.status !== 201 && response.status !== 200) {
    throw new Error(`注册 Agent 失败：${String(response.status)} ${await response.text()}`)
  }
  const registered = (await response.json()) as {
    agent: { id: string }
    session_key: string
  }

  return session(gateway, registered.agent.id, registered.session_key)
}

/** consoleCall 从 Console 面发一次 POST，替人做那些 Agent 自己做不了的事。 */
async function consoleCall(gateway: Gateway, path: string, body?: unknown): Promise<void> {
  const response = await fetch(`${gateway.consoleURL}${path}`, {
    method: 'POST',
    headers: {
      Origin: gateway.consoleURL,
      'X-Requested-By': 'opendelo-console',
      Authorization: `Bearer ${gateway.sessionToken}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  if (!response.ok) {
    throw new Error(`${path} 失败：${String(response.status)} ${await response.text()}`)
  }
}

function fingerprintOf(workspacePath: string): string {
  return `fp-${workspacePath.replaceAll('/', '-')}`
}

function session(gateway: Gateway, agentId: string, sessionKey: string): AgentSession {
  let nextId = 1

  const rpc = async (method: string, params: unknown): Promise<RPCResult> => {
    const response = await fetch(gateway.mcpURL, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Mcp-Session-Id': sessionKey,
      },
      body: JSON.stringify({ jsonrpc: '2.0', id: nextId++, method, params }),
    })
    const text = await response.text()
    if (text === '') {
      throw new Error(`MCP ${method} 没有返回内容（HTTP ${String(response.status)}）`)
    }
    return JSON.parse(text) as RPCResult
  }

  return {
    agentId,
    sessionKey,

    call: async (tool, args) => {
      const answered = await rpc('tools/call', { name: tool, arguments: args })
      if (answered.error !== undefined) {
        return { refused: true, text: '', rpcError: answered.error.message }
      }
      const content = answered.result?.content ?? []
      return {
        refused: answered.result?.isError === true,
        text: content.map((part) => part.text ?? '').join(''),
        rpcError: '',
      }
    },

    listTools: async () => {
      const answered = await rpc('tools/list', {})
      return answered.result?.tools ?? []
    },

    confirm: async () => {
      await consoleCall(gateway, `/v1/agents/${agentId}/trust`, { confirmed: true })
    },

    disconnect: async () => {
      await consoleCall(gateway, `/v1/agents/${agentId}/disconnect`)
    },
  }
}
