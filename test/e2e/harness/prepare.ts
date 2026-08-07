import { expect, type Page } from '@playwright/test'
import type { WebAPI } from './api.js'
import type { IdentityView, ListOf } from './views.js'

/*
 * 用例开跑前要摆好的前置状态。
 *
 * 集中在这里而不是各写一份：连接身份是几乎每个用例的第一步，各自复制一遍
 * 之后，端点契约一改就要在多处追改，而漏掉的那一处只会在运行时才发现。
 */

/** IdentitySpec 是一次连接身份的坐标与标签。 */
export interface IdentitySpec {
  /** service 是已声明 Adapter 的服务名，例如 github。 */
  readonly service: string
  /** item 是假保险库里的条目名。 */
  readonly item: string
  /** field 是条目里的字段名，决定取到哪个哨兵（见 onepassword.ts）。 */
  readonly field: string
  readonly accountLabel: string
  readonly environment?: 'production' | 'non-production'
  readonly isDefault?: boolean
}

/** connect 连接一个身份并返回它。 */
export async function connect(api: WebAPI, spec: IdentitySpec): Promise<IdentityView> {
  const connected = await api.connectIdentity({
    service: spec.service,
    item: spec.item,
    field: spec.field,
    accountLabel: spec.accountLabel,
    ...(spec.environment === undefined ? {} : { environment: spec.environment }),
    ...(spec.isDefault === undefined ? {} : { isDefault: spec.isDefault }),
  })
  expect(connected.status, JSON.stringify(connected.body)).toBe(201)
  return connected.body as IdentityView
}

/**
 * openConsole 打开一个页面，并等到它真的接上了事件流。
 *
 * 这一步不能省：Gate 的列表先拉一次 REST 再订阅 SSE，而广播不做重放
 * （`internal/transport/httpapi/events.go`）。在这两步之间发生的到达没有
 * 任何人收得到 —— 用例若在订阅之前就去触发请求，等到的会是一张永远不出现的卡片，
 * 而机器越忙那个窗口越宽。
 */
export async function openConsole(page: Page, path: string): Promise<void> {
  watch(page)
  const streaming = page.waitForResponse((response) => response.url().includes('/v1/events'))
  await page.goto(path)
  await streaming
}

/*
 * 缝前那张卡片等不到时，把两边的状态都摆出来。
 *
 * 「locator resolved to 0 elements」这一句同时符合好几种故障：网关没收到请求、
 * 事件没发出去、浏览器没收到、收到了但没渲染。2026-08-07 起为它写了三个修复，
 * 全部打在错误的一环上 —— 每一轮都要等一次 CI 才知道又错了。
 *
 * 所以失败信息里带上：网关那边有没有这条请求、页面开了几条事件流、
 * 控制台报了什么。答案在日志里，不必再猜一轮。
 */
interface PageWatch {
  streams: string[]
  problems: string[]
}

const watched = new WeakMap<Page, PageWatch>()

function watch(page: Page): void {
  if (watched.has(page)) {
    return
  }
  const record: PageWatch = { streams: [], problems: [] }
  watched.set(page, record)

  page.on('response', (response) => {
    if (response.url().includes('/v1/events')) {
      record.streams.push(`${String(response.status())} ${response.url()}`)
    }
  })
  page.on('console', (message) => {
    if (message.type() === 'error') {
      record.problems.push(`console: ${message.text()}`)
    }
  })
  page.on('pageerror', (error) => {
    record.problems.push(`pageerror: ${error.message}`)
  })
}

/** expectArrivalCard 等缝前长出 count 张卡片，等不到就连着证据一起失败。 */
export async function expectArrivalCard(page: Page, api: WebAPI, count = 1): Promise<void> {
  const card = page.locator('button[aria-pressed]')
  try {
    await expect(card).toHaveCount(count, { timeout: 10_000 })
  } catch (failure) {
    // 诊断自己**不能**抛。它一抛，真正的失败就被换成了一句无关的错，
    // 而那正是 2026-08-07 那一轮 CI 上发生的事：/v1/capability-requests 没有
    // 列表端点，取 items 时崩掉，四种可能的故障一种也没查出来。
    const notes: string[] = []
    for (const [what, gather] of [
      ['缝前的待决项', async () => JSON.stringify(
          (await api.get<ListOf<ApprovalLike>>('/v1/approvals')).body.items.map(
            (item) => `${item.id}:${item.status}`,
          ),
        )],
      ['页面开过的事件流', () => Promise.resolve(JSON.stringify(watched.get(page)?.streams ?? []))],
      ['控制台', () => Promise.resolve(JSON.stringify(watched.get(page)?.problems ?? []))],
      ['页面文字', async () => (await page.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 400)],
    ] as const) {
      try {
        notes.push(`${what}：${await gather()}`)
      } catch (cause) {
        notes.push(`${what}：取不到（${String(cause)}）`)
      }
    }

    throw new Error(
      [`缝前没有长出卡片（期望 ${String(count)} 张）。`, ...notes, String(failure)].join('\n'),
    )
  }
}

/** ApprovalLike 只取诊断要看的那几项，避免与视图类型互相牵扯。 */
interface ApprovalLike {
  readonly id: string
  readonly status: string
}
