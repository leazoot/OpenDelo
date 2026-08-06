/*
 * 响应视图里 E2E 用到的那部分字段。
 *
 * 只声明断言要看的字段，不照抄整个视图：多写的字段不会被检查，
 * 却会在后端改动时让人以为这里核对过它们。
 */

/** DecisionView 是一次决策对外的样子。 */
export interface DecisionView {
  readonly verdict: 'auto_allow' | 'require_approval' | 'deny'
  readonly risk_level: string
  readonly identity_id: string
  readonly match_level: string
  readonly reason_code: string
  readonly trust_memory_id: string
}

/** ApprovalView 是一个待决项。 */
export interface ApprovalView {
  readonly id: string
  readonly status: string
  /** available_actions 是这个风险等级下允许提供的操作（REQ-APPROVAL-002）。 */
  readonly available_actions: string[]
  readonly request: {
    readonly service: string
    readonly operation: string
    readonly resource: Record<string, string>
  } | null
  readonly decision: DecisionView | null
}

/** IdentityView 是一个已连接的身份。 */
export interface IdentityView {
  readonly id: string
  readonly service: string
  readonly account_label: string
  readonly is_default?: boolean
}

/** AuditEventView 是账本里的一条记录。 */
export interface AuditEventView {
  readonly operation_id: string
  readonly type: string
  readonly service: string
  readonly operation: string
  readonly verdict: string
  readonly outcome: string
  readonly identity_id: string
  readonly lease_id: string
  readonly response_status: number
  readonly duration_ms: number
  /** metadata 是这条记录的补充元数据，例如 `{ face: 'proxy' }`。 */
  readonly metadata: Record<string, string>
}

/** TrustMemoryView 是一条自动授权记忆。 */
export interface TrustMemoryView {
  readonly id: string
  readonly service: string
  readonly status: string
}

/** listOf 是列表端点的信封。 */
export interface ListOf<T> {
  readonly items: T[]
  readonly next_cursor: string
}
