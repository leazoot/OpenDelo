import { test, expect } from '../harness/fixtures.js'
import { connect } from '../harness/prepare.js'
import type { WebAPI } from '../harness/api.js'
import type { ApprovalView, AuditEventView, ListOf, TrustMemoryView } from '../harness/views.js'

/*
 * 「允许到任务结束」在同一会话里不再重复问人（D-17，关闭 R-39）。
 *
 * 这个决定签出一条绑定本次会话的 Lease，但**不生成记忆**。决策链路此前只读记忆，
 * 于是同一会话里下一次同样的调用又要重新问一遍 —— 而用户明明已经为这个确切范围
 * 点过头了。这条 E2E 走的是真网关：批准一次，第二次同样的调用直接通过。
 *
 * 它是决策链路上新增的一个放行出口，因此另外三条用例全在它不该放行的时候。
 */

const repository = { owner: 'octocat', repo: 'hello-world' }
const identity = { service: 'github', item: 'GitHub Bot', field: 'token', accountLabel: 'work' }

async function onlyPending(api: WebAPI): Promise<string> {
  const listed = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  const waiting = listed.body.items.filter((item) => item.status === 'pending')
  expect(waiting, '缝前的待决项不是恰好一条').toHaveLength(1)
  return waiting[0]?.id ?? ''
}

async function settle(api: WebAPI, approvalID: string, action: string): Promise<void> {
  const decided = await api.post<unknown>(`/v1/approvals/${approvalID}/${action}`, {})
  expect(decided.status, JSON.stringify(decided.body)).toBe(200)
}

async function activeLeaseIDs(api: WebAPI): Promise<string[]> {
  const listed = await api.get<ListOf<{ id: string; status: string }>>('/v1/leases')
  return listed.body.items.filter((item) => item.status === 'active').map((item) => item.id)
}

test('允许到任务结束之后，同一会话里同样的调用不再问人', async ({ api, agent, external }) => {
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  const first = await session.call('github.issue.create', { ...repository, title: '第一次' })
  expect(first.refused, '第一次写操作没有停在缝前').toBe(true)
  await settle(api, await onlyPending(api), 'allow-task')

  const issued = await activeLeaseIDs(api)
  expect(issued, '批准之后没有签出授权').toHaveLength(1)

  // 这一步此前会被再问一遍人：allow-task 不留记忆，而决策链路只读记忆。
  const again = await session.call('github.issue.create', { ...repository, title: '第二次' })
  expect(again.refused, '批准过的同一件事又被问了一遍（R-39）').toBe(false)

  // 没有第二条待决项 —— 它是真的通过了，不是又开了一次审批。
  const stillWaiting = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  expect(stillWaiting.body.items.filter((item) => item.status === 'pending')).toHaveLength(0)

  // **不得另签一条授权**：为同一次请求签出第二条，等于把一次人工确认换成两份权限。
  // 「允许到任务结束」的次数上限是 scope.TaskRequestLimit，用掉一次之后它还活着，
  // 因此这里断言的是「还是原来那一条」。
  expect(
    await activeLeaseIDs(api),
    '据已有授权放行时又签了一条新的 —— 一次确认换出了两份权限',
  ).toEqual(issued)

  // 出站那条执行记的必须是**原来那条**授权。
  // 不看 `decision.auto_allowed`：审计先于签发写入（ADR-004），那条记录上的
  // lease_id 在任何路径下都是空的 —— 拿它做断言只会恒真或恒假。
  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  const executed = ledger.body.items.filter((event) => event.type === 'adapter.executed')
  expect(executed, '账本上没有这次执行').toHaveLength(1)
  expect(executed[0]?.lease_id, '执行用的不是原来那条授权').toBe(issued[0])

  // 记忆一条都没多：allow-task 是「这次任务里可以」，不是「今后都可以」。
  const memories = await api.get<ListOf<TrustMemoryView>>('/v1/trust-memories')
  expect(memories.body.items, '「允许到任务结束」生成了记忆 —— 那是另一个决定').toHaveLength(0)

  // 只有第二次真的发出去了：第一次停在缝前，什么也没发。
  expect(external.github.received().filter((arrived) => arrived.method === 'POST')).toHaveLength(1)
})

test('允许到任务结束真的覆盖到任务结束，不是只覆盖一次', async ({ api, agent, external }) => {
  // R-49：这个选项此前签出的授权次数上限是 1，与「仅允许这一次」毫无区别 ——
  // 批准之后第二次调用通过、第三次又回到缝前。
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  expect((await session.call('github.issue.create', { ...repository, title: '第一次' })).refused).toBe(true)
  await settle(api, await onlyPending(api), 'allow-task')

  for (const round of [2, 3, 4, 5]) {
    const call = await session.call('github.issue.create', { ...repository, title: `第 ${String(round)} 次` })
    expect(call.refused, `第 ${String(round)} 次调用又回到了缝前 —— 「到任务结束」只覆盖了一次`).toBe(false)
  }

  expect(
    external.github.received().filter((arrived) => arrived.method === 'POST'),
    '四次调用没有都真的发出去',
  ).toHaveLength(4)

  // 一次确认仍然只对应一份授权。
  const listed = await api.get<ListOf<{ id: string; status: string }>>('/v1/leases')
  expect(listed.body.items).toHaveLength(1)
})

test('允许到任务结束罩不住另一个资源', async ({ api, agent, external }) => {
  // 差一维就是不覆盖 —— 与 Learn Without Expanding 同一条规矩。
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  expect((await session.call('github.issue.create', { ...repository, title: '第一次' })).refused).toBe(true)
  await settle(api, await onlyPending(api), 'allow-task')

  const elsewhere = await session.call('github.issue.create', {
    owner: 'octocat',
    repo: 'another-repo',
    title: '换个仓库',
  })
  expect(elsewhere.refused, '一条授权罩住了它范围之外的资源').toBe(true)
  expect(
    external.github.received().filter((arrived) => arrived.path.includes('another-repo')),
    '被拒的请求仍然发出去了',
  ).toHaveLength(0)
})

test('会话结束后那条授权失效，同样的调用重新回到缝前', async ({ api, agent }) => {
  // 所有授权默认有期限（不可协商约束第 5 条）。放行分支靠的是「仍然生效」，
  // 收回之后它就没有依据了。
  await connect(api, identity)
  const session = await agent()
  await session.confirm()

  expect((await session.call('github.issue.create', { ...repository, title: '第一次' })).refused).toBe(true)
  await settle(api, await onlyPending(api), 'allow-task')
  expect((await session.call('github.issue.create', { ...repository, title: '第二次' })).refused).toBe(false)

  await session.disconnect()
  expect(await activeLeaseIDs(api), '会话结束了，授权却还活着').toEqual([])

  const resumed = await agent()
  await resumed.confirm()
  const afterwards = await resumed.call('github.issue.create', { ...repository, title: '第三次' })
  expect(afterwards.refused, '授权已经收回，同样的调用却仍然通过').toBe(true)
})
