import type { ConnectDraft, ProviderKind } from '../../data/identities'

/*
 * 坐标的形状 —— 每种来源各不相同（REQ-CRED-002/003）。
 *
 * 用户填的是他在密码管理器里真能看到的东西（保险库、条目、账号），
 * `op://` 与 `keychain://` 这两个前缀由这里拼出来。让用户自己拼有两个问题：
 * 前缀是取用侧的实现细节，写错了在登记时看不出来；而两种来源的
 * 「条目」「字段」含义并不相同 —— 钥匙串的 Field 是 `security -a` 的**账号名**，
 * 不是字段名，用同一组标签描述它们必然有一半是错的。
 */

/** 钥匙串条目的两种类型，对应 security 的两个子命令。 */
export const KEYCHAIN_ITEM_KINDS = ['internet', 'generic'] as const

export type KeychainItemKind = (typeof KEYCHAIN_ITEM_KINDS)[number]

/** 表单持有的原始输入。提交前由 toDraft 拼成后端要的坐标。 */
export interface CoordinateInput {
  readonly providerKind: ProviderKind
  /** 1Password：保险库名。钥匙串不用。 */
  readonly vault: string
  /** 1Password：条目名。钥匙串：服务名（`security -s`）。 */
  readonly item: string
  /** 1Password：字段名。钥匙串：账号名（`security -a`）。 */
  readonly secretName: string
  /** 钥匙串：条目类型。1Password 不用。 */
  readonly itemKind: KeychainItemKind
  readonly service: string
  readonly accountLabel: string
  readonly environment: 'production' | 'non-production'
}

export const EMPTY_INPUT: CoordinateInput = {
  providerKind: '1password',
  vault: '',
  item: '',
  secretName: '',
  itemKind: 'internet',
  service: '',
  accountLabel: '',
  environment: 'non-production',
}

/**
 * 拼出后端要的坐标。
 *
 * 来源名称不再问用户：钥匙串每个用户只有一个登录钥匙串，1Password 的保险库名
 * 本来就在坐标里。它只用来区分同一种类下的多个来源，用保险库名/条目类型
 * 已经足够，多问一遍只是让人再想一次。
 */
export function toDraft(input: CoordinateInput): ConnectDraft {
  const accountLabel = input.accountLabel.trim() === '' ? defaultAccountLabel(input) : input.accountLabel

  if (input.providerKind === 'macos-keychain') {
    return {
      providerKind: 'macos-keychain',
      // 来源名称是区分同种来源用的键，不是展示文本 —— 用条目类型即可，
      // 钥匙串每个用户只有一个登录钥匙串，没有第二个维度要区分。
      providerLabel: `keychain-${input.itemKind}`,
      providerItemRef: `keychain://${input.itemKind}/${input.item.trim()}`,
      field: input.secretName.trim(),
      service: input.service,
      accountLabel,
      environment: input.environment,
    }
  }

  return {
    providerKind: input.providerKind,
    providerLabel: input.vault.trim(),
    providerItemRef: `op://${input.vault.trim()}/${input.item.trim()}`,
    field: input.secretName.trim(),
    service: input.service,
    accountLabel,
    environment: input.environment,
  }
}

/**
 * 身份名的默认值。
 *
 * 钥匙串取账号名（那本来就是「谁」），1Password 取条目名。
 * 两者都是用户刚刚填过的东西，比让他再想一个名字省事。
 */
function defaultAccountLabel(input: CoordinateInput): string {
  const preferred = input.providerKind === 'macos-keychain' ? input.secretName : input.item
  return preferred.trim()
}

/** 这一份输入还缺什么。返回空数组表示可以提交。 */
export function missingOf(input: CoordinateInput): readonly (keyof CoordinateInput)[] {
  // 只列文本项。来源种类与环境是下拉，它们永远有值。
  const required: ('vault' | 'item' | 'secretName' | 'service')[] =
    input.providerKind === 'macos-keychain'
      ? ['item', 'secretName', 'service']
      : ['vault', 'item', 'secretName', 'service']

  return required.filter((name) => input[name].trim() === '')
}
