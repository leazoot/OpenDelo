#!/usr/bin/env node
/*
 * 校验 Console 的首屏包体（REQ-NFR-001 第六项：gzip 后 initial chunk < 250KB）。
 *
 * 「initial chunk」取的是**首屏渲染之前浏览器必须下载完的那几个文件**：入口
 * 模块、它的静态依赖（Vite 会给这些加 modulepreload）、以及入口样式表。按需
 * 加载的分块不算 —— 它们不挡首屏；字体也不算，它们是 font-display: swap，
 * 缺席时先用回退字形，不阻塞渲染。
 *
 * 量的是 gzip 之后的字节：网关下发时会压缩，源文件大小与用户等待时间无关。
 */

import { gzipSync } from 'node:zlib'
import { readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { argv, exit, stdout } from 'node:process'

const ROOT = 'web/embedded/dist'
const ENTRY = join(ROOT, 'index.html')

/** BUDGET 是 REQ-NFR-001 给出的上限：250KB（gzip 后）。 */
const BUDGET = 250 * 1024

/** 首屏必须先拿到的三类引用。 */
const BLOCKING = [
  { name: '入口模块', regex: /<script[^>]*\btype=["']module["'][^>]*\bsrc=["']([^"']+)["']/gi },
  { name: '静态依赖', regex: /<link[^>]*\brel=["']modulepreload["'][^>]*\bhref=["']([^"']+)["']/gi },
  { name: '样式表', regex: /<link[^>]*\brel=["']stylesheet["'][^>]*\bhref=["']([^"']+)["']/gi },
]

let html
try {
  html = readFileSync(ENTRY, 'utf8')
} catch {
  stdout.write(`找不到 ${ENTRY}，检查等于没做。请先运行 pnpm --dir web build。\n`)
  exit(1)
}

// <noscript> 里的东西只在脚本被关掉时才会被取，那时首屏根本不是这个页面。
// 把它算进预算等于让「跑不动时的提示」去挤跑得动时的额度。
const blocking = html.replace(/<noscript[\s\S]*?<\/noscript>/gi, '')

const assets = []
for (const { name, regex } of BLOCKING) {
  for (const [, reference] of blocking.matchAll(regex)) {
    const path = join(ROOT, reference.replace(/^\//, ''))
    let raw
    try {
      raw = readFileSync(path)
    } catch {
      stdout.write(`index.html 引用了 ${reference}，但 ${path} 不存在。\n`)
      exit(1)
    }
    assets.push({ name, reference, raw: statSync(path).size, gzipped: gzipSync(raw, { level: 9 }).length })
  }
}

if (assets.length === 0) {
  stdout.write(`${ENTRY} 里没有任何阻塞首屏的引用，检查等于没做。\n`)
  exit(1)
}

const total = assets.reduce((sum, asset) => sum + asset.gzipped, 0)
const kilobytes = (bytes) => `${(bytes / 1024).toFixed(1)}KB`

if (total >= BUDGET) {
  stdout.write(`首屏包体 gzip 后 ${kilobytes(total)}，预算是 ${kilobytes(BUDGET)}：\n\n`)
  for (const asset of assets) {
    stdout.write(`  ${asset.name.padEnd(6)} ${asset.reference}  ${kilobytes(asset.gzipped)}\n`)
  }
  stdout.write('\n超标视为任务未完成（REQ-NFR-001）。\n')
  exit(1)
}

if (argv.includes('--verbose')) {
  stdout.write(`首屏包体 gzip 后 ${kilobytes(total)} / ${kilobytes(BUDGET)}：\n`)
  for (const asset of assets) {
    stdout.write(`  ${asset.name.padEnd(6)} ${asset.reference}  ${kilobytes(asset.gzipped)}（原始 ${kilobytes(asset.raw)}）\n`)
  }
}
