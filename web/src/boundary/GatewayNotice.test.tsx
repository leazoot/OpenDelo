import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'

import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { GatewayNotice } from './GatewayNotice'

// 只看声明，不看注释：注释里提到令牌名是在说明「为什么不用它」。
const noticeDeclarations = readFileSync(
  resolve(process.cwd(), 'src/boundary/GatewayNotice.module.css'),
  'utf8',
).replace(/\/\*[\s\S]*?\*\//g, '')

const zh = copyFor('zh')
const en = copyFor('en')

describe('GatewayNotice 的写法约束', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
  })

  it('离线说明不使用 --block 色（REQ-UI-012 AC1）', () => {
    // 连不上不是一次错误，是那一侧还没应答。用阻断色会把它讲成事故。
    expect(noticeDeclarations).not.toContain('--block')
  })

  it('用的都是设计令牌，没有字面色值', () => {
    expect(noticeDeclarations).not.toMatch(/#[0-9a-fA-F]{3,8}|\brgba?\s*\(|\bhsla?\s*\(/)
  })

  it.each([
    ['zh', ['受限模式', '受限', '降级', '不可用']],
    ['en', ['limited', 'degraded', 'unavailable', 'read-only']],
  ] as const)('%s 文案不把网页说成「受限模式」', (language, forbiddenWords) => {
    useLanguageStore.setState({ language })
    render(<GatewayNotice state="disconnected" />)

    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of forbiddenWords) {
      expect(text).not.toContain(forbidden)
    }
  })

  it('离线说明先讲现状，再给出可执行的下一步', () => {
    render(<GatewayNotice state="disconnected" />)

    expect(screen.getByText(zh.gatewayOfflineTitle)).toBeInTheDocument()
    expect(screen.getByText(/没有任何请求会绕过这道缝/)).toBeInTheDocument()
    expect(screen.getByText('opendelo start')).toBeInTheDocument()
  })

  it('已连接时不显示任何说明', () => {
    const { container } = render(<GatewayNotice state="connected" />)

    expect(container).toBeEmptyDOMElement()
  })

  it('连接中时说明还没有结论，不预告任何授权状态', () => {
    render(<GatewayNotice state="connecting" />)

    expect(screen.getByText(zh.gatewayConnectingTitle)).toBeInTheDocument()
    expect(screen.getByText(/确认之前不会显示任何授权状态/)).toBeInTheDocument()
  })

  it('切到英文之后说明也是英文，恢复命令不变', () => {
    // 这一页的两段说明一度是硬编码中文：英文界面上它们原样留在那里。
    useLanguageStore.setState({ language: 'en' })
    render(<GatewayNotice state="disconnected" />)

    expect(screen.getByText(en.gatewayOfflineTitle)).toBeInTheDocument()
    expect(screen.getByText(en.gatewayOfflineBody)).toBeInTheDocument()
    // 命令是要照着敲的东西，两种语言下必须是同一个字符串。
    expect(screen.getByText('opendelo start')).toBeInTheDocument()
  })
})
