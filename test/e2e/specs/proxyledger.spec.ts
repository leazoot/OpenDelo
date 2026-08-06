import { request as httpRequest } from 'node:http'
import { test, expect } from '../harness/fixtures.js'
import { connect } from '../harness/prepare.js'
import type { WebAPI } from '../harness/api.js'
import type { AuditEventView, ListOf } from '../harness/views.js'

/*
 * 经代理（8788）的成功转发要在账本上留下执行事件（R-47）。
 *
 * 单测守的是「Proxy 会调 RecordExecuted」与「组装根写进去的是什么形状」，
 * 这里守的是**这条路整段接得上**：真网关、真代理面、真账本，出站落在本地假服务上。
 * 缺了它，两端各自绿着而中间那根线断了也没人知道。
 *
 * 代理面自己不跑决策（REQ-PROXY-002），它只问「有没有一条已签发的 Lease 罩着」。
 * 而 Lease 只在人做过决定之后才存在 —— 低风险读操作自动放行时**不签发 Lease**
 * （这一条是写这个用例时量出来的，不是猜的）。所以要先让一次写操作停在缝前、
 * 替人点头，代理这一侧才有东西可匹配。
 */

const repository = { owner: 'octocat', repo: 'hello-world' }
const identity = { service: 'github', item: 'GitHub Bot', field: 'token', accountLabel: 'work' }

/**
 * viaProxy 用绝对形式的请求行经 8788 发一次请求。
 *
 * 不能用 fetch：代理面只接受绝对形式（`POST http://host/path HTTP/1.1`），
 * 而 fetch 发的是源形式加一个 Host 头 —— 那条路在 targetOf 里就被拒了，
 * 于是用例会以「请求没有指明目标主机」通过，什么也没验到。
 */
function viaProxy(
  proxyURL: string,
  method: string,
  absoluteURL: string,
  sessionKey: string,
  body: string,
): Promise<{ status: number }> {
  const proxy = new URL(proxyURL)
  return new Promise((resolve, reject) => {
    const call = httpRequest(
      {
        host: proxy.hostname,
        port: proxy.port,
        method,
        path: absoluteURL,
        headers: {
          'Proxy-Authorization': sessionKey,
          'Content-Type': 'application/json',
          'Content-Length': String(Buffer.byteLength(body)),
        },
      },
      (response) => {
        response.resume()
        response.on('end', () => {
          resolve({ status: response.statusCode ?? 0 })
        })
      },
    )
    call.on('error', reject)
    call.write(body)
    call.end()
  })
}

/** approveOnlyPending 替人批准此刻唯一那条待决项。 */
async function approveOnlyPending(api: WebAPI, action = 'allow-task'): Promise<void> {
  const waiting = await api.get<ListOf<{ id: string; status: string }>>('/v1/approvals')
  const pending = waiting.body.items.filter((item) => item.status === 'pending')
  expect(pending, '缝前没有待决项 —— 那次写操作没有停下来').toHaveLength(1)
  const only = pending[0]
  if (only === undefined) return
  const decided = await api.post<unknown>(`/v1/approvals/${only.id}/${action}`, {})
  expect(decided.status, JSON.stringify(decided.body)).toBe(200)
}

/** executionsIn 取回账本上的全部执行事件。 */
async function executionsIn(api: WebAPI): Promise<AuditEventView[]> {
  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  return ledger.body.items.filter((event) => event.type === 'adapter.executed')
}

