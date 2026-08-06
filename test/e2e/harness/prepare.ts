import { expect, type Page } from '@playwright/test'
import type { WebAPI } from './api.js'
import type { IdentityView } from './views.js'

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
  const streaming = page.waitForResponse((response) => response.url().includes('/v1/events'))
  await page.goto(path)
  await streaming
}
