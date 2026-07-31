import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { Passage as PassageModel } from '../data/passages'
import { copyFor } from '../i18n/copy'

import { Passage } from './Passage'
import { reasonTextOf, riskTextOf, verdictTextOf } from './passageText'

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/boundary/Passage.module.css'), 'utf8')

function ruleBlock(selector: string): string {
  const match = new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`).exec(css)
  if (!match?.[1]) {
    throw new Error(`未找到规则 ${selector}`)
  }
  return match[1]
}

/** 取出每个 @media 块的内容。数括号而不是按空行切，嵌套的规则才不会被截断。 */
function mediaBlocks(source: string): string[] {
  const blocks: string[] = []
  for (let at = source.indexOf('@media'); at >= 0; at = source.indexOf('@media', at + 1)) {
    const open = source.indexOf('{', at)
    let depth = 0
    for (let cursor = open; cursor < source.length; cursor++) {
      if (source[cursor] === '{') {
        depth++
      } else if (source[cursor] === '}') {
        depth--
        if (depth === 0) {
          blocks.push(source.slice(open + 1, cursor))
          break
        }
      }
    }
  }
  return blocks
}

const passage = (overrides: Partial<PassageModel> = {}): PassageModel => ({
  id: 'rq-1',
  agentId: '01JAGENT',
  service: 'github',
  operation: 'read',
  resource: 'src/',
  verdict: 'waiting',
  riskLevel: 'medium',
  reason: '',
  approvalId: 'ap-1',
  availableActions: [],
  at: '2026-07-29T00:00:00Z',
  ...overrides,
})

function renderPassage(model: PassageModel, agentName?: string) {
  return render(
    <ul>
      <Passage passage={model} agentName={agentName} copy={zh} />
    </ul>,
  )
}

describe('Passage', () => {
  it('显示 Agent 的名字、动作与目的地', () => {
    renderPassage(passage(), 'writer-agent')

    expect(screen.getByText('writer-agent')).toBeInTheDocument()
    expect(screen.getByText(/read · src\//)).toBeInTheDocument()
    expect(screen.getByText('github')).toBeInTheDocument()
  })

  it('名字还没拉回来时退回显示主键，而不是留一个空位', () => {
    renderPassage(passage())

    expect(screen.getByText('01JAGENT')).toBeInTheDocument()
  })

  it('四种结论都有文字，颜色不是唯一的信息载体（REQ-UI-009 AC4）', () => {
    for (const [verdict, text] of [
      ['waiting', zh.verdictWaiting],
      ['allowed', zh.verdictAllowed],
      ['denied', zh.verdictDenied],
      ['cancelled', zh.verdictCancelled],
    ] as const) {
      const { unmount } = renderPassage(passage({ verdict }))
      expect(screen.getByText(new RegExp(text)), verdict).toBeInTheDocument()
      unmount()
    }
  })

  it('风险等级有文字标识，认不出的等级说未知而不是留空', () => {
    expect(riskTextOf('high', zh)).toBe(zh.riskHigh)
    expect(riskTextOf('', zh)).toBe(zh.riskUnknown)

    renderPassage(passage({ riskLevel: 'high' }))
    expect(screen.getByText(zh.riskHigh)).toBeInTheDocument()
  })

  it('自动允许的那条显示它的理由（REQ-DECIDE-001 AC3）', () => {
    renderPassage(passage({ verdict: 'allowed', reason: 'trust_memory_match' }))

    expect(screen.getByText(new RegExp(zh.reasonTrustMemoryMatch))).toBeInTheDocument()
  })

  it('认不出的理由编码原样显示，不编一句话盖住它', () => {
    expect(reasonTextOf('some_new_reason', zh)).toBe('some_new_reason')
  })

  it('等待中的那条不显示理由 —— 还没有结论可讲', () => {
    renderPassage(passage())

    expect(screen.getByText(zh.verdictWaiting).textContent).toBe(zh.verdictWaiting)
  })

  it('结论的文字与顺序在两种语言下都齐备', () => {
    const en = copyFor('en')
    for (const verdict of ['waiting', 'allowed', 'denied', 'cancelled'] as const) {
      expect(verdictTextOf(verdict, en)).not.toBe('')
    }
  })
})

describe('Passage 的样式', () => {
  it('结论只改颜色与底色，不改任何几何', () => {
    // 被拒绝的请求走的是同样长的一段路。几何一变，缝两侧的对齐就散了。
    for (const selector of ['.waiting .card', '.denied .card', '.waiting .trace', '.allowed .trace']) {
      const block = ruleBlock(selector)
      for (const property of ['width:', 'height:', 'left:', 'top:', 'margin:', 'padding:', 'flex:']) {
        expect(block, selector).not.toContain(property)
      }
    }
  })

  it('穿越动画只用 transform 与 opacity（REQ-UI-010 AC3）', () => {
    const frames = /@keyframes reqL\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(frames).toContain('transform')
    expect(frames).toContain('opacity')
    for (const property of ['left:', 'width:', 'height:', 'margin:', 'padding:']) {
      expect(frames).not.toContain(property)
    }
  })

  it('收窄断点只改宽度，不动任何定位', () => {
    // 收窄时压缩两侧的列，绝不推移缝（REQ-UI-002）。
    expect(css).toContain('max-width: 1439px')
    expect(css).toContain('max-width: 1279px')

    for (const block of mediaBlocks(css)) {
      for (const property of ['position:', 'left:', 'right:', 'top:', 'transform:', 'margin:']) {
        expect(block, property).not.toContain(property)
      }
    }
  })
})
