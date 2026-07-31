import { useState } from 'react'
import { Link, useParams } from 'react-router'

import { cx } from '../../components/cx'
import { StrongAuthPrompt } from '../../components/StrongAuthPrompt'
import { useAgentNames } from '../../data/agents'
import { useNow } from '../../data/clock'
import { useIdentityLabels } from '../../data/identities'
import { useTightenMemory, useTrustMemories, type TrustMemory } from '../../data/trustMemories'
import { useUnlockVault } from '../../data/vault'
import { useCopy, type Copy } from '../../i18n/copy'

import { clockTextOf, loadDraft, saveDraft } from './draft'
import {
  asksEveryTime,
  hasPendingChange,
  manuscriptOf,
  segmentsOf,
  toYaml,
  type Impact,
  type SlotName,
} from './manuscript'
import styles from './ManuscriptPage.module.css'
import { useManuscriptImpact } from './useManuscriptImpact'

/*
 * Rule Manuscript（设计稿 §06、REQ-TRUST-006）。
 *
 * 一份文书是一条规则读成的散文，可编辑处是**带下划线的行内槽位**而不是输入框
 * （AC1）。文书**只能收紧**：把「直接通行」改成「当面确认」是它唯一能签下去的
 * 改动；别的槽位照样有下划线，但那是措辞不是旋钮 —— 放宽在 API 里连表达
 * 都表达不出来（REQ-TRUST-002），给一个拧不动的旋钮只会让人以为自己能拧。
 */

const ALWAYS_ASK = 'always_ask'

export function ManuscriptPage() {
  const copy = useCopy()
  const { ruleId } = useParams()
  const now = useNow()

  const { memories, isLoading } = useTrustMemories()
  const agentNames = useAgentNames()
  const identityNames = useIdentityLabels()
  const record = memories.find((memory) => memory.id === ruleId) ?? null

  const [showYaml, setShowYaml] = useState(false)
  const [draft, setDraft] = useState(() => loadDraft(ruleId ?? ''))

  const { unlock, isUnlocked, isPending: unlocking, failureCode } = useUnlockVault()
  const { tighten, isPending: signing, isDone: signed } = useTightenMemory()
  const impact = useManuscriptImpact(record?.agent_id ?? '', record, now)

  if (isLoading) {
    return <p className={styles.notice}>{copy.manuscriptLoading}</p>
  }
  if (record === null) {
    return (
      <div className={styles.notice} role="status">
        <strong className={styles.noticeTitle}>{copy.manuscriptNotFound}</strong>
        <p className={styles.blurb}>{copy.manuscriptNotFoundBlurb}</p>
        <Link className={styles.back} to="/automation">
          {copy.manuscriptBack}
        </Link>
      </div>
    )
  }

  const manuscript = manuscriptOf(record)
  const behavior = draft?.behavior ?? manuscript.behavior
  const pending = hasPendingChange(manuscript.behavior, behavior)
  const maySign = pending && isUnlocked && !signing

  function draftAlwaysAsk() {
    const drafted = { behavior: ALWAYS_ASK, savedAt: clockTextOf(now) }
    setDraft(drafted)
    saveDraft(record?.id ?? '', drafted)
  }

  const slots = {
    agent: {
      text: agentNames.get(manuscript.agentId) ?? copy.manuscriptSlotAgent,
      editable: false,
    },
    operation: { text: copy.manuscriptSlotOperation, editable: false },
    resource: {
      text: identityNames.get(manuscript.identityId) ?? manuscript.service,
      editable: false,
    },
    behavior: {
      text: asksEveryTime(behavior) ? copy.manuscriptBehaviorAsk : copy.manuscriptBehaviorAuto,
      editable: !asksEveryTime(behavior),
    },
  }

  return (
    <div className={styles.page}>
      <header className={styles.bar}>
        <span className={styles.saved}>
          {draft === null ? copy.manuscriptUnsaved : copy.manuscriptSaved(draft.savedAt)}
        </span>
        <button
          type="button"
          className={styles.toggle}
          onClick={() => {
            setShowYaml(!showYaml)
          }}
        >
          {showYaml ? copy.manuscriptStructured : copy.manuscriptYaml}
        </button>
        <button
          type="button"
          className={styles.sign}
          disabled={!maySign}
          onClick={() => {
            tighten(manuscript.id)
          }}
        >
          {copy.manuscriptSign}
        </button>
      </header>

      <div className={styles.body}>
        <aside className={styles.list} aria-label={copy.manuscriptList}>
          <h2 className={styles.eyebrow}>{copy.manuscriptList}</h2>
          <ul className={styles.items}>
            {memories.map((memory) => (
              <li key={memory.id}>
                <Link
                  className={cx(styles.item, memory.id === manuscript.id && styles.current)}
                  to={`/automation/advanced/${encodeURIComponent(memory.id)}`}
                >
                  <span className={styles.itemName}>{memory.service}</span>
                  <span className={styles.itemMeta}>{memory.environment}</span>
                </Link>
              </li>
            ))}
          </ul>
        </aside>

        {/*
          不是 <main>：外壳已经有一个了，两个 main 会让读屏用户听见两个
          「主要内容」而无从分辨哪个是（axe landmark-no-duplicate-main）。
          这里的 <h1> 已经标出了它是什么。
        */}
        <section className={styles.sheet}>
          <p className={styles.eyebrow}>{copy.manuscriptEyebrow}</p>
          <h1 className={styles.title}>{manuscript.title}</h1>

          {showYaml ? (
            <pre className={styles.yaml}>{toYaml({ ...manuscript, behavior })}</pre>
          ) : (
            <>
              <p className={styles.prose}>
                {segmentsOf(copy.manuscriptProse).map((segment) =>
                  segment.slot === '' ? (
                    <span key={segment.key}>{segment.text}</span>
                  ) : (
                    <Slot
                      key={segment.key}
                      name={segment.slot}
                      text={slots[segment.slot].text}
                      editable={slots[segment.slot].editable}
                      fixedHint={copy.manuscriptSlotFixed}
                      onEdit={draftAlwaysAsk}
                    />
                  ),
                )}
              </p>
              <p className={styles.prose}>
                {segmentsOf(copy.manuscriptProseTail).map((segment) =>
                  segment.slot === '' ? (
                    <span key={segment.key}>{segment.text}</span>
                  ) : (
                    <span key={segment.key} className={styles.slot} title={copy.manuscriptSlotFixed}>
                      {manuscript.riskCeiling}
                    </span>
                  ),
                )}
              </p>
              {asksEveryTime(behavior) && !pending && (
                <p className={styles.blurb}>{copy.manuscriptTightenDone}</p>
              )}
            </>
          )}

          <section className={styles.foot}>
            <div className={styles.column}>
              <h2 className={styles.eyebrow}>{copy.manuscriptImpactTitle}</h2>
              <ImpactLine impact={impact} copy={copy} />
            </div>
            <div className={styles.column}>
              <h2 className={styles.eyebrow}>{copy.manuscriptSignTitle}</h2>
              <p className={styles.blurb}>{copy.manuscriptSignBlurb}</p>
              {/* 签署要先当面解锁（AC3）；解锁之前那个按钮一直是灰的。 */}
              {!isUnlocked && (
                <StrongAuthPrompt
                  isPending={unlocking}
                  failureCode={failureCode}
                  onUnlock={(masterPassword) => {
                    unlock({ masterPassword })
                  }}
                />
              )}
              {signed && (
                <p className={styles.blurb} role="status">
                  {copy.manuscriptSigned}
                </p>
              )}
              {!pending && !signed && <p className={styles.blurb}>{copy.manuscriptNoChange}</p>}
            </div>
          </section>
        </section>

        <aside className={styles.margin} aria-label={copy.manuscriptMarginTitle}>
          <h2 className={styles.eyebrow}>{copy.manuscriptMarginTitle}</h2>
          <ul className={styles.notes}>
            {marginNotes(record, copy).map((note) => (
              <li key={note} className={styles.note}>
                {note}
              </li>
            ))}
          </ul>

          <h2 className={cx(styles.eyebrow, styles.seamTitle)}>{copy.manuscriptSeamTitle}</h2>
          <div className={styles.seamPreview}>
            <span className={styles.seamLine} aria-hidden="true" />
            <span
              className={cx(styles.notch, asksEveryTime(behavior) ? styles.notchAsk : styles.notchAuto)}
              aria-hidden="true"
            />
            <span className={styles.seamText}>
              {asksEveryTime(behavior) ? copy.manuscriptSeamAsk : copy.manuscriptSeamAuto}
            </span>
          </div>
        </aside>
      </div>
    </div>
  )
}

