import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'

import { cx } from '../../components/cx'
import { useAgentNames } from '../../data/agents'
import { downloadExport, useLedger, type LedgerRecord } from '../../data/ledger'
import { useRevokeLease } from '../../data/leases'
import { useCopy, type Copy } from '../../i18n/copy'

import styles from './LedgerPage.module.css'
import {
  detailsOf,
  filterLedger,
  laneOf,
  LANES,
  revokeStateOf,
  ruleDraftPath,
  timeTextOf,
  verdictTextOf,
  type Lane,
} from './ledgerView'

/*
 * Boundary Ledger（设计稿 §07、REQ-AUDIT-003）。
 *
 * 时间轴脊线是缝的纵向投影：左边是谁在缝外发起（含设备），右边是缝内发生了什么。
 * **没有图表**（AC2）—— 账本是一条一条的事实，不是统计后台；要看趋势就导出去。
 */

export function LedgerPage() {
  const copy = useCopy()
  const agentNames = useAgentNames()
  const [params, setParams] = useSearchParams()

  // 过滤片与选中项走 URL：一次筛选的结果可以直接分享给同一条缝前的另一个窗口
  const lane = laneFrom(params.get('lane'))
  const selectedId = params.get('entry') ?? ''

  const query = { agentId: params.get('agent') ?? '', service: params.get('service') ?? '' }
  const { records, isLoading, isError, isWindowFull } = useLedger(query)
  const { revoke, pendingId: revokingId } = useRevokeLease()
  const [exporting, setExporting] = useState(false)
  const [exportFailed, setExportFailed] = useState(false)

  const shown = filterLedger(records, lane)
  const selected = shown.find((record) => record.id === selectedId) ?? null

  function setParam(key: string, value: string) {
    const next = new URLSearchParams(params)
    if (value === '') {
      next.delete(key)
    } else {
      next.set(key, value)
    }
    setParams(next, { replace: true })
  }

  async function exportJsonl() {
    setExporting(true)
    setExportFailed(false)
    try {
      await downloadExport(query, 'jsonl')
    } catch {
      // 导出失败不影响账本本身，但必须说出来 —— 静静地什么都不发生，
      // 用户会以为文件已经存好了。
      setExportFailed(true)
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className={styles.page}>
      <header className={styles.bar}>
        <div className={styles.lanes} role="group" aria-label={copy.ledgerAll}>
          {LANES.map((each) => (
            <button
              key={each}
              type="button"
              className={cx(styles.lane, each === lane && styles.laneOn)}
              aria-pressed={each === lane}
              onClick={() => {
                setParam('lane', each === 'all' ? '' : each)
              }}
            >
              {each !== 'all' && <span className={cx(styles.dot, styles[each])} aria-hidden="true" />}
              {laneTextOf(each, copy)}
            </button>
          ))}
        </div>
        <button
          type="button"
          className={styles.export}
          disabled={exporting}
          onClick={() => {
            void exportJsonl()
          }}
        >
          {exporting ? copy.ledgerExporting : copy.ledgerExport('jsonl')}
        </button>
      </header>

      {isError ? (
        <p className={styles.notice} role="status">
          <strong className={styles.noticeTitle}>{copy.ledgerErrorTitle}</strong>
          {copy.ledgerErrorBlurb}
        </p>
      ) : (
        <div className={styles.body}>
          <section className={styles.timeline} aria-label={copy.ledgerAll}>
            <span className={styles.spine} aria-hidden="true" />

            {isLoading && <p className={styles.empty}>{copy.ledgerPageLoading}</p>}
            {!isLoading && records.length === 0 && <p className={styles.empty}>{copy.ledgerPageEmpty}</p>}
            {!isLoading && records.length > 0 && shown.length === 0 && (
              <p className={styles.empty}>{copy.ledgerEmptyLane}</p>
            )}

            <ul className={styles.entries}>
              {shown.map((record) => (
                <li key={record.id}>
                  <button
                    type="button"
                    className={cx(styles.entry, record.id === selectedId && styles.entryOn)}
                    aria-pressed={record.id === selectedId}
                    onClick={() => {
                      setParam('entry', record.id)
                    }}
                  >
                    <span className={styles.who}>
                      <span className={styles.agent}>{agentNames.get(record.agent_id) ?? record.agent_id}</span>
                      <span className={styles.stamp}>
                        {timeTextOf(record.created_at, copy)} · {shortId(record.device_id)}
                      </span>
                    </span>
                    <span className={styles.pin} aria-hidden="true">
                      <span className={cx(styles.dot, styles[laneClassOf(record)])} />
                    </span>
                    <span className={styles.what}>
                      <span className={styles.line}>
                        <span className={cx(styles.verdict, styles[laneClassOf(record)])}>
                          {verdictTextOf(record, copy)}
                        </span>
                        <span className={styles.action}>
                          {record.service} · {record.operation}
                        </span>
                      </span>
                      <span className={styles.detail}>{record.operation_id}</span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>

            {isWindowFull && <p className={styles.window}>{copy.ledgerWindow(records.length)}</p>}
          </section>

          <aside className={styles.entryPanel} aria-label={copy.ledgerEntry('')}>
            {selected === null ? (
              <p className={styles.empty}>{copy.ledgerEmptyEntry}</p>
            ) : (
              <Entry
                record={selected}
                copy={copy}
                agent={agentNames.get(selected.agent_id) ?? selected.agent_id}
                revoking={revokingId !== ''}
                onRevoke={() => {
                  revoke(selected.lease_id)
                }}
              />
            )}
            <p className={styles.localOnly}>{copy.ledgerLocalOnly}</p>
            {exportFailed && (
              <p className={styles.failed} role="status">
                {copy.ledgerExportFailed}
              </p>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}

interface EntryProps {
  readonly record: LedgerRecord
  readonly copy: Copy
  readonly agent: string
  readonly revoking: boolean
  readonly onRevoke: () => void
}

function Entry({ record, copy, agent, revoking, onRevoke }: EntryProps) {
  const revokeState = revokeStateOf(record, copy)

  return (
    <>
      <p className={styles.eyebrow}>{copy.ledgerEntry(timeTextOf(record.created_at, copy))}</p>
      <h2 className={styles.entryTitle}>
        {copy.ledgerEntryTitle(agent, `${record.service} · ${record.operation}`)}
      </h2>

      <dl className={styles.fields}>
        {detailsOf(record, copy).map(([key, value]) => (
          <div key={key} className={styles.field}>
            <dt className={styles.fieldKey}>{key}</dt>
            <dd className={styles.fieldValue}>{value}</dd>
          </div>
        ))}
      </dl>

      {record.is_redacted && <p className={styles.localOnly}>{copy.ledgerRedacted}</p>}

      <div className={styles.actions}>
        {/* 预填的是这条记录的 Scope 三要素，走 URL 交给文书（AC3）。 */}
        <Link className={styles.entryAction} to={ruleDraftPath(record)}>
          {copy.ledgerWriteRule}
        </Link>
        <button
          type="button"
          className={cx(styles.entryAction, styles.revoke)}
          disabled={!revokeState.may || revoking}
          onClick={onRevoke}
        >
          {copy.ledgerRevoke}
        </button>
      </div>
      {/* 置灰之外还要说明为什么（AC4）：一个灰按钮本身回答不了「为什么」。 */}
      {!revokeState.may && <p className={styles.why}>{revokeState.why}</p>}
    </>
  )
}

function laneTextOf(lane: Lane, copy: Copy): string {
  if (lane === 'passed') {
    return copy.ledgerPassed
  }
  if (lane === 'confirmed') {
    return copy.ledgerConfirmed
  }
  if (lane === 'refused') {
    return copy.ledgerRefused
  }
  return copy.ledgerAll
}

/** 认不出结论的那一条用中性样式，不借用三个片里任何一个的颜色。 */
function laneClassOf(record: LedgerRecord): Lane {
  const lane = laneOf(record)
  return lane === '' ? 'all' : lane
}

/** URL 里认不出的过滤片退回「全部」：它不隐藏任何记录。 */
function laneFrom(raw: string | null): Lane {
  return LANES.find((lane) => lane === raw) ?? 'all'
}

/** 设备主键取后六位：够把两台机器分开，又不必把整串 ULID 摆在每一行上。 */
function shortId(id: string): string {
  return id === '' ? '—' : id.slice(-6)
}
