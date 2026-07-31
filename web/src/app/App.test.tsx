import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactElement } from 'react'
import { createMemoryRouter, MemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { GatewayNotice } from '../boundary/GatewayNotice'
import { useGatewayStatus, type GatewayStatus } from '../data/gatewayStatus'
import { createQueryClient } from '../data/queryClient'

import { BoundaryBar } from './BoundaryBar'
import { routes } from './routes'

const RUNNING: GatewayStatus = {
  status: 'running',
  version: '1.2.3-test',
  listen_address: '127.0.0.1',
  web_api_port: 8787,
  started_at: '2026-07-28T09:15:30.123Z',
}

/** 每个用例一份 QueryClient，缓存不跨用例。 */
function renderWithQuery(element: ReactElement) {
  return render(<QueryClientProvider client={createQueryClient()}>{element}</QueryClientProvider>)
}

/**
 * 把 useGatewayStatus 接到一个可控的请求上，再交给真实的界面组件。
 *
 * 这样测的是「查询结果 → 界面」这整条链路，而不是给组件硬塞一个状态。
 */
function Probe({ request, intervalMs }: { request: () => Promise<GatewayStatus>; intervalMs: number }) {
  const { state, status } = useGatewayStatus({ request, intervalMs })
  return (
    <MemoryRouter>
      <BoundaryBar connection={state} status={status} pending={0} onOpenCommands={() => undefined} />
      <GatewayNotice state={state} />
    </MemoryRouter>
  )
}

describe('Gateway 状态的三态', () => {
  it('加载态：还没有结果时顶栏显示连接中，说明也不假装知道结果', async () => {
    // 请求悬着不返回，界面必须仍然可用，而不是空白或整页 spinner。
    renderWithQuery(<Probe request={() => new Promise<GatewayStatus>(() => undefined)} intervalMs={10} />)

    expect(await screen.findByText('连接中')).toBeInTheDocument()
    expect(screen.getByText('正在连接 Gateway')).toBeInTheDocument()
  })

  it('成功态：顶栏显示已连接与真实的 host', async () => {
    renderWithQuery(<Probe request={() => Promise.resolve(RUNNING)} intervalMs={10} />)

    expect(await screen.findByText('已连接')).toBeInTheDocument()
    expect(screen.getByText('127.0.0.1:8787')).toBeInTheDocument()
    // 已连接时没有说明要讲。
    expect(screen.queryByText('Gateway 未连接')).not.toBeInTheDocument()
  })

  it('错误态：说明发生了什么并给出下一步，不是空白错误页', async () => {
    renderWithQuery(<Probe request={() => Promise.reject(new Error('connection refused'))} intervalMs={10} />)

    expect(await screen.findByText('未连接')).toBeInTheDocument()
    expect(screen.getByText('Gateway 未连接')).toBeInTheDocument()
    expect(screen.getByText('opendelo start')).toBeInTheDocument()
    // REQ-GATEWAY-003 AC2：本地数据仍然安全这件事必须说出来。
    expect(screen.getByText(/本地账本与既有授权都在原处/)).toBeInTheDocument()
  })

  it('响应结构不符合契约时按未连接处理，不显示半个状态', async () => {
    renderWithQuery(
      <Probe request={() => Promise.reject(new Error('schema mismatch'))} intervalMs={10} />,
    )

    expect(await screen.findByText('未连接')).toBeInTheDocument()
  })
})

describe('连接状态的变化', () => {
  it('Gateway 停掉之后顶栏在一个轮询周期内变为未连接', async () => {
    // REQ-GATEWAY-002 AC1。这里用 10ms 的间隔跑完真实的
    // 轮询链路；生产用的间隔由 gatewayStatus.test.ts 断言在 5 秒预算内。
    let alive = true
    const request = () => (alive ? Promise.resolve(RUNNING) : Promise.reject(new Error('connection refused')))

    renderWithQuery(<Probe request={request} intervalMs={10} />)
    expect(await screen.findByText('已连接')).toBeInTheDocument()

    alive = false
    await waitFor(() => {
      expect(screen.getByText('未连接')).toBeInTheDocument()
    })
  })

  it('恢复之后自己回到已连接，不需要刷新页面', async () => {
    let alive = false
    const request = () => (alive ? Promise.resolve(RUNNING) : Promise.reject(new Error('connection refused')))

    renderWithQuery(<Probe request={request} intervalMs={10} />)
    expect(await screen.findByText('未连接')).toBeInTheDocument()

    alive = true
    await waitFor(() => {
      expect(screen.getByText('已连接')).toBeInTheDocument()
    })
  })
})

describe('外壳的接线', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  /** 走完真实链路：从入口文档读令牌，再经 requestGateway 发请求。 */
  function renderConsole(respond: (path: string) => Promise<Response>) {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    vi.stubGlobal('fetch', (path: string) => respond(path))
    const router = createMemoryRouter(routes, { initialEntries: ['/gate'] })
    return render(
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )
  }

  const jsonOf = (body: unknown) =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
      }),
    )

  it('离线时缝换成离线样式，说明同时出现', async () => {
    // 单测每个组件都绿、外壳却忘了把 offline 传给 Seam —— 变异探针实测会漏，
    // 所以这条用例从整个外壳这一层看链路。
    const { container } = renderConsole(() => Promise.reject(new Error('connection refused')))

    expect(await screen.findByText('Gateway 未连接')).toBeInTheDocument()
    expect(container.innerHTML).toContain('fillOff')
  })

  it('已连接时缝不带离线样式，也没有说明', async () => {
    const { container } = renderConsole((path) => (path.includes('approvals') ? jsonOf({ items: [] }) : jsonOf(RUNNING)))

    expect(await screen.findByText('已连接')).toBeInTheDocument()
    expect(container.innerHTML).not.toContain('fillOff')
    expect(screen.queryByText('Gateway 未连接')).not.toBeInTheDocument()
  })

  it('入口文档没有令牌时按未连接处理，而不是白屏', async () => {
    // 令牌缺失是 Console 自己的问题，但用户看到的应该仍然是一个能用的界面。
    vi.stubGlobal('fetch', () => Promise.reject(new Error('不该走到这里')))
    const router = createMemoryRouter(routes, { initialEntries: ['/gate'] })
    render(
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )

    expect(await screen.findByText('未连接')).toBeInTheDocument()
  })

  it('Gateway 的离线说明在所有页面上都在，不只在 Gate', async () => {
    // 连不上是整个 Console 的处境。把说明放进 Gate 会让其他页面看起来一切正常。
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    vi.stubGlobal('fetch', () => Promise.reject(new Error('connection refused')))
    const router = createMemoryRouter(routes, { initialEntries: ['/ledger'] })
    render(
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )

    expect(await screen.findByText('Gateway 未连接')).toBeInTheDocument()
  })
})

describe('状态的可访问性与配色', () => {
  it('状态点旁边永远有文字，颜色不是唯一的信息载体', async () => {
    renderWithQuery(<Probe request={() => Promise.reject(new Error('connection refused'))} intervalMs={10} />)

    const label = await screen.findByText('未连接')
    expect(label.textContent).toBe('未连接')
  })

  it('离线说明通过 live region 播报', async () => {
    renderWithQuery(<Probe request={() => Promise.reject(new Error('connection refused'))} intervalMs={10} />)

    await screen.findByText('Gateway 未连接')
    const notice = screen.getByText('Gateway 未连接').closest('aside')
    expect(notice).toHaveAttribute('aria-live', 'polite')
  })
})
