import type { LedgerEvent } from '../../data/ledgerSummary'
import type { TrustMemory } from '../../data/trustMemories'

/*
 * Rule Manuscript 的内容模型（REQ-TRUST-006、设计稿 §06）。
 *
 * 一份文书 = 一条 Trust Memory 读成人话。**它能改的只有一处**：把「直接通行」
 * 改成「当面确认」。别的槽位照样带下划线，但那是文书的措辞，不是可以拧的旋钮
 * —— 放宽范围在 API 里连表达都表达不出来（REQ-TRUST-002），
 * 界面给出一个改不动的控件只会让人以为自己能改。
 */

export interface Manuscript {
  readonly id: string
  readonly title: string
  readonly service: string
  readonly agentId: string
  readonly workspaceId: string
  readonly identityId: string
  readonly environment: string
  readonly riskCeiling: string
  readonly behavior: string
  readonly expiresAt: string
}

export function manuscriptOf(memory: TrustMemory): Manuscript {
  return {
    id: memory.id,
    title: `${memory.service} · ${memory.environment}`,
    service: memory.service,
    agentId: memory.agent_id,
    workspaceId: memory.workspace_id,
    identityId: memory.identity_id,
    environment: memory.environment,
    riskCeiling: memory.risk_ceiling,
    behavior: memory.approval_behavior,
    expiresAt: memory.expires_at,
  }
}

/** 这份文书此刻是「当面确认」吗。认不出的行为按要确认处理（Fail Closed）。 */
export function asksEveryTime(behavior: string): boolean {
  return behavior !== 'auto_allow'
}

/**
 * 签署会改变什么。
 *
 * 已经是「当面确认」的文书没有可签署的改动 —— 签一次不会有任何事发生，
 * 按钮因此不该亮着。
 */
export function hasPendingChange(current: string, drafted: string): boolean {
  return current !== drafted && asksEveryTime(drafted)
}

/** 影响预览的三个数（REQ-TRUST-006 AC2：来自真实审计查询）。 */
export interface Impact {
  readonly confirmed: number
  readonly passed: number
  readonly refused: number
  /** 取回的这一窗被 7 天填满 —— 三个数是下界。 */
  readonly isPartial: boolean
}

const SEVEN_DAYS_MS = 7 * 24 * 60 * 60_000

/**
 * 过去 7 天里这条规则会碰到多少次请求。
 *
 * 只数**这个 Agent 在这个服务上**的那些：账本里别的服务的记录与这份文书无关，
 * 把它们算进来会让「签下去会发生什么」这个问题得到一个更大的、错的答案。
 */
export function impactOf(
  events: readonly LedgerEvent[],
  manuscript: Manuscript,
  now: number,
  window: number,
): Impact {
  const mine = events.filter(
    (event) => event.service === manuscript.service && withinWeek(event.created_at, now),
  )
  const oldest = events[events.length - 1]

  return {
    confirmed: mine.filter((event) => event.verdict === 'require_approval').length,
    passed: mine.filter((event) => event.verdict === 'allow').length,
    refused: mine.filter((event) => event.verdict === 'deny').length,
    isPartial: events.length >= window && oldest !== undefined && withinWeek(oldest.created_at, now),
  }
}

function withinWeek(iso: string, now: number): boolean {
  const at = Date.parse(iso)
  return !Number.isNaN(at) && now - at <= SEVEN_DAYS_MS
}

/*
 * YAML 视图（REQ-TRUST-006 AC4）。
 *
 * 自己写而不是引一个 YAML 库：这里的形状是固定的八个标量字段，
 * 而一个通用解析器会连锚点、标签、嵌套一起带进来 —— 那些都是
 * 安全规则明确不要的东西。
 */

const YAML_KEYS = [
  'id',
  'title',
  'service',
  'agent',
  'workspace',
  'identity',
  'environment',
  'risk_ceiling',
  'behavior',
  'expires_at',
] as const

export function toYaml(manuscript: Manuscript): string {
  const values: Record<(typeof YAML_KEYS)[number], string> = {
    id: manuscript.id,
    title: manuscript.title,
    service: manuscript.service,
    agent: manuscript.agentId,
    workspace: manuscript.workspaceId,
    identity: manuscript.identityId,
    environment: manuscript.environment,
    risk_ceiling: manuscript.riskCeiling,
    behavior: manuscript.behavior,
    expires_at: manuscript.expiresAt,
  }
  return YAML_KEYS.map((key) => `${key}: ${quote(values[key])}`).join('\n')
}

/**
 * 读回结构化视图。
 *
 * 认不出的行、缺失的键都当作**解析失败**而不是补一个默认值：
 * 一份少了 `behavior` 却照样能签的文书，签下去会把「当面确认」写成空。
 */
export function fromYaml(text: string): Manuscript | null {
  const found = new Map<string, string>()
  for (const line of text.split('\n')) {
    if (line.trim() === '') {
      continue
    }
    const at = line.indexOf(': ')
    if (at <= 0) {
      return null
    }
    found.set(line.slice(0, at).trim(), unquote(line.slice(at + 2)))
  }
  if (YAML_KEYS.some((key) => !found.has(key))) {
    return null
  }
  return {
    id: found.get('id') ?? '',
    title: found.get('title') ?? '',
    service: found.get('service') ?? '',
    agentId: found.get('agent') ?? '',
    workspaceId: found.get('workspace') ?? '',
    identityId: found.get('identity') ?? '',
    environment: found.get('environment') ?? '',
    riskCeiling: found.get('risk_ceiling') ?? '',
    behavior: found.get('behavior') ?? '',
    expiresAt: found.get('expires_at') ?? '',
  }
}

/** 空串与带空白的值必须加引号，否则读回来会变形。 */
function quote(value: string): string {
  return value === '' || value !== value.trim() ? `"${value.replaceAll('"', '\\"')}"` : value
}

function unquote(value: string): string {
  const trimmed = value.trim()
  if (!trimmed.startsWith('"') || !trimmed.endsWith('"') || trimmed.length < 2) {
    return trimmed
  }
  return trimmed.slice(1, -1).replaceAll('\\"', '"')
}

/** 文书里那四处槽位。 */
export type SlotName = 'agent' | 'operation' | 'resource' | 'behavior'

interface Segment {
  readonly key: string
  readonly text: string
  readonly slot: SlotName | ''
}

const SLOT_NAMES: readonly SlotName[] = ['agent', 'operation', 'resource', 'behavior']

/** 把带 `{slot}` 的句子拆成「文字 · 槽位」交替的片段。 */
export function segmentsOf(template: string): readonly Segment[] {
  const segments: Segment[] = []
  const pattern = /\{(\w+)\}/g
  let at = 0
  let found = pattern.exec(template)
  while (found !== null) {
    segments.push({ key: `t${String(at)}`, text: template.slice(at, found.index), slot: '' })
    const name = found[1] ?? ''
    segments.push({
      key: `s${name}`,
      text: '',
      slot: SLOT_NAMES.find((slot) => slot === name) ?? 'agent',
    })
    at = found.index + found[0].length
    found = pattern.exec(template)
  }
  segments.push({ key: `t${String(at)}`, text: template.slice(at), slot: '' })
  return segments
}
