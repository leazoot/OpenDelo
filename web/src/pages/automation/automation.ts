import type { TrustMemory } from '../../data/trustMemories'
import type { Copy } from '../../i18n/copy'

/*
 * Automation 主页的六类内容（PRD §16.3、REQ-UI-006）。
 *
 * 全部由服务端已有的数据推出来：模式来自偏好，其余五类来自 Trust Memory。
 * 这里**不发明任何规则**——页面上写着的每一条都能在账本或记忆里找到出处。
 */

/** 一条学过的授权（PRD §16.3 的「已学习授权」）。 */
export interface LearnedRule {
  readonly id: string
  readonly title: string
  readonly environment: string
  readonly riskCeiling: string
  readonly behavior: string
  /** 「由你在 YYYY-MM-DD 的审批中创建」（REQ-TRUST-001 AC3）。 */
  readonly origin: string
  readonly isInvalidated: boolean
  readonly invalidationReason: string
}

/** 一条项目与身份的绑定（PRD §16.3 的「项目和身份绑定」）。 */
export interface Binding {
  readonly key: string
  readonly workspaceId: string
  readonly identityId: string
  readonly service: string
  readonly ruleCount: number
}

export function learnedRules(
  memories: readonly TrustMemory[],
  identityNames: ReadonlyMap<string, string>,
  copy: Copy,
): readonly LearnedRule[] {
  return memories.map((memory) => ({
    id: memory.id,
    title: `${memory.service} · ${identityNames.get(memory.identity_id) ?? memory.identity_id}`,
    environment: memory.environment,
    riskCeiling: memory.risk_ceiling,
    behavior: memory.approval_behavior,
    origin: originTextOf(memory, copy),
    isInvalidated: memory.status !== 'active',
    invalidationReason: memory.invalidation_reason,
  }))
}

/**
 * 这条授权是怎么来的（REQ-TRUST-001 AC3）。
 *
 * 日期取记忆的创建时刻。**认不出的时刻不编一个日期**：一句「由你在某天的
 * 审批中创建」若日期是猜的，用户就再也无法拿它去账本里对。
 */
export function originTextOf(memory: TrustMemory, copy: Copy): string {
  const day = memory.created_at.slice(0, 10)
  return /^\d{4}-\d{2}-\d{2}$/.test(day) ? copy.automationOrigin(day) : copy.automationOriginUnknown
}

/** 命中后自动允许的那些。 */
export function autoAllowed(rules: readonly LearnedRule[]): readonly LearnedRule[] {
  return rules.filter((rule) => !rule.isInvalidated && rule.behavior === 'auto_allow')
}

/**
 * 每次都要问的那些。
 *
 * 用**否定 auto_allow** 而不是 `=== 'always_ask'`：认不出的行为落进「要问」
 * 这一侧，而不是落进「自动允许」（Fail Closed）。
 */
export function alwaysAsked(rules: readonly LearnedRule[]): readonly LearnedRule[] {
  return rules.filter((rule) => !rule.isInvalidated && rule.behavior !== 'auto_allow')
}

export function bindingsOf(memories: readonly TrustMemory[]): readonly Binding[] {
  const found = new Map<string, Binding>()
  for (const memory of memories) {
    if (memory.status !== 'active') {
      continue
    }
    const key = `${memory.workspace_id} ${memory.identity_id}`
    const seen = found.get(key)
    found.set(key, {
      key,
      workspaceId: memory.workspace_id,
      identityId: memory.identity_id,
      service: memory.service,
      ruleCount: (seen?.ruleCount ?? 0) + 1,
    })
  }
  return [...found.values()]
}

/** 一条风险策略：这个等级的操作在当前模式下会发生什么。 */
export interface RiskPolicy {
  readonly level: 'low' | 'medium' | 'high'
  readonly text: string
  /** 这一条不受模式影响（REQ-DECIDE-003 的不可协商约束）。 */
  readonly isFixed: boolean
}

/**
 * 当前模式下的三条风险策略（REQ-DECIDE-003 的表）。
 *
 * **高风险那一条与模式无关**，三种模式下都是「始终需要你确认」——
 * 不存在任何配置组合能让它自动执行（AC3）。认不出的模式按最严的那一档显示：
 * 说得比实际严，最坏是让用户多确认一次；说得比实际松，用户会以为某件事
 * 不会自动发生。
 */
export function riskPolicies(mode: string, copy: Copy): readonly RiskPolicy[] {
  const known = mode === 'balanced' || mode === 'automatic' ? mode : 'cautious'
  return [
    { level: 'low', text: copy.automationLowPolicy(known), isFixed: false },
    { level: 'medium', text: copy.automationMediumPolicy(known), isFixed: false },
    { level: 'high', text: copy.automationHighPolicy, isFixed: true },
  ]
}

/** 高级规则以文书形式编辑，一条规则一份文书。 */
export function manuscriptPath(ruleId: string): string {
  return `/automation/advanced/${encodeURIComponent(ruleId)}`
}
