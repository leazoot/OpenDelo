import { describe, expect, it } from 'vitest'

import { describeIdentity, identityListSchema } from './identities'

/*
 * 身份的可读写法。审批页要回答「用谁的身份去做这件事」。
 */

describe('身份', () => {
  it('账号标签带上环境 —— 同一个账号在两个环境里是两回事', () => {
    expect(
      describeIdentity({ id: 'id-1', service: 'github', account_label: 'ops@example.com', environment: 'production', is_default: false, status: 'active' }),
    ).toBe('ops@example.com · production')
  })

  it('没有环境时不留下一个孤零零的分隔符', () => {
    expect(describeIdentity({ id: 'id-1', service: 'github', account_label: 'ops', environment: '', is_default: false, status: 'active' })).toBe('ops')
  })

  it('响应里多出来的字段不会被带进来', () => {
    const parsed = identityListSchema.parse({
      items: [
        {
          id: 'id-1',
          service: 'github',
          account_label: 'ops',
          environment: 'production',
          is_default: false,
          status: 'active',
          credential_reference_id: 'cr-1',
        },
      ],
    })

    expect(parsed.items[0]).not.toHaveProperty('credential_reference_id')
  })
})
