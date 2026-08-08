import type { Page } from '@playwright/test'
import { test, expect } from '../harness/fixtures.js'
import { connect, expectArrivalCard, openConsole } from '../harness/prepare.js'
import type { ListOf } from '../harness/views.js'

/*
 * REQ-NFR-003：四种浏览器 × 五个主页面 + 核心流程 + 打不开时的提示。
 *
 * 这一份在 chromium / firefox / webkit / msedge 上各跑一遍（playwright.config.ts）。
 * 其余用例只在 chromium 上跑：它们问的是产品的行为，那些答案与浏览器无关。
 * 会因浏览器而不同的是渲染与浏览器 API —— 布局、fetch、SSE、键盘事件、
 * 以及「跑不动时看到什么」，本文件覆盖的正是这些。
 *
 * 断言全部打在结构与行为上，不打在文案上：文案由 web 侧的用例守，
 * 在这里再抄一遍只会让改一句话要动两个仓的文件。
 */

/** 五个主页面（PRD §16）。标题在中英两套文案里同名，因此可以直接断言。 */
const mainPages = [
  { path: '/gate', title: 'Gate' },
  { path: '/identities', title: 'Identities' },
  { path: '/automation', title: 'Automation' },
  { path: '/ledger', title: 'Boundary Ledger' },
  { path: '/preferences', title: 'Preferences' },
] as const

const gitHubIdentity = { service: 'github', item: 'GitHub Bot', field: 'token' } as const
const repository = { owner: 'octocat', repo: 'hello-world' }

// ---------------------------------------------------------------- 五个主页面

test('五个主页面都渲染得出来，没有脚本错误，也没有横向溢出', async ({ page }) => {
  const broken: string[] = []
  page.on('pageerror', (error) => broken.push(error.message))
  // 控制台上的 error 同样算数：React 渲染失败时先出现在这里，
  // 而页面可能还留着上一帧的内容，光看结构看不出来。
  page.on('console', (message) => {
    if (message.type() === 'error') {
      broken.push(message.text())
    }
  })

  for (const each of mainPages) {
    await page.goto(each.path)

    await expect(page).toHaveTitle(new RegExp(each.title))
    const body = page.locator('main')
    await expect(body).toBeVisible()
    expect((await body.innerText()).trim(), `${each.path} 的主体是空的`).not.toBe('')

    // 缝的两侧只能压缩，不能把内容推到窗口外面（REQ-UI-002）。
    // 横向滚动条是「这个引擎上布局塌了」最先看得见的形状。
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    )
    expect(overflow, `${each.path} 出现了横向溢出`).toBeLessThanOrEqual(1)

    // 跑得动的浏览器上不该看见「跑不动」那段话 —— 它是给别人看的。
    await expect(page.locator('#unsupported')).toHaveCount(0)
  }

  expect(broken).toEqual([])
})

// ---------------------------------------------------------------- 核心流程

test('核心流程走得通：缝前出现请求、按键放行、授权真的生效', async ({ api, agent, page, gateway }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()

  await openConsole(page, '/gate')

  // SSE：请求从 Agent 那一面进来，缝前要自己长出来一张卡片。
  const refused = await session.call('github.issue.create', { ...repository, title: '兼容性' })
  expect(refused.refused).toBe(true)
  const card = page.locator('button[aria-pressed]')
  await expectArrivalCard(page, api, 1, gateway.output)

  // 键盘：选中之后按 A 放行（REQ-APPROVAL-003）。
  await card.first().click()
  await expect(card.first()).toHaveAttribute('aria-pressed', 'true')
  await page.keyboard.press('a')

  // 决定是从这条浏览器上送出去的：缝前不再有人等，
  // 缝内侧多了一条这次授权签出来的 Lease。
  await expect
    .poll(async () => (await pendingApprovals(page)).length, {
      message: '按键之后待决项还在，这个引擎上的决定没有送出去',
    })
    .toBe(0)
  await expect(page.locator('button[draggable="true"]')).toHaveCount(1)

  const leases = await api.get<ListOf<{ status: string; is_session_bound: boolean }>>('/v1/leases')
  const active = leases.body.items.filter((lease) => lease.status === 'active')
  expect(active).toHaveLength(1)
  expect(active[0]?.is_session_bound).toBe(true)
})

// ---------------------------------------------------------------- 打不开的时候

test('脚本跑不起来时给的是一段说明，不是白屏', async ({ gateway, browser }) => {
  // 关掉 JS 是「这个浏览器跑不动 Console」最忠实的模拟：打包出来的那份代码
  // 一行都不会执行，与认不出 ESM 的浏览器处境相同。
  const context = await browser.newContext({
    baseURL: gateway.consoleURL,
    javaScriptEnabled: false,
  })
  try {
    const page = await context.newPage()
    await page.goto('/gate')

    const notice = page.locator('#unsupported')
    await expect(notice).toBeVisible()
    // 两种语言都要在：这一刻 i18n 还没有机会运行。
    expect(await notice.innerText()).toMatch(/OpenDelo Console/)
    expect(await notice.locator('[lang="en"]').count()).toBeGreaterThan(0)

    // 白屏的形状就是「挂载点是空的，页面上也没有别的东西」。
    expect((await page.locator('#root').innerText()).trim()).toBe('')
  } finally {
    await context.close()
  }
})

