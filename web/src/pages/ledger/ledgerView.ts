import type { LedgerRecord } from '../../data/ledger'
import type { Copy } from '../../i18n/copy'

/*
 * 账本条目读成人话（设计稿 §07、REQ-AUDIT-003）。
 *
 * 时间轴脊线是缝的纵向投影：左边是谁在缝外发起，右边是缝内发生了什么。
 * 这里只做筛选与措辞，**不做任何统计** —— 账本不是统计后台。
 */

/** 设计稿的四个过滤片：全部 / 通行 / 确认过 / 拒绝。 */
export type Lane = 'all' | 'passed' | 'confirmed' | 'refused'

export const LANES: readonly Lane[] = ['all', 'passed', 'confirmed', 'refused']

/**
 * 一条记录落在哪个片里。
 *
 * 按 `verdict` 分，而不是按 `outcome`：账本上「拒绝」问的是当时的决定，
 * 而不是这次执行成没成功。认不出的结论只出现在「全部」里 ——
 * 把它算进三个片中的任何一个，都是在替它下结论。
 */
export function laneOf(record: LedgerRecord): Lane | '' {
  if (record.verdict === 'allow') {
    return 'passed'
  }
  if (record.verdict === 'require_approval') {
    return 'confirmed'
  }
  if (record.verdict === 'deny') {
    return 'refused'
  }
  return ''
}

export function inLane(record: LedgerRecord, lane: Lane): boolean {
  return lane === 'all' || laneOf(record) === lane
}

export function filterLedger(records: readonly LedgerRecord[], lane: Lane): readonly LedgerRecord[] {
  return records.filter((record) => inLane(record, lane))
}

/** 时刻写成 HH:MM:SS（设计稿：`14:22:07`）。认不出的时刻照实说。 */
export function timeTextOf(iso: string, copy: Copy): string {
  const at = Date.parse(iso)
  if (Number.isNaN(at)) {
    return copy.ledgerTimeUnknown
  }
  const stamp = new Date(at)
  return [stamp.getHours(), stamp.getMinutes(), stamp.getSeconds()]
    .map((part) => part.toString().padStart(2, '0'))
    .join(':')
}

/** 结论那一列的措辞。认不出的结论原样显示，不折成三个里的一个。 */
export function verdictTextOf(record: LedgerRecord, copy: Copy): string {
  const lane = laneOf(record)
  if (lane === 'passed') {
    return copy.ledgerPassed
  }
  if (lane === 'confirmed') {
    return copy.ledgerConfirmed
  }
  if (lane === 'refused') {
    return copy.ledgerRefused
  }
  return record.type
}

/** 条目详情的键值明细（设计稿 §07 右栏）。空值不摆出来，摆一行空的没有信息。 */
export function detailsOf(record: LedgerRecord, copy: Copy): readonly (readonly [string, string])[] {
  const rows: (readonly [string, string])[] = [
    [copy.ledgerFieldAgent, record.agent_id],
    [copy.ledgerFieldDevice, record.device_id],
    [copy.ledgerFieldWorkspace, record.workspace_id],
    [copy.ledgerFieldIdentity, record.identity_id],
    [copy.ledgerFieldService, `${record.service} · ${record.operation}`],
    [copy.ledgerFieldRisk, record.risk_level],
    [copy.ledgerFieldOutcome, record.outcome],
    [copy.ledgerFieldDuration, copy.ledgerDuration(record.duration_ms)],
    [copy.ledgerFieldOperationID, record.operation_id],
  ]
  return rows.filter(([, value]) => value !== '' && value !== ' · ')
}

/**
 * 这条记录的 Lease 还收得回来吗（REQ-AUDIT-003 AC4）。
 *
 * 只有 `active` 才收得回。**没有 lease_id 与已经失效是两句不同的话**：
 * 前者是「这次没有签发授权」，后者是「签过，但它已经不在了」。
 */
export function revokeStateOf(record: LedgerRecord, copy: Copy): { readonly may: boolean; readonly why: string } {
  if (record.lease_id === '') {
    return { may: false, why: copy.ledgerNoLease }
  }
  if (record.lease_status !== 'active') {
    return { may: false, why: copy.ledgerLeaseGone(record.lease_status) }
  }
  return { may: true, why: '' }
}

/**
 * 「据此写规则」跳到哪里（REQ-AUDIT-003 AC3）。
 *
 * 预填的是这条记录的 Scope 三要素：Agent、身份、服务。走 URL 而不是内存，
 * 因此这条链接可以直接分享，刷新之后也还在（与 Identities 的草稿同一条路）。
 */
export function ruleDraftPath(record: LedgerRecord): string {
  const query = new URLSearchParams({
    agent: record.agent_id,
    identity: record.identity_id,
    service: record.service,
  })
  return `/automation/advanced/draft?${query.toString()}`
}
