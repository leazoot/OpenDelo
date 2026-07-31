import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router'

import { Inspector } from '../../boundary/Inspector'
import { mayDecideHere } from '../../boundary/inspectorRules'
import { LeaseRack } from '../../boundary/LeaseRack'
import { LedgerRibbon } from '../../boundary/LedgerRibbon'
import { Passage } from '../../boundary/Passage'
import { verdictTextOf } from '../../boundary/passageText'
import { Seam } from '../../boundary/Seam'
import { useAgentNames } from '../../data/agents'
import { isActionOffered, useSettleApproval, type DecisionAction } from '../../data/decisions'
import { useGatewayStatus } from '../../data/gatewayStatus'
import { usePassages, type Passage as PassageModel } from '../../data/passages'
import { useCopy } from '../../i18n/copy'
import { useSelectionStore } from '../../state/selection'

import { wantsFocusBack } from './foldState'
import styles from './GatePage.module.css'
import { useGateKeys, type GateKey } from './useGateKeys'

/**
 * Gate。缝居中，左侧恒为 Outside（代理侧），右侧恒为 Inside（受保护侧），
 * 这个方向不随页面改变。
 *
 * 缝在舞台里居中，Inspector 是舞台**旁边**的一列 —— 它收窄或展开抽屉时
 * 压缩的是舞台，缝在舞台里的位置不变（REQ-UI-002）。三态也只换舞台里
 * 那一层的内容（REQ-APPROVAL-006 AC3）。
 */
export function GatePage() {
  const copy = useCopy()
  const navigate = useNavigate()
  const { state, status } = useGatewayStatus()
  const { passages, isLoading, isError } = usePassages()
  const agentNames = useAgentNames()
  const { settle } = useSettleApproval()

  const selectedId = useSelectionStore((store) => store.selectedPassageId)
  const select = useSelectionStore((store) => store.select)
  const clear = useSelectionStore((store) => store.clear)
  const [isDrawerOpen, setDrawerOpen] = useState(false)

  const selected = passages.find((item) => item.id === selectedId) ?? null

  const onKey = useCallback(
    (intent: GateKey) => {
      if (intent === 'dismiss') {
        setDrawerOpen(false)
        clear()
        return
      }
      if (selected === null) {
        return
      }
      if (intent === 'open') {
        void navigate(`/gate/folio/${selected.id}`)
        return
      }
      // 高风险不在这里放行，后端没提供的操作也不从这里发出去（Fail Closed）。
      if (!mayDecideHere(selected)) {
        return
      }
      const action: DecisionAction =
        intent === 'allow' ? 'allow-task' : intent === 'allowOnce' ? 'allow-once' : 'deny'
      if (!isActionOffered(selected.availableActions, action)) {
        return
      }
      settle({ approvalId: selected.approvalId, action })
    },
    [selected, settle, navigate, clear],
  )

  useGateKeys(onKey)

  // 从卷宗折回时，焦点回到打开它的那一条。
  // 依赖里挂的是这一次导航的 key，因此列表刷新不会再把焦点抢回来。
  const stream = useRef<HTMLDivElement>(null)
  // react-router 把 state 的类型定成 any；收窄成 unknown，由 `wantsFocusBack` 认。
  const location: { readonly state: unknown; readonly key: string } = useLocation()
  const foldedBack = wantsFocusBack(location.state)
  useEffect(() => {
    if (!foldedBack) {
      return
    }
    const card = stream.current?.querySelector('[aria-pressed="true"]')
    if (card instanceof HTMLElement) {
      card.focus()
    }
  }, [foldedBack, location.key])

  // 最新一条穿越的主键。它变化时缝的凭据核心跳一次 —— 那正是有东西穿过去的时刻。
  const newest = passages[0]?.id ?? ''

  return (
    <div className={styles.gate}>
      <div className={styles.stage}>
        <Seam offline={state === 'disconnected'} pulseKey={newest} />

        <div className={styles.sides}>
          <span className={styles.outside}>{copy.outside}</span>
          <span className={styles.inside}>{copy.inside}</span>
        </div>

        <div className={styles.stream} ref={stream}>
          {isLoading && <PassageSkeleton />}
          {!isLoading && isError && (
            <Notice title={copy.gateErrorTitle} blurb={copy.gateErrorBlurb} tone="error" />
          )}
          {!isLoading && !isError && passages.length === 0 && (
            <Notice title={copy.gateEmptyTitle} blurb={copy.gateEmptyBlurb} tone="quiet" />
          )}
          {!isLoading && !isError && passages.length > 0 && (
            <ul className={styles.list} aria-label={copy.passageListLabel}>
              {passages.map((passage) => (
                <Passage
                  key={passage.id}
                  passage={passage}
                  agentName={agentNames.get(passage.agentId)}
                  copy={copy}
                  isSelected={passage.id === selectedId}
                  onSelect={() => {
                    // 再次点击同一条收回抽屉（REQ-UI-004 ②）。收的是抽屉不是选中：
                    // 1280 以上 Inspector 一直在，抽屉开合在那些宽度上没有效果。
                    if (passage.id === selectedId) {
                      setDrawerOpen(!isDrawerOpen)
                      return
                    }
                    select(passage.id)
                    setDrawerOpen(true)
                  }}
                />
              ))}
            </ul>
          )}
        </div>

        <Announcer passage={passages[0] ?? null} agentNames={agentNames} />

        <LeaseRack />
        <LedgerRibbon />
      </div>

      <Inspector
        passage={selected}
        agentName={selected === null ? undefined : agentNames.get(selected.agentId)}
        status={status}
        isDrawerOpen={isDrawerOpen}
      />
    </div>
  )
}

/**
 * 缝前的动静，播报给读屏。
 *
 * 只播报最新的那一条：新来一个请求，或者最上面那条的结论变了。整张列表每次
 * 都念一遍等于什么都没说，而这两件事恰恰是用户在等的。
 */
function Announcer({
  passage,
  agentNames,
}: {
  passage: PassageModel | null
  agentNames: ReadonlyMap<string, string>
}) {
  const copy = useCopy()
  const subject =
    passage === null
      ? ''
      : `${agentNames.get(passage.agentId) ?? passage.agentId} · ${passage.operation} · ${passage.service}`

  return (
    <p className={styles.announce} aria-live="polite">
      {passage === null ? '' : copy.gateAnnounce(subject, verdictTextOf(passage.verdict, copy))}
    </p>
  )
}

/**
 * 加载态。
 *
 * 骨架占的是与真实行完全相同的高度，因此缝两侧的内容不会在数据到达时跳一下
 */
function PassageSkeleton() {
  const copy = useCopy()

  return (
    <ul className={styles.list} aria-label={copy.gateLoading} aria-busy="true">
      {[0, 1, 2].map((slot) => (
        <li key={slot} className={styles.skeletonRow}>
          <div className={styles.skeletonOutside}>
            <span className={styles.skeletonCard} />
          </div>
          <div className={styles.skeletonInside} />
        </li>
      ))}
    </ul>
  )
}

/**
 * 空态与错误态。
 *
 * 两者用同一个安静的说明块：出错不是灾难，缝仍在，既有授权也没有变
 * （REQ-GATEWAY-003 AC2）。错误态因此也不用 `--block`。
 */
function Notice({ title, blurb, tone }: { title: string; blurb: string; tone: 'quiet' | 'error' }) {
  return (
    <div className={styles.notice} role={tone === 'error' ? 'status' : undefined}>
      <p className={styles.noticeTitle}>{title}</p>
      <p className={styles.noticeBlurb}>{blurb}</p>
    </div>
  )
}
