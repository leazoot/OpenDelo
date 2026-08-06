import { test, expect } from '../harness/fixtures.js'
import type { WebAPI } from '../harness/api.js'
import { allSentinels, sentinelToken } from '../harness/sentinel.js'
import type { ApprovalView, AuditEventView, ListOf } from '../harness/views.js'

/*
 * 安全验收（REQ-NFR-002 的五个零指标 + `.claude/rules/security.md` §15）。
 *
 * 与 success-criteria 分开是因为问的问题不同：那边问「产品做到了吗」，
 * 这边问「有没有哪一面漏了」。断言全部是**否定式**的，而否定式断言最容易
 * 因为「压根没发生过」而假通过 —— 所以每一条之前都先证明那件事真的发生过。
 */

const gitHubIdentity = { service: 'github', item: 'GitHub Bot', field: 'token' } as const
const repository = { owner: 'octocat', repo: 'hello-world' }

// ------------------------------------------------ 八个面

test('八个面都扫不到凭据', async ({ api, agent, gateway, external, page }) => {
  await connect(api)
  const session = await agent()
  await session.confirm()

  // 先让凭据真的流动一次：低风险读取会走完取用 → 注入 → 出站。
  const read = await session.call('github.repository.read', repository)
  expect(read.refused).toBe(false)
  // 出站那一面上它**必须**在 —— 这一条不成立的话下面八条全是空转。
  expect(external.github.received()[0]?.headers['authorization']).toBe(`Bearer ${sentinelToken}`)

  // 再让一次中风险停在缝前，好让「审批信息」这一面有东西可扫。
  await session.call('github.pull_request.create', {
    ...repository,
    title: '一个 PR',
    head: 'feature',
    base: 'main',
  })
  const waiting = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  expect(waiting.body.items.length).toBeGreaterThan(0)

  // ① Agent 上下文：回给模型的那段文字。
  clean(read.text, 'Agent 上下文')
  // ② 环境变量：`opendelo run` 交给子进程的那一份，由子进程自己打印出来。
  const child = await gateway.run(['/bin/sh', '-c', 'env'])
  expect(child.code).toBe(0)
  expect(child.output).toContain('OPENDELO_SESSION_KEY')
  clean(child.output, 'Agent 的环境变量')
  // ③ MCP 响应：工具清单。
  clean(JSON.stringify(await session.listTools()), 'MCP 响应')
  // ④ 日志（debug 级别，没有「调试模式输出完整信息」这种开关）。
  clean(gateway.output(), 'Gateway 日志')
  // ⑤ 临时文件：整个数据目录，逐字节读。
  clean(await readEveryFile(gateway.configDir), '数据目录里的文件')
  // ⑥ Console DOM。
  await page.goto('/gate')
  clean(await page.content(), 'Console DOM')
  // ⑦ 审批信息。
  clean(JSON.stringify(waiting.body), '审批信息')
  // ⑧ 调试输出：账本与它的导出。
  clean(JSON.stringify((await api.get<unknown>('/v1/audit-events')).body), '账本')
  // 导出是 JSONL / CSV，不是 JSON：按原样的字节扫，别先解析一遍。
  clean(await consoleText(gateway, '/v1/audit-events/export'), '账本导出')
})

// ------------------------------------------------ 五个零指标

test('零指标一 · Secret 暴露事件为零', async ({ api, agent, gateway }) => {
  // 与上一条同源，这里单独立一个断言点，使「五个零指标各有断言」可逐条核对。
  await connect(api)
  const session = await agent()
  expect((await session.call('github.repository.read', repository)).refused).toBe(false)

  clean(gateway.output(), 'Gateway 日志')
  clean(JSON.stringify((await api.get<unknown>('/v1/audit-events')).body), '账本')
})

test('零指标二 · 高风险自动批准事件为零', async ({ api, agent, external }) => {
  await connect(api)
  const session = await agent()
  await session.confirm()

  // 同一个高风险操作连做三次。任何一次自动放行都会在账本里留下 auto_allow。
  for (const attempt of [1, 2, 3]) {
    const refused = await session.call('github.repository.delete', repository)
    expect(refused.refused, `第 ${String(attempt)} 次没有停下来`).toBe(true)
  }

  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  const automatic = ledger.body.items.filter(
    (event) => event.verdict === 'auto_allow' && event.operation === 'delete_repository',
  )
  expect(automatic).toEqual([])
  expect(external.github.received()).toEqual([])
})

test('零指标三 · 超范围执行事件为零', async ({ api, agent, external }) => {
  await connect(api)
  const session = await agent()
  await session.confirm()

  // 学到的是「这个仓库上的 create_issue」。
  expect((await session.call('github.issue.create', { ...repository, title: '第一次' })).refused).toBe(
    true,
  )
  await settle(api, await pendingID(api), 'allow-project')
  expect((await session.call('github.issue.create', { ...repository, title: '第二次' })).refused).toBe(
    false,
  )

  const sentSoFar = external.github.received().length

  // 换一个仓库、换一个操作，两者都在学到的范围之外。
  const elsewhere = await session.call('github.issue.create', {
    owner: 'octocat',
    repo: 'another-repo',
    title: '别的仓库',
  })
  const wider = await session.call('github.pull_request.create', {
    ...repository,
    title: 'PR',
    head: 'f',
    base: 'main',
  })

  expect(elsewhere.refused).toBe(true)
  expect(wider.refused).toBe(true)
  expect(external.github.received()).toHaveLength(sentSoFar)
})

