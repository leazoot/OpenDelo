import { useId, useState } from 'react'

import { GatewayError } from '../../data/gateway'
import { PROVIDER_KINDS, useConnectIdentity, type ProviderKind } from '../../data/identities'
import { useCopy, type Copy } from '../../i18n/copy'

import {
  EMPTY_INPUT,
  KEYCHAIN_ITEM_KINDS,
  missingOf,
  toDraft,
  type CoordinateInput,
} from './coordinates'
import styles from './IdentitiesPage.module.css'

/*
 * 连接身份（REQ-CRED-002 AC1、REQ-IDENT-001）。
 *
 * 表单收的是一份凭据的**坐标**，而不是凭据。这里没有一个 password 类型的
 * 输入框，也不该有：明文从不经过 Web API（REQ-CRED-001）。
 *
 * 字段按来源换 —— 两种来源的坐标形状不同，用同一组标签描述它们
 * 必然有一半是错的（见 `coordinates.ts`）。
 *
 * Local Vault 的明文录入不在这里：那条路走 CLI 的交互式提示符，
 * 明文既不进浏览器也不进命令行参数。
 */

interface ConnectFormProps {
  /** 已声明 Adapter 的服务。为空表示这台 Gateway 认不出任何服务。 */
  readonly services: readonly string[]
  /** 收起表单。由页面持有开合状态，焦点才好还给那个触发它的按钮。 */
  readonly onClose: () => void
}

export function ConnectForm({ services, onClose }: ConnectFormProps) {
  const copy = useCopy()
  const prefix = useId()
  const [input, setInput] = useState<CoordinateInput>(EMPTY_INPUT)
  const { connect, isPending, isError, failure, reset } = useConnectIdentity()

  const isKeychain = input.providerKind === 'macos-keychain'
  const incomplete = missingOf(input).length > 0

  function change<K extends keyof CoordinateInput>(key: K, value: CoordinateInput[K]) {
    setInput((current) => ({ ...current, [key]: value }))
    // 改动之后上一次的失败说明就过期了，留着它会让人以为改完仍然不对。
    if (isError) {
      reset()
    }
  }

  return (
    <form
      className={styles.connectForm}
      aria-label={copy.identitiesConnect}
      onSubmit={(event) => {
        event.preventDefault()
        // 提交中不再受理第二次：重复提交会登记出两个身份（REQ-APPROVAL-006 AC2 的同一条）。
        if (incomplete || isPending) {
          return
        }
        connect(toDraft(input))
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          onClose()
        }
      }}
    >
      <p className={styles.connectBlurb}>{copy.identitiesConnectBlurb}</p>

      <div className={styles.connectGrid}>
        <Choice
          id={`${prefix}-kind`}
          label={copy.identitiesConnectKind}
          value={input.providerKind}
          disabled={isPending}
          onChange={(value) => {
            change('providerKind', kindOf(value))
          }}
        >
          {PROVIDER_KINDS.map((kind) => (
            // 本地保险库还没有接上取用路径，选了只会得到一次拒绝。
            // 画成禁用并说明，比留一个选得中却做不成的选项好（REQ-UI-007 AC2）。
            <option key={kind} value={kind} disabled={kind === 'local-vault'}>
              {kind === 'local-vault'
                ? copy.identitiesProviderKindLater(copy.identitiesProviderKind(kind))
                : copy.identitiesProviderKind(kind)}
            </option>
          ))}
        </Choice>

        {isKeychain ? (
          <Choice
            id={`${prefix}-itemkind`}
            label={copy.identitiesKeychainItemKind}
            value={input.itemKind}
            disabled={isPending}
            onChange={(value) => {
              change('itemKind', value === 'generic' ? 'generic' : 'internet')
            }}
          >
            {KEYCHAIN_ITEM_KINDS.map((kind) => (
              <option key={kind} value={kind}>
                {copy.identitiesKeychainItemKindName(kind)}
              </option>
            ))}
          </Choice>
        ) : (
          <TextField
            id={`${prefix}-vault`}
            label={copy.identitiesOnePasswordVault}
            hint={copy.identitiesOnePasswordVaultHint}
            value={input.vault}
            disabled={isPending}
            onChange={(value) => {
              change('vault', value)
            }}
          />
        )}

        <TextField
          id={`${prefix}-item`}
          label={isKeychain ? copy.identitiesKeychainService : copy.identitiesOnePasswordItem}
          hint={
            isKeychain
              ? copy.identitiesKeychainServiceHint(input.itemKind)
              : copy.identitiesOnePasswordItemHint
          }
          value={input.item}
          disabled={isPending}
          onChange={(value) => {
            change('item', value)
          }}
        />

        <TextField
          id={`${prefix}-secret`}
          label={isKeychain ? copy.identitiesKeychainAccount : copy.identitiesOnePasswordField}
          hint={isKeychain ? copy.identitiesKeychainAccountHint : copy.identitiesOnePasswordFieldHint}
          value={input.secretName}
          disabled={isPending}
          onChange={(value) => {
            change('secretName', value)
          }}
        />

        <Choice
          id={`${prefix}-service`}
          label={copy.identitiesConnectService}
          value={input.service}
          disabled={isPending || services.length === 0}
          onChange={(value) => {
            change('service', value)
          }}
        >
          <option value="">{copy.identitiesConnectServicePlaceholder}</option>
          {services.map((service) => (
            <option key={service} value={service}>
              {service}
            </option>
          ))}
        </Choice>

        <TextField
          id={`${prefix}-account`}
          label={copy.identitiesConnectAccount}
          hint={copy.identitiesConnectAccountHint}
          value={input.accountLabel}
          disabled={isPending}
          onChange={(value) => {
            change('accountLabel', value)
          }}
        />

        <Choice
          id={`${prefix}-env`}
          label={copy.identitiesConnectEnvironment}
          value={input.environment}
          disabled={isPending}
          onChange={(value) => {
            change('environment', value === 'production' ? 'production' : 'non-production')
          }}
        >
          <option value="non-production">{copy.identitiesEnvNonProduction}</option>
          <option value="production">{copy.identitiesEnvProduction}</option>
        </Choice>
      </div>

      <p className={styles.connectNote}>{copy.identitiesConnectNoSecret}</p>

      <div className={styles.draftActions}>
        <button type="submit" className={styles.sign} disabled={incomplete || isPending}>
          {isPending ? copy.identitiesConnecting : copy.identitiesConnectSubmit}
        </button>
        <button type="button" className={styles.discard} onClick={onClose}>
          {copy.identitiesConnectCancel}
        </button>
      </div>

      {services.length === 0 && <p className={styles.connectHint}>{copy.identitiesNoServices}</p>}

      {isError && (
        <p className={styles.connectFailure} role="status">
          {explain(failure, copy)}
        </p>
      )}
    </form>
  )
}

