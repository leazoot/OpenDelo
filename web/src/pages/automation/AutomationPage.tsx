import { Link } from 'react-router'

import { cx } from '../../components/cx'
import { useIdentityLabels } from '../../data/identities'
import { usePreferences } from '../../data/preferences'
import { useTrustMemories } from '../../data/trustMemories'
import { useCopy, type Copy } from '../../i18n/copy'

import styles from './AutomationPage.module.css'
import {
  alwaysAsked,
  autoAllowed,
  bindingsOf,
  learnedRules,
  manuscriptPath,
  riskPolicies,
  type LearnedRule,
} from './automation'

/*
 * Automation —— 替代传统 Rules 的那一页（PRD §16.3、REQ-UI-006）。
 *
 * 设计稿没有画这一页，因此它用的是别处已有的语言：小标题、卡片、
 * Lease 标签的那套边框，不引入新色板也不引入新组件风格（AC3）。
 *
 * 这一页**只读**：模式在 Preferences 里切换，单条规则去文书里改。
 * 在这里再放一份开关，等于让同一件事有两个可以改它的地方。
 */

export function AutomationPage() {
  const copy = useCopy()
  const { preferences, isLoading: modeLoading, isError: modeFailed } = usePreferences()
  const { memories, isLoading: rulesLoading, isError: rulesFailed } = useTrustMemories()
  const identityNames = useIdentityLabels()

  const rules = learnedRules(memories, identityNames, copy)
  const bindings = bindingsOf(memories)
  const isLoading = modeLoading || rulesLoading
  const isError = modeFailed || rulesFailed

  if (isError) {
    return (
      <div className={styles.page}>
        <p className={styles.notice} role="status">
          <strong className={styles.noticeTitle}>{copy.automationErrorTitle}</strong>
          {copy.automationErrorBlurb}
        </p>
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <header className={styles.head}>
        <h1 className={styles.title}>{copy.automationTitle}</h1>
        <p className={styles.blurb}>{copy.automationBlurb}</p>
      </header>

      {isLoading && <p className={styles.empty}>{copy.automationLoading}</p>}

      {!isLoading && (
        <div className={styles.grid}>
          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.automationModeTitle}</h2>
            <p className={styles.mode}>{modeTextOf(preferences?.automation_mode ?? '', copy)}</p>
            <p className={styles.hint}>{copy.automationModeHint}</p>
            <Link className={styles.action} to="/preferences">
              {copy.automationModeGoto}
            </Link>
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.automationRiskTitle}</h2>
            <ul className={styles.rows}>
              {riskPolicies(preferences?.automation_mode ?? '', copy).map((policy) => (
                <li key={policy.level} className={styles.policy}>
                  <span className={cx(styles.level, styles[policy.level])}>{policy.level}</span>
                  <span className={styles.policyText}>{policy.text}</span>
                  {policy.isFixed && <span className={styles.fixed}>{copy.automationFixed}</span>}
                </li>
              ))}
            </ul>
          </section>

          <RuleSection
            title={copy.automationLearnedTitle}
            empty={copy.automationLearnedEmpty}
            rules={rules}
            copy={copy}
          />
          <RuleSection
            title={copy.automationAutoAllowTitle}
            empty={copy.automationAutoAllowEmpty}
            rules={autoAllowed(rules)}
            copy={copy}
          />
          <RuleSection
            title={copy.automationAlwaysAskTitle}
            empty={copy.automationAlwaysAskEmpty}
            rules={alwaysAsked(rules)}
            copy={copy}
          />

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.automationBindingsTitle}</h2>
            {bindings.length === 0 && <p className={styles.empty}>{copy.automationBindingsEmpty}</p>}
            <ul className={styles.rows}>
              {bindings.map((binding) => (
                <li key={binding.key} className={styles.binding}>
                  <span className={styles.mono}>
                    {copy.automationBindingLine(
                      binding.workspaceId,
                      identityNames.get(binding.identityId) ?? binding.identityId,
                    )}
                  </span>
                  <span className={styles.meta}>{copy.automationBindingRules(binding.ruleCount)}</span>
                </li>
              ))}
            </ul>
          </section>

          <section className={cx(styles.panel, styles.advanced)}>
            <h2 className={styles.eyebrow}>{copy.automationAdvancedTitle}</h2>
            <p className={styles.hint}>{copy.automationManuscriptHint}</p>
          </section>
        </div>
      )}
    </div>
  )
}

interface RuleSectionProps {
  readonly title: string
  readonly empty: string
  readonly rules: readonly LearnedRule[]
  readonly copy: Copy
}

function RuleSection({ title, empty, rules, copy }: RuleSectionProps) {
  return (
    <section className={styles.panel}>
      <h2 className={styles.eyebrow}>{title}</h2>
      {rules.length === 0 && <p className={styles.empty}>{empty}</p>}
      <ul className={styles.rows}>
        {rules.map((rule) => (
          <li key={rule.id} className={styles.rule}>
            <span className={styles.line}>
              <span className={styles.mono}>{rule.title}</span>
              <span className={styles.meta}>{rule.environment}</span>
              <span className={styles.meta}>{copy.automationCeiling(rule.riskCeiling)}</span>
            </span>
            {/* 每条授权都要说出自己是怎么来的（REQ-TRUST-001 AC3）。 */}
            <span className={styles.origin}>{rule.origin}</span>
            {rule.isInvalidated && (
              <span className={styles.invalid}>{copy.automationInvalidated(rule.invalidationReason)}</span>
            )}
            <Link className={styles.open} to={manuscriptPath(rule.id)}>
              {copy.automationOpenManuscript}
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** 认不出的模式照实说，而不是当成默认的平衡模式。 */
function modeTextOf(mode: string, copy: Copy): string {
  if (mode === 'cautious') {
    return copy.automationModeCautious
  }
  if (mode === 'balanced') {
    return copy.automationModeBalanced
  }
  if (mode === 'automatic') {
    return copy.automationModeAutomatic
  }
  return copy.automationModeUnknown
}
