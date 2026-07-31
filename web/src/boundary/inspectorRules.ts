import type { Passage } from '../data/passages'

/*
 * Inspector 上「能不能在这里决定」的判断。
 *
 * 单独成一个模块：Gate 的键盘处理也要问同一个问题，而两处各写一份判断
 * 就是让快捷键有一套自己的规则的开始。
 */

/** 高风险永远需要人当面确认，不在这里单键放行（REQ-DECIDE-003 AC3）。 */
export function mayDecideHere(passage: Passage): boolean {
  return passage.verdict === 'waiting' && passage.riskLevel !== 'high' && passage.approvalId !== ''
}