interface SlotProps {
  readonly name: SlotName
  readonly text: string
  readonly editable: boolean
  readonly fixedHint: string
  readonly onEdit: () => void
}

/**
 * 一处槽位：**行内下划线，不是输入框**（AC1）。
 *
 * 能改的那一处是按钮（键盘可达），改不动的那些是带说明的文本 ——
 * 让它们长得一样、但一个点得动一个点不动，比不让它长成控件更糟。
 */
function Slot({ name, text, editable, fixedHint, onEdit }: SlotProps) {
  if (!editable) {
    return (
      <span className={styles.slot} data-slot={name} title={fixedHint}>
        {text}
      </span>
    )
  }
  return (
    <button type="button" className={cx(styles.slot, styles.slotEditable)} data-slot={name} onClick={onEdit}>
      {text}
    </button>
  )
}

function ImpactLine({ impact, copy }: { readonly impact: Impact; readonly copy: Copy }) {
  return (
    <p className={styles.blurb}>
      {copy.manuscriptImpact(String(impact.confirmed), String(impact.passed), String(impact.refused))}
      {impact.isPartial && ` ${copy.manuscriptImpactPartial}`}
    </p>
  )
}

/**
 * 批注不是用户写的字。
 *
 * 没有存放批注的地方（PRD §27 里没有这样的端点），因此这里的每一条都是
 * 从这条规则本身读出来的事实 —— 一个可以输入却存不下的批注框，
 * 会让用户的字在刷新之后无声消失。
 */
function marginNotes(memory: TrustMemory, copy: Copy): readonly string[] {
  return [
    copy.manuscriptMarginOrigin(memory.created_at.slice(0, 10)),
    copy.manuscriptMarginCeiling(memory.risk_ceiling),
    copy.manuscriptMarginTighten,
  ]
}