test('零指标四 · 未审计请求为零', async ({ api, agent, gateway, external }) => {
  await connect(api)
  const session = await agent()
  await session.confirm()

  // 三种结局各来一次：放行、停在缝前、直接拒绝。
  await session.call('github.repository.read', repository)
  await session.call('github.pull_request.create', {
    ...repository,
    title: 'PR',
    head: 'f',
    base: 'main',
  })
  await session.call('github.repository.delete', repository)

  // 代理面也来一次：认不出主机的请求同样要留下记录。
  const target = new URL(external.github.baseURL)
  await fetch(`${gateway.proxyURL}/repos/octocat/hello-world`, {
    headers: { Host: target.host },
  }).catch(() => undefined)

  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  const kinds = new Set(ledger.body.items.map((event) => event.type))
  expect(kinds.has('adapter.executed')).toBe(true)
  expect(kinds.has('decision.denied')).toBe(true)

  // 每一条都带着 operation_id：没有它，这次请求在账本里定位不到，
  // 「已审计」就只是一条查不回去的记录。
  for (const event of ledger.body.items) {
    expect(event.operation_id, JSON.stringify(event)).not.toBe('')
  }
})

test('零指标五 · Gateway 失败后直连为零', async ({ api, agent, gateway, external }) => {
  await connect(api)
  const session = await agent()
  expect((await session.call('github.repository.read', repository)).refused).toBe(false)
  const sentSoFar = external.github.received().length

  await gateway.stop()

  // 网关停了之后，Agent 侧的两条路都必须是死的，且都不产生出站。
  await session.call('github.repository.read', repository).catch(() => undefined)
  await fetch(`${gateway.proxyURL}/repos/octocat/hello-world`).catch(() => undefined)

  expect(external.github.received()).toHaveLength(sentSoFar)
})

// ------------------------------------------------ 注入与边界

test('伪造的 Origin 被拒，且拒绝里不含令牌', async ({ gateway }) => {
  const forged = await fetch(`${gateway.consoleURL}/v1/identities`, {
    headers: {
      Origin: 'http://evil.example',
      'X-Requested-By': 'opendelo-console',
      Authorization: `Bearer ${gateway.sessionToken}`,
    },
  })

  expect(forged.status).toBe(403)
  const body = await forged.text()
  expect(body).not.toContain(gateway.sessionToken)
})

test('缺少自定义头的简单请求被拒', async ({ gateway }) => {
  const simple = await fetch(`${gateway.consoleURL}/v1/identities`, {
    headers: { Authorization: `Bearer ${gateway.sessionToken}` },
  })

  expect(simple.status).toBe(403)
})

test('Agent 拿不到审批与配置端点', async ({ gateway, agent }) => {
  const session = await agent()

  // 用 Agent 的 Session Key 去敲 Console 面。它不是会话令牌，认证就过不去；
  // 就算过去了，那几个端点对 Agent 也是 403。
  for (const path of ['/v1/approvals', '/v1/identities', '/v1/preferences']) {
    const refused = await fetch(`${gateway.consoleURL}${path}`, {
      headers: {
        'X-Requested-By': 'opendelo-console',
        Authorization: `Bearer ${session.sessionKey}`,
      },
    })
    expect(refused.status, path).toBeGreaterThanOrEqual(400)
  }
})

test('外部来的字符串在 Console 上是文本，不是标记', async ({ api, agent, page }) => {
  await connect(api)
  const session = await agent()
  await session.confirm()

  // 资源名里塞一段标记。它会一路进到审批项，再被 Gate 页面渲染出来。
  const injected = '<img src=x onerror=alert(1)>'
  await session.call('github.pull_request.create', {
    owner: injected,
    repo: injected,
    title: 'PR',
    head: 'f',
    base: 'main',
  })

  await page.goto('/gate')
  await expect(page.getByText(injected, { exact: false }).first()).toBeVisible()
  // 转义之后它是文本节点，不会变成一个真的 <img>。
  expect(await page.locator('img[onerror]').count()).toBe(0)
})

// ------------------------------------------------ 辅助

async function connect(api: WebAPI): Promise<void> {
  const connected = await api.connectIdentity({
    ...gitHubIdentity,
    accountLabel: 'work',
  })
  expect(connected.status, JSON.stringify(connected.body)).toBe(201)
}

async function pendingID(api: WebAPI): Promise<string> {
  const listed = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  const waiting = listed.body.items.filter((item) => item.status === 'pending')
  expect(waiting).toHaveLength(1)
  return waiting[0]?.id ?? ''
}

async function settle(api: WebAPI, approvalID: string, action: string): Promise<void> {
  const decided = await api.post<unknown>(`/v1/approvals/${approvalID}/${action}`, {})
  expect(decided.status, JSON.stringify(decided.body)).toBe(200)
}

/** consoleText 从 Console 面取回一段原样的响应体。 */
async function consoleText(
  gateway: { consoleURL: string; sessionToken: string },
  path: string,
): Promise<string> {
  const response = await fetch(`${gateway.consoleURL}${path}`, {
    headers: {
      Origin: gateway.consoleURL,
      'X-Requested-By': 'opendelo-console',
      Authorization: `Bearer ${gateway.sessionToken}`,
    },
  })
  return response.text()
}

/** clean 断言一段内容里不含任何哨兵。 */
function clean(content: string, face: string): void {
  for (const sentinel of allSentinels) {
    expect(content.includes(sentinel), `${face}上出现了凭据`).toBe(false)
  }
}

/** readEveryFile 把一个目录下的每一个文件按字节读出来拼在一起。 */
async function readEveryFile(dir: string): Promise<string> {
  const { readdir, readFile } = await import('node:fs/promises')
  const { join } = await import('node:path')

  const entries = await readdir(dir, { recursive: true, withFileTypes: true })
  const contents: string[] = []
  for (const entry of entries) {
    if (!entry.isFile()) {
      continue
    }
    contents.push((await readFile(join(entry.parentPath, entry.name))).toString('latin1'))
  }
  return contents.join('\n')
}
