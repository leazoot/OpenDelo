import type { Verdict } from '../data/passages'
import type { Copy } from '../i18n/copy'

/*
 * Passage 上的三段文字。
 *
 * 单独成一个模块而不是留在组件里：它们是纯函数，用例直接调它们比渲染一遍再从
 * DOM 里读回来更直接。
 */

/** 结论的文字。颜色之外必须有文字（REQ-UI-009 AC4）。 */
export function verdictTextOf(verdict: Verdict, copy: Copy): string {
  switch (verdict) {
    case 'waiting':
      return copy.verdictWaiting
    case 'allowed':
      return copy.verdictAllowed
    case 'denied':
      return copy.verdictDenied
    case 'cancelled':
      return copy.verdictCancelled
  }
}

/** 风险等级的文字。认不出的等级说「未知」，不留空 —— 空白读起来像「没有风险」。 */
export function riskTextOf(level: string, copy: Copy): string {
  switch (level) {
    case 'low':
      return copy.riskLow
    case 'medium':
      return copy.riskMedium
    case 'high':
      return copy.riskHigh
    default:
      return copy.riskUnknown
  }
}

/**
 * 决策理由的说法（REQ-DECIDE-001 AC3）。
 *
 * 认不出的编码原样显示：编造一句「因为某些原因」比一个陌生的编码更没用。
 */
export function reasonTextOf(reason: string, copy: Copy): string {
  switch (reason) {
    case 'low_risk':
      return copy.reasonLowRisk
    case 'trust_memory_match':
      return copy.reasonTrustMemoryMatch
    case 'requires_confirmation':
      return copy.reasonRequiresConfirmation
    case 'high_risk':
      return copy.reasonHighRisk
    case 'beyond_learned_scope':
      return copy.reasonBeyondLearnedScope
    case 'identity_ambiguous':
      return copy.reasonIdentityAmbiguous
    case 'forbidden':
      return copy.reasonForbidden
    case 'fail_closed':
      return copy.reasonFailClosed
    default:
      return reason
  }
}
