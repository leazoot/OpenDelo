import { describe, expect, it } from 'vitest'

import { EMPTY_INPUT, missingOf, toDraft, type CoordinateInput } from './coordinates'

/*
 * 坐标拼装的用例。
 *
 * 这条路上的错误在登记时看不出来 —— 后端只校验字段非空与来源可用，
 * 前缀写错要等到真去取凭据那一刻才暴露。
 */

const keychain = (overrides: Partial<CoordinateInput> = {}): CoordinateInput => ({
  ...EMPTY_INPUT,
  providerKind: 'macos-keychain',
  itemKind: 'internet',
  item: 'github.com',
  secretName: 'octocat',
  service: 'github',
  ...overrides,
})

const onePassword = (overrides: Partial<CoordinateInput> = {}): CoordinateInput => ({
  ...EMPTY_INPUT,
  providerKind: '1password',
  vault: 'Work',
  item: 'GitHub Bot',
  secretName: 'token',
  service: 'github',
  ...overrides,
})

describe('钥匙串的坐标', () => {
  it('拼成 keychain://<类型>/<服务>，字段是账号名', () => {
    // Fetch 那一侧解析的正是这个形状：keychain://internet/github.com
    // 之后走 `security find-internet-password -s github.com -a octocat -w`。
    const draft = toDraft(keychain())

    expect(draft.providerItemRef).toBe('keychain://internet/github.com')
    expect(draft.field).toBe('octocat')
  })

  it('应用密码走 generic，那是另一个 security 子命令', () => {
    expect(toDraft(keychain({ itemKind: 'generic' })).providerItemRef).toBe('keychain://generic/github.com')
  })

  it('身份名留空时用账号名 —— 那本来就是「谁」', () => {
    expect(toDraft(keychain()).accountLabel).toBe('octocat')
  })

  it('不问来源名称：登录钥匙串只有一个，没有第二个维度要区分', () => {
    expect(toDraft(keychain()).providerLabel).toBe('keychain-internet')
  })

  it('缺服务名或账号时不让提交', () => {
    expect(missingOf(keychain({ item: '' }))).toContain('item')
    expect(missingOf(keychain({ secretName: '' }))).toContain('secretName')
    // 钥匙串不问保险库，缺它不该拦住提交。
    expect(missingOf(keychain({ vault: '' }))).toHaveLength(0)
  })
})

describe('1Password 的坐标', () => {
  it('拼成 op://<保险库>/<条目>，字段单独一项', () => {
    // readURI 要求恰好两段：少一段就成了整个条目，多一段就越过了用户选的范围。
    const draft = toDraft(onePassword())

    expect(draft.providerItemRef).toBe('op://Work/GitHub Bot')
    expect(draft.field).toBe('token')
    expect(draft.providerLabel).toBe('Work')
  })

  it('身份名留空时用条目名', () => {
    expect(toDraft(onePassword()).accountLabel).toBe('GitHub Bot')
  })

  it('缺保险库时不让提交 —— 少了它拼出来的地址会指向整个保险库', () => {
    expect(missingOf(onePassword({ vault: '' }))).toContain('vault')
  })
})

describe('两种来源共通的部分', () => {
  it('前后空格不进坐标：拼进 URI 之后没人看得出多了一个空格', () => {
    const draft = toDraft(onePassword({ vault: '  Work  ', item: ' GitHub Bot ' }))

    expect(draft.providerItemRef).toBe('op://Work/GitHub Bot')
  })

  it('没选服务时不让提交', () => {
    expect(missingOf(onePassword({ service: '' }))).toContain('service')
    expect(missingOf(keychain({ service: '' }))).toContain('service')
  })

  it('填全了就没有缺的', () => {
    expect(missingOf(onePassword())).toHaveLength(0)
    expect(missingOf(keychain())).toHaveLength(0)
  })
})
