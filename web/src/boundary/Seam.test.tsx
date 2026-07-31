import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { Seam } from './Seam'

/*
 * 缝居中是本产品的空间常量（REQ-UI-002）。
 *
 * jsdom 不做布局，getBoundingClientRect 恒为 0，无法直接测像素。这里改为从 CSS
 * 声明里解析出定位参数再推算缝心。已验证的变异：left 50%→40%、margin 除数 -2→-1、
 * 给缝加断点分支，三者都会让用例失败。
 * 真实浏览器下的像素级断言由 S7 的 Playwright 视觉回归覆盖。
 */

// 直接读源文件而不是 import：`?raw` 会被 CSS Modules 转换抢先，`?inline` 拿到的是
// 哈希后的类名。用例要守的正是源码里的声明本身。vitest 的 root 是 web/。
const readCss = (relativePath: string) => readFileSync(resolve(process.cwd(), relativePath), 'utf8')

const seamCss = readCss('src/boundary/Seam.module.css')
const tokensCss = readCss('src/styles/tokens.css')

/** 取出某个选择器的声明块。 */
function ruleBlock(css: string, selector: string): string {
  const match = new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`).exec(css)
  if (!match?.[1]) {
    throw new Error(`未找到规则 ${selector}`)
  }
  return match[1]
}

/** 读取 tokens.css 中以 px 为单位的令牌值。 */
function pxToken(name: string): number {
  const match = new RegExp(`${name}:\\s*([\\d.]+)px`).exec(tokensCss)
  if (!match?.[1]) {
    throw new Error(`未找到令牌 ${name}`)
  }
  return Number(match[1])
}

/** 按声明推算某一图层的中心横坐标；left 为百分比，margin-left 为 calc(宽度 / 除数)。 */
function layerCenterAt(selector: string, widthToken: string, containerWidth: number): number {
  const block = ruleBlock(seamCss, selector)

  const leftMatch = /left:\s*([\d.]+)%/.exec(block)
  const divisorMatch = new RegExp(
    `margin-left:\\s*calc\\(var\\(${widthToken}\\)\\s*/\\s*(-?[\\d.]+)\\)`,
  ).exec(block)
  if (!leftMatch?.[1] || !divisorMatch?.[1]) {
    throw new Error(`${selector} 的定位声明不是预期形式`)
  }

  const layerWidth = pxToken(widthToken)
  const left = (containerWidth * Number(leftMatch[1])) / 100 + layerWidth / Number(divisorMatch[1])
  return left + layerWidth / 2
}

describe('Seam', () => {
  it.each([1440, 1280, 1024])('缝心在 %i px 宽度下与容器中心重合', (containerWidth) => {
    expect(layerCenterAt('.seam', '--seam-width', containerWidth)).toBe(containerWidth / 2)
  })

  it.each([1440, 1280, 1024])('辉光带在 %i px 宽度下与缝共用同一条中心线', (containerWidth) => {
    expect(layerCenterAt('.glow', '--seam-glow-width', containerWidth)).toBe(
      layerCenterAt('.seam', '--seam-width', containerWidth),
    )
  })

  it('缝宽与刻度密度不带任何断点分支', () => {
    expect(seamCss).not.toMatch(/@media/)
  })

  it('渲染出辉光带与缝体，两者都对辅助技术隐藏', () => {
    const { container } = render(<Seam />)

    // 缝是纯装饰性的空间标记，没有可读内容，不应进入无障碍树。
    const topLayers = container.querySelectorAll(':scope > div')
    expect(topLayers).toHaveLength(2)
    for (const layer of topLayers) {
      expect(layer).toHaveAttribute('aria-hidden', 'true')
    }

    // 辉光带 + 缝体 + 填充 + 内侧暗线 + 刻度带
    expect(container.querySelectorAll('div')).toHaveLength(5)
  })
})

describe('Seam 的离线态', () => {
  it('离线样式只改颜色与动画，不出现任何定位声明', () => {
    // 缝是空间常量，离线不是让它挪动的理由。
    for (const selector of ['.fillOff', '.glowOff']) {
      const block = ruleBlock(seamCss, selector)
      for (const property of ['left', 'right', 'width', 'margin', 'top', 'bottom', 'inset', 'translate']) {
        expect(block).not.toContain(`${property}:`)
      }
    }
  })

  it('离线态用 --seam-off 与 offPulse', () => {
    expect(ruleBlock(seamCss, '.fillOff')).toContain('var(--seam-off)')
    expect(ruleBlock(seamCss, '.glowOff')).toContain('offPulse')
    expect(seamCss).toContain('@keyframes offPulse')
  })

  it('offPulse 只动 opacity，不触发布局（REQ-UI-010 AC3）', () => {
    const frames = /@keyframes offPulse\s*\{([\s\S]*?)\n\}/.exec(seamCss)?.[1] ?? ''
    expect(frames).toContain('opacity')
    for (const property of ['width', 'height', 'left', 'top', 'margin', 'padding']) {
      expect(frames).not.toContain(`${property}:`)
    }
  })

  it('离线与在线渲染出同样的图层结构', () => {
    // 图层数一变就说明几何被改动过了。
    const online = render(<Seam />).container.querySelectorAll('div').length
    const offline = render(<Seam offline />).container.querySelectorAll('div').length

    expect(offline).toBe(online)
  })

  it('只有离线时才挂上离线样式', () => {
    const { container: online } = render(<Seam />)
    const { container: offline } = render(<Seam offline />)

    expect(online.innerHTML).not.toContain('fillOff')
    expect(offline.innerHTML).toContain('fillOff')
    expect(offline.innerHTML).toContain('glowOff')
  })
})
