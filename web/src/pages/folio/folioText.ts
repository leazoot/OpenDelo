import type { Folio, ResolvedScope } from '../../data/folio'
import { formatRemaining } from '../../data/leases'
import type { Copy } from '../../i18n/copy'

/*
 * 卷宗上的措辞。
 *
 * 与组件分开：这些是纯函数，可以逐条断言，也不会因为组件文件同时导出
 * 非组件而触发 react-refresh 规则。
 */

/** 风险因子码 → 一句人话。认不出的码原样显示，不假装它不存在。 */
export function riskFactorTextOf(factor: string, copy: Copy): string {
  const table: Record<string, string> = {
    adapter_declared_label: copy.factorDeclaredLabel,
    read_only: copy.factorReadOnly,
    destructive: copy.factorDestructive,
    irreversible: copy.factorIrreversible,
    permission_change: copy.factorPermissionChange,
    secret_access: copy.factorSecretAccess,
    billing: copy.factorBilling,
    bulk_change: copy.factorBulkChange,
    production_write: copy.factorProductionWrite,
    beyond_history: copy.factorBeyondHistory,
    first_seen: copy.factorFirstSeen,
    agent_unverified: copy.factorAgentUnverified,
    untrusted_device: copy.factorUntrustedDevice,
    external_communication: copy.factorExternalCommunication,
  }
  return table[factor] ?? factor
}

/**
 * 授权期限：Scope 的开始到结束有多长。
 *
 * 两端有一端认不出就说「待定」，不拿现在的时刻去补 —— 那样得到的是一个
 * 随打开时间变化的期限，而期限是这次授权的属性，不是这次浏览的属性。
 */
export function durationTextOf(scope: ResolvedScope, copy: Copy): string {
  const from = Date.parse(scope.not_before ?? '')
  const to = Date.parse(scope.expires_at ?? '')
  if (Number.isNaN(from) || Number.isNaN(to) || to <= from) {
    return copy.folioDurationUnknown
  }
  return formatRemaining(to - from)
}

/** 次数上限。0 或缺省表示不限。 */
export function limitTextOf(scope: ResolvedScope, copy: Copy): string {
  const limit = scope.request_limit ?? 0
  return limit > 0 ? copy.folioLeaseLimit(limit) : copy.folioLeaseUnlimited
}

/** Scope 里的资源写法；Scope 没给就退回请求里的资源。 */
export function scopeResourceTextOf(folio: Folio): string {
  const values = Object.values(folio.scope.resource ?? {}).filter((value) => value !== '')
  return values.length > 0 ? values.join(' · ') : folio.resource
}

/** 一行里最多点名几个仍然关闭的操作。 */
const NAMED_WITHHELD = 3

/**
 * 「仍然关闭的权限」里操作那一条。
 *
 * 能力表给得出清单就逐个点名，给不出就退回那句「不获得这个操作之外的操作」——
 * 后者说的也是真的，只是不完整。**不点名全部**：一个服务声明十几个
 * 操作时，把它们全铺开会把这一段变成一张表，而卷宗要的是一眼看完。
 * 剩下的数目照实说出来，不假装那就是全部。
 */
export function withheldOperationsTextOf(folio: Folio, operation: string, copy: Copy): string {
  const withheld = folio.withheldOperations
  if (withheld.length === 0) {
    return copy.folioWithheldOperation(operation)
  }
  const named = withheld.slice(0, NAMED_WITHHELD)
  return copy.folioWithheldOperations(named, withheld.length - named.length)
}

export type ConsequenceTone = 'granted' | 'withheld' | 'note'

export interface Consequence {
  readonly mark: string
  readonly text: string
  readonly tone: ConsequenceTone
}

function rollbackTextOf(folio: Folio, copy: Copy): string {
  switch (folio.rollback) {
    case 'none':
      return copy.folioRollbackNone
    case 'possible':
      return copy.folioRollbackPossible
    case 'not-applicable':
      return copy.folioRollbackNotApplicable
  }
}

/**
 * 放行之后会发生什么，以及**不会**发生什么。
 *
 * 「仍然关闭的权限」是这一段的重点（REQ-APPROVAL-001）：只说得到什么，
 * 用户无从判断这次放行的边界在哪里。三条否定句都由解析后的 Scope 决定，
 * 不是写死的宣传语。
 */
export function consequencesOf(folio: Folio, copy: Copy): Consequence[] {
  const resource = scopeResourceTextOf(folio)
  const operation = folio.scope.operation ?? folio.operation

  return [
    {
      mark: '✓',
      text: copy.folioGranted(operation, resource, durationTextOf(folio.scope, copy)),
      tone: 'granted',
    },
    { mark: '—', text: copy.folioWithheldResource(resource), tone: 'withheld' },
    { mark: '—', text: withheldOperationsTextOf(folio, operation, copy), tone: 'withheld' },
    { mark: '—', text: copy.folioWithheldCredential, tone: 'withheld' },
    { mark: '↺', text: rollbackTextOf(folio, copy), tone: 'note' },
    { mark: '↺', text: copy.folioRevocable, tone: 'note' },
  ]
}
