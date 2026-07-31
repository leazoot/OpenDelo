import { useId, useState } from 'react'

import { LOCKOUT_CODE } from '../data/vault'
import { useCopy } from '../i18n/copy'

import styles from './StrongAuthPrompt.module.css'

/*
 * 当面解锁（REQ-APPROVAL-005、REQ-TRUST-006 AC3，用户决定 D-14 方案 C）。
 *
 * 两处用得到它：卷宗上的高风险放行，以及文书的签署。放在 components/ 而不是
 * 某一页里，是因为第二处出现时复制一份意味着「主密码送出后清不清空」
 * 会有两个答案。
 *
 * 主密码由 Gateway 校验，这里只负责把它送过去。**密码不进任何全局状态、
 * 不进 URL、不进日志**：它只活在这个组件的 useState 里，解锁成功后
 * 这一段整个消失，输入框随之被卸载。
 */

export interface StrongAuthPromptProps {
  readonly isPending: boolean
  readonly failureCode: string
  readonly onUnlock: (masterPassword: string) => void
}

export function StrongAuthPrompt({ isPending, failureCode, onUnlock }: StrongAuthPromptProps) {
  const copy = useCopy()
  const fieldId = useId()
  const [masterPassword, setMasterPassword] = useState('')

  const lockedOut = failureCode === LOCKOUT_CODE

  return (
    <form
      className={styles.prompt}
      onSubmit={(event) => {
        event.preventDefault()
        if (masterPassword === '') {
          return
        }
        onUnlock(masterPassword)
        // 送出即忘：留着它只会让一份明文在内存里多活一段时间
        setMasterPassword('')
      }}
    >
      <label className={styles.label} htmlFor={fieldId}>
        {copy.folioUnlockLabel}
      </label>
      <div className={styles.row}>
        <input
          id={fieldId}
          className={styles.field}
          type="password"
          autoComplete="current-password"
          value={masterPassword}
          disabled={isPending || lockedOut}
          onChange={(event) => {
            setMasterPassword(event.target.value)
          }}
        />
        <button type="submit" className={styles.action} disabled={isPending || lockedOut}>
          {isPending ? copy.folioUnlockPending : copy.folioUnlockAction}
        </button>
      </div>
      {failureCode !== '' && (
        <p className={styles.failure} role="status">
          {lockedOut ? copy.folioUnlockLockedOut : copy.folioUnlockFailed}
        </p>
      )}
    </form>
  )
}
