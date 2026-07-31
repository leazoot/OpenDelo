import { useId, useState } from 'react'

import { MIN_MASTER_PASSWORD, useCreateVault } from '../../data/vault'
import { useCopy } from '../../i18n/copy'

import styles from './PreferencesPage.module.css'

/*
 * 设定 Local Vault 的主密码（REQ-CRED-004 §2，用户决定 D-15）。
 *
 * 它是强认证的前提：保险库文件不存在时，任何主密码都解不开，高风险审批
 * 因此只能被拒绝。
 *
 * **不先问一句「这台机器上有没有保险库」**：那个答案正是解锁路径拒绝泄漏的
 * 东西。直接提交，已存在时后端拒绝且不覆盖，界面照实转述。
 */

export function VaultSetup() {
  const copy = useCopy()
  const fieldId = useId()
  const [masterPassword, setMasterPassword] = useState('')
  const { create, isCreated, isPending, failureCode } = useCreateVault()

  if (isCreated) {
    return (
      <span className={styles.value} role="status">
        {copy.prefsVaultDone}
      </span>
    )
  }

  const tooShort = masterPassword.length > 0 && masterPassword.length < MIN_MASTER_PASSWORD

  return (
    <form
      className={styles.vault}
      onSubmit={(event) => {
        event.preventDefault()
        if (masterPassword.length < MIN_MASTER_PASSWORD) {
          return
        }
        create({ masterPassword })
        // 送出即忘：一份明文在内存里活多久，就有多久可能被别的代码读到。
        setMasterPassword('')
      }}
    >
      <label className={styles.label} htmlFor={fieldId}>
        {copy.prefsVaultSet}
      </label>
      <span className={styles.vaultRow}>
        <input
          id={fieldId}
          className={styles.field}
          type="password"
          autoComplete="new-password"
          value={masterPassword}
          disabled={isPending}
          onChange={(event) => {
            setMasterPassword(event.target.value)
          }}
        />
        <button
          type="submit"
          className={styles.action}
          disabled={isPending || masterPassword.length < MIN_MASTER_PASSWORD}
        >
          {isPending ? copy.prefsVaultSetting : copy.prefsVaultSet}
        </button>
      </span>
      <span className={styles.hint}>{copy.prefsVaultHint(MIN_MASTER_PASSWORD)}</span>
      {tooShort && <span className={styles.hint}>{copy.prefsVaultHint(MIN_MASTER_PASSWORD)}</span>}
      {failureCode !== '' && (
        <span className={styles.value} role="status">
          {failureCode === 'conflict' ? copy.prefsVaultExists : copy.prefsVaultFailed}
        </span>
      )}
    </form>
  )
}