test('提示不依赖打包出来的那份 JS', async ({ gateway }) => {
  // 认不出 ESM 的浏览器 Playwright 里没有，因此这一条查的是**结构**：
  // 提示能不能被揭开，只取决于 nomodule 脚本与 <noscript> 里那张样式表，
  // 两者都不在打包产物的模块图里。少了哪一个，对应那种浏览器就是白屏。
  const html = await (await fetch(gateway.consoleURL)).text()

  expect(html, 'nomodule 脚本不见了，认不出 ESM 的浏览器无从得知发生了什么').toContain(
    '<script nomodule src="/unsupported.js">',
  )
  expect(html, '<noscript> 里的样式表不见了，关掉 JS 的浏览器看到的是隐藏起来的提示').toContain(
    '<noscript><link rel="stylesheet" href="/unsupported.css" /></noscript>',
  )

  for (const asset of ['/unsupported.js', '/unsupported.css']) {
    const response = await fetch(`${gateway.consoleURL}${asset}`)
    expect(response.status, `${asset} 取不到`).toBe(200)
  }
})

// ---------------------------------------------------------------- 辅助

/** pendingApprovals 从浏览器里问一次待决项，用的是页面自己的凭证。 */
async function pendingApprovals(page: Page): Promise<{ status: string }[]> {
  return page.evaluate(async () => {
    const token = document
      .querySelector('meta[name="opendelo-session-token"]')
      ?.getAttribute('content')
    const response = await fetch('/v1/approvals', {
      headers: {
        'X-Requested-By': 'opendelo-console',
        Authorization: `Bearer ${token ?? ''}`,
      },
    })
    const body = (await response.json()) as ListOf<{ status: string }>
    return body.items.filter((item) => item.status === 'pending')
  })
}

// ---------------------------------------------------------------- 流式读取

/*
 * 这个引擎能不能**边收边读**一条还没结束的响应，而且一整条事件当场读得完。
 *
 * Console 不能用 EventSource（它发不出会话令牌与 X-Requested-By），只能自己读
 * fetch 的响应体。那条路上有两种坏法，症状一模一样（缝前永远是空的）：
 *
 *   · 引擎要等整条响应结束才交给你 —— 而事件流永远不会结束；
 *   · 引擎只交出一次写入的开头，尾巴要等这条连接上过一会儿又有数据。
 *     Linux 的 WebKit 就是这样：一条 2897 字节的 arrival 只到 123 字节，
 *     剩下的要等二十秒后的心跳（2026-08-08 实测，修法见 events.go 的 nudge）。
 *
 * 因此这一条读到**一整帧**为止，而不是读到第一块就算数 —— 只读第一块的话，
 * 上面第二种坏法会让它照样通过，而那正是它此前一周里做的事。
 *
 * 与「核心流程」分开：核心流程红的时候，是浏览器读不到、服务端没发出、
 * 还是页面没渲染，从那条用例上看不出来。这一条只问传输，答案没有歧义。
 */
test('事件流能边收边读，一整条事件当场读得完', async ({ api, agent, page, gateway }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'stream' })
  const session = await agent()
  await openConsole(page, '/gate')

  // 先把流开起来，再去触发事件：这条流不做重放，反过来的话事件发生在订阅之前。
  const opened = await page.evaluate(async () => {
    const token = document.querySelector<HTMLMetaElement>('meta[name="opendelo-session-token"]')?.content
    const response = await fetch('/v1/events', {
      headers: { Authorization: `Bearer ${token ?? ''}`, 'X-Requested-By': 'opendelo-console' },
    })
    const body = response.body
    if (body === null) {
      return { status: response.status, hasBody: false }
    }
    Object.assign(window, { __stream: { reader: body.getReader(), decoder: new TextDecoder() } })
    return {
      status: response.status,
      hasBody: true,
      contentType: response.headers.get('content-type') ?? '',
    }
  })
  expect(opened.hasBody, `响应没有体：${JSON.stringify(opened)}`).toBe(true)

  // 心跳在二十秒后。预算取八秒：靠心跳才凑齐的一帧不算「当场读得完」。
  const reading = page.evaluate(async () => {
    const stream = (window as unknown as {
      __stream: { reader: ReadableStreamDefaultReader<Uint8Array>; decoder: TextDecoder }
    }).__stream
    const chunks: number[] = []
    let text = ''
    const deadline = Date.now() + 8_000
    while (Date.now() < deadline && !/data: .*\n\n/.test(text)) {
      const got = await Promise.race([
        stream.reader.read(),
        new Promise<null>((resolve) => setTimeout(() => { resolve(null) }, Math.max(1, deadline - Date.now()))),
      ])
      if (got === null || got.done || got.value === undefined) {
        break
      }
      chunks.push(got.value.length)
      text += stream.decoder.decode(got.value, { stream: true })
    }
    await stream.reader.cancel()
    return { chunks, text }
  })

  const refused = await session.call('github.issue.create', { ...repository, title: '流式探测' })
  expect(refused.refused).toBe(true)
  const probe = await reading

  // 先打出来：这条红的时候，日志里要有足够的东西定位，不用再猜一轮。
  console.log(`[流式探测] ${gateway.consoleURL} → ${JSON.stringify(probe.chunks)}`)

  const frame = /data: (.*)\n\n/.exec(probe.text)
  expect(
    frame,
    `八秒之内没有读到一整条事件。收到的块：${JSON.stringify(probe.chunks)}，` +
      `共 ${String(probe.text.length)} 字节，末尾：${JSON.stringify(probe.text.slice(-60))} —— ` +
      `半条事件既解析不出来也不报错，界面上就是「缝前无人等待」`,
  ).not.toBeNull()

  const event = JSON.parse(frame?.[1] ?? '{}') as { type?: string }
  expect(event.type, `读到的这一帧不是到达事件：${JSON.stringify(probe.text.slice(0, 80))}`).toBe('arrival')
})