/**
 * 把 select 的取值收敛成一个已知的来源种类。
 *
 * 认不出的退回第一种而不是断言：断言只是让类型检查闭嘴，运行时的形状
 * 并没有被验证，而这个值会原样发给一个拒绝未知取值的端点。
 */
function kindOf(value: string): ProviderKind {
  return PROVIDER_KINDS.find((kind) => kind === value) ?? PROVIDER_KINDS[0]
}

/**
 * 把一次失败翻译成「发生了什么 + 下一步做什么」。
 *
 * 错误正文本身是脱敏后的通用句子，说不出是哪一项填错了；字段名走的是
 * 错误体里的 fields（REQ-CAP-001 AC1）。认不出的错误退回一句通用说明，
 * 而不是把原始文本铺到界面上。
 */
function explain(failure: GatewayError | null, copy: Copy): string {
  if (failure === null) {
    return copy.identitiesConnectFailed
  }
  if (failure.code === 'credential_not_authorized') {
    return copy.identitiesConnectUnresolved
  }
  if (failure.code === 'provider_unavailable') {
    return copy.identitiesConnectProviderDown
  }
  if (failure.code === 'conflict') {
    return copy.identitiesConnectConflict
  }
  if (failure.fields.length > 0) {
    return copy.identitiesConnectBadField(failure.fields.join('、'))
  }
  return copy.identitiesConnectFailed
}

interface FieldShellProps {
  readonly id: string
  readonly label: string
  readonly children: React.ReactNode
  readonly hint?: string
}

/**
 * 一个带标签的输入位。
 *
 * 提示语在 label 之外，用 aria-describedby 挂上去：写进 label 里的话，
 * 这个输入框的可及名称会变成「服务 钥匙串里的 service 名…」一整句，
 * 读屏用户每次聚焦都要听完。
 */
function FieldShell({ id, label, hint, children }: FieldShellProps) {
  return (
    <div className={styles.connectField}>
      <label className={styles.connectLabel} htmlFor={id}>
        {label}
      </label>
      {children}
      {hint !== undefined && (
        <span className={styles.connectHint} id={`${id}-hint`}>
          {hint}
        </span>
      )}
    </div>
  )
}

interface TextFieldProps {
  readonly id: string
  readonly label: string
  readonly hint: string
  readonly value: string
  readonly disabled: boolean
  readonly onChange: (value: string) => void
}

function TextField({ id, label, hint, value, disabled, onChange }: TextFieldProps) {
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <input
        id={id}
        className={styles.connectInput}
        type="text"
        autoComplete="off"
        aria-describedby={`${id}-hint`}
        value={value}
        disabled={disabled}
        onChange={(event) => {
          onChange(event.target.value)
        }}
      />
    </FieldShell>
  )
}

interface ChoiceProps {
  readonly id: string
  readonly label: string
  readonly value: string
  readonly disabled: boolean
  readonly onChange: (value: string) => void
  readonly children: React.ReactNode
}

function Choice({ id, label, value, disabled, onChange, children }: ChoiceProps) {
  return (
    <FieldShell id={id} label={label}>
      <select
        id={id}
        className={styles.connectInput}
        value={value}
        disabled={disabled}
        onChange={(event) => {
          onChange(event.target.value)
        }}
      >
        {children}
      </select>
    </FieldShell>
  )
}
