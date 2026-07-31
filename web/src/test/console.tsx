import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, type RenderResult } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'

import { routes } from '../app/routes'
import { createQueryClient } from '../data/queryClient'

/*
 * 整个 Console 的测试装置：真实路由 + 真实外壳 + 一份答复全部端点的 fetch。
 *
 * 页面各自的用例只挂那一页，因为它们测的是那一页的行为。跨页的检查（可访问性、
 * 键盘全流程）测的却恰恰是页面**之间**的事 —— 顶栏与页面共处一个无障碍树、
 * 决策之后跳回哪一页 —— 把外壳换成 `<p>占位</p>` 就等于把要测的东西拿掉了。
 */

const NOW = '2026-07-30T09:00:00.000Z'

export const agents = {
  items: [
    {
      id: 'ag-1',
      name: 'writer-agent',
      type: 'claude_code',
      device_id: 'dv-000042',
      workspace_id: 'ws-1',
      trust_level: 'known',
      status: 'active',
      last_seen_at: NOW,
    },
  ],
}

export const identities = {
  items: [
    {
      id: 'id-1',
      service: 'cloudflare',
      account_label: 'ops@example.com',
      environment: 'production',
      is_default: true,
      status: 'active',
    },
  ],
}

export const gatewayStatus = {
  status: 'running',
  version: '1.2.3-test',
  listen_address: '127.0.0.1',
  web_api_port: 8787,
  started_at: NOW,
}

/** 待审批的那一条。中风险 + 生产写入，因此界面上该出现的东西一个不少。 */
const request = {
  id: 'rq-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  service: 'cloudflare',
  operation: 'update_dns_record',
  resource: { zone: 'example.com', record: 'www' },
  desired_change: { content: '203.0.113.9' },
  change_preview: null,
  reason: '把 www 指到新机器',
  status: 'awaiting_approval',
  withheld_operations: null,
  created_at: NOW,
  decision: {
    verdict: 'require_approval',
    risk_level: 'medium',
    risk_factors: ['adapter_declared_label', 'production_write'],
    identity_id: 'id-1',
    reason_code: 'requires_confirmation',
    resolved_scope: {
      operation: 'update_dns_record',
      resource: { zone: 'example.com' },
      not_before: NOW,
      expires_at: '2026-07-30T09:15:00.000Z',
      request_limit: 1,
    },
  },
}

export const approvals = {
  items: [
    {
      id: 'ap-1',
      status: 'pending',
      created_at: NOW,
      available_actions: ['allow_once', 'allow_until_task_end', 'deny'],
      request: { ...request, decision: null },
      decision: null,
    },
  ],
}

export const leases = {
  items: [
    {
      id: 'ls-1',
      agent_id: 'ag-1',
      identity_id: 'id-1',
      service: 'cloudflare',
      resource_scope: { zone: 'example.com' },
      expires_at: '2026-07-30T09:15:00.000Z',
      status: 'active',
      is_session_bound: false,
    },
  ],
}

export const trustMemories = {
  items: [
    {
      id: 'tm-1',
      agent_id: 'ag-1',
      workspace_id: 'ws-1',
      identity_id: 'id-1',
      service: 'cloudflare',
      environment: 'production',
      risk_ceiling: 'medium',
      approval_behavior: 'auto_allow',
      created_from: 'ap-1',
      status: 'active',
      invalidation_reason: '',
      expires_at: '2026-08-30T09:00:00.000Z',
      created_at: '2026-07-01T09:00:00.000Z',
    },
  ],
}

export const auditEvents = {
  items: [
    {
      id: 'ev-1',
      operation_id: '01KYM0OP1',
      type: 'decision.auto_allowed',
      agent_id: 'ag-1',
      device_id: 'dv-000042',
      workspace_id: 'ws-1',
      identity_id: 'id-1',
      service: 'cloudflare',
      operation: 'update_dns_record',
      resource: { zone: 'example.com' },
      resolved_scope: {},
      verdict: 'allow',
      risk_level: 'low',
      lease_id: 'ls-1',
      lease_status: 'active',
      outcome: 'succeeded',
      duration_ms: 42,
      is_redacted: true,
      created_at: NOW,
    },
  ],
  next_cursor: '',
}

export const preferences = {
  automation_mode: 'balanced',
  approval_timeout_seconds: 300,
  read_only_auto_allow: false,
  theme: 'system',
  language: 'zh',
  restart_required: {
    listen_address: '127.0.0.1',
    web_api_port: 8787,
    mcp_port: 8789,
    proxy_port: 8788,
  },
  warnings: [],
}

function json(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )
}

/** 每个端点都答得出东西：任何一处空手而归，页面就落到空态，扫的就不是真实结构了。 */
export function respondTo(path: string): Promise<Response> {
  if (path.startsWith('/v1/events')) {
    // 事件流不在这些用例的范围内。204 让订阅立刻结束，界面读的仍是 Query 里那一份。
    return Promise.resolve(new Response(null, { status: 204 }))
  }
  if (path.startsWith('/v1/gateway/status')) {
    return json(gatewayStatus)
  }
  if (path.startsWith('/v1/capability-requests/')) {
    return json(request)
  }
  if (path.startsWith('/v1/approvals')) {
    return json(approvals)
  }
  if (path.startsWith('/v1/agents')) {
    return json(agents)
  }
  if (path.startsWith('/v1/identities')) {
    return json(identities)
  }
  if (path.startsWith('/v1/leases')) {
    return json(leases)
  }
  if (path.startsWith('/v1/trust-memories')) {
    return json(trustMemories)
  }
  if (path.startsWith('/v1/audit-events')) {
    return json(auditEvents)
  }
  if (path.startsWith('/v1/preferences')) {
    return json(preferences)
  }
  return json({ items: [] })
}

/** 挂起整个 Console，落在指定路径上。 */
export function renderConsole(at: string): RenderResult {
  const router = createMemoryRouter(routes, { initialEntries: [at] })
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

/**
 * 把挂起的请求跑完。
 *
 * 跑几轮而不是一轮：页面上的查询是分层的（先要 agents 才认得出请求属于谁），
 * 一轮只推进最外面那一层，界面会停在骨架上 —— 扫的就成了加载态。
 */
export async function settleRequests(): Promise<void> {
  for (let round = 0; round < 4; round += 1) {
    await act(async () => {
      await new Promise((resolve) => {
        setTimeout(resolve, 0)
      })
    })
  }
}