test('经代理的成功转发在账本上留下一条 adapter.executed', async ({
  api,
  agent,
  gateway,
  external,
}) => {
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  // 写操作停在缝前，替人点头 —— 这一步签出的 Lease 正是代理那一侧要匹配的东西。
  const stopped = await session.call('github.issue.create', { ...repository, title: '经代理' })
  expect(stopped.refused, '写操作没有停在缝前').toBe(true)
  await approveOnlyPending(api)

  const before = await executionsIn(api)
  const sentBefore = external.github.received().length

  const target = new URL(external.github.baseURL)
  const forwarded = await viaProxy(
    gateway.proxyURL,
    'POST',
    `http://${target.host}/repos/${repository.owner}/${repository.repo}/issues`,
    session.sessionKey,
    JSON.stringify({ title: '经代理' }),
  )

  expect(forwarded.status, '代理没有放行这次请求').toBe(200)
  expect(
    external.github.received().length,
    '代理答了 200，外部假服务却没收到请求 —— 这个 200 是假的',
  ).toBe(sentBefore + 1)

  const after = await executionsIn(api)
  expect(
    after.length,
    '账本上执行事件的条数没变 —— 经代理的转发仍然是无痕的（R-47）',
  ).toBe(before.length + 1)

  const viaProxyEvent = after.find((event) => event.metadata['face'] === 'proxy')
  expect(viaProxyEvent, '账本上没有一条标着 face=proxy 的执行').toBeDefined()
  if (viaProxyEvent === undefined) return

  expect(viaProxyEvent.service).toBe('github')
  expect(viaProxyEvent.operation).toBe('create_issue')
  expect(viaProxyEvent.outcome).toBe('succeeded')
  // 「凭什么发出去的」是事后唯一要问的问题。
  expect(viaProxyEvent.lease_id, '执行事件追不回到那份授权').not.toBe('')
  expect(viaProxyEvent.operation_id).not.toBe('')
  // 记的是上游真答的那个数字，不是给 Agent 的 200/502 映射（R-44）。
  expect(viaProxyEvent.response_status).toBe(201)
  // 耗时是量出来的，不是填的：0 在账本上读起来是「这次没花时间」（R-48）。
  expect(
    viaProxyEvent.duration_ms,
    '经代理的执行耗时记成了 0 —— 账本上它与「没测」分不开',
  ).toBeGreaterThan(0)
  expect(viaProxyEvent.metadata['path']).toBe(`/repos/${repository.owner}/${repository.repo}/issues`)
  // 正文不进账本（PRD §22.1）：元数据只有方法、主机、路径与面。
  expect(Object.keys(viaProxyEvent.metadata).sort()).toEqual(['face', 'host', 'method', 'path'])
})

test('账本上两个面的执行除 face 外看不出区别', async ({ api, agent, gateway, external }) => {
  // mcpcalls.record 里那句「同一条链路上，8788 与 8789 的执行在账本里除此之外
  // 看不出区别」写在 face 字段旁边 —— 而 8788 此前从来没写过任何执行事件。
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  const stopped = await session.call('github.issue.create', { ...repository, title: '两个面' })
  expect(stopped.refused).toBe(true)
  // 这里必须是 allow-project 而不是 allow-task：后者只签一条会话绑定的 Lease，
  // 而决策链路目前不读已签发的 Lease（R-39），MCP 那条路会再问一次人。
  // 代理那一侧要的是 Lease，MCP 那一侧要的是记忆，allow-project 两样都留下。
  await approveOnlyPending(api, 'allow-project')

  // **先走代理，再走 MCP。** 顺序是必需的：批准签出的那条授权次数上限是 1
  // （`scope.DefaultRequestLimit`），而决策链路现在会优先复用已签发的授权而不是
  // 另签一条（D-17）。反过来的话 MCP 先把它用掉，代理这一侧就没有可匹配的了。
  const target = new URL(external.github.baseURL)
  await viaProxy(
    gateway.proxyURL,
    'POST',
    `http://${target.host}/repos/${repository.owner}/${repository.repo}/issues`,
    session.sessionKey,
    JSON.stringify({ title: '两个面' }),
  )

  // 那条授权已经用满，这一次由 allow-project 留下的记忆放行并另签一条。
  const viaMcp = await session.call('github.issue.create', { ...repository, title: '两个面' })
  expect(viaMcp.refused, '批准之后 MCP 那条路仍然不通').toBe(false)

  const executions = await executionsIn(api)
  expect(executions.length, '两个面各该留下一条执行').toBe(2)

  const [one, other] = executions
  if (one === undefined || other === undefined) return
  for (const field of ['service', 'operation', 'verdict', 'outcome'] as const) {
    expect(one[field], `${field} 在两个面上不一致`).toBe(other[field])
  }
  // lease_id **本就该不同**：MCP 那次由记忆放行、自己签一条 Lease，
  // 代理那次匹配的是批准时签出的那条。两条执行各自指得回自己的授权就够了 ——
  // 要求它们相同是把「同形」误当成「同一次」。
  for (const event of executions) {
    expect(event.lease_id, '执行事件追不回到任何授权').not.toBe('')
  }
  // face 是唯一该不同的那一项。
  const faces = executions.map((event) => event.metadata['face'])
  expect(new Set(faces).size, '两条执行的 face 相同 —— 分不出是哪个面发的').toBe(2)
})
