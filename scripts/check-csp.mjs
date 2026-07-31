#!/usr/bin/env node
/*
 * 校验 web/dist 能在 Gateway 下发的 CSP 之下正常工作。
 *
 * 策略是 `default-src 'self'; …; script-src 'self'; font-src 'self'`
 * （Go 侧常量见 internal/transport/httpapi/headers.go）。
 * 它没有 'unsafe-inline'，因此产物里任何内联脚本、内联样式、内联事件处理器都会被
 * 浏览器拒绝执行；font-src 'self' 同样不接受 data: 形式的字体。
 *
 * 这类问题在构建产物里才看得见（内联是打包器的决定），而且表现是运行时白屏，
 * 类型检查与单元测试都发现不了，所以在构建之后单独扫一遍。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { argv, exit, stdout } from 'node:process'

const ROOT = 'web/embedded/dist'

const HTML_PATTERNS = [
  { name: '内联 <script>（无 src）', regex: /<script(?![^>]*\bsrc=)[^>]*>/gi },
  { name: '内联 <style>', regex: /<style[^>]*>/gi },
  { name: '内联事件处理器', regex: /\son[a-z]+\s*=\s*["']/gi },
  { name: 'javascript: 链接', regex: /["']javascript:/gi },
  { name: '内联 style 属性', regex: /\sstyle\s*=\s*["']/gi },
]

const CSS_PATTERNS = [
  // 字体只能来自 'self'。data: URI 的字体会被 font-src 'self' 拦掉。
  { name: 'data: URI 字体', regex: /url\(\s*["']?data:(?:font|application\/font)[^)]*\)/gi },
]

const EXTERNAL_HOST = { name: '外部主机资源', regex: /\b(?:src|href)\s*=\s*["']https?:\/\//gi }

function* walk(dir) {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* walk(path)
    } else {
      yield path
    }
  }
}

const violations = []
let htmlFiles = 0
let cssFiles = 0

for (const path of walk(ROOT)) {
  const extension = extname(path)
  const patterns = extension === '.html' ? [...HTML_PATTERNS, EXTERNAL_HOST] : extension === '.css' ? CSS_PATTERNS : []
  if (patterns.length === 0) {
    continue
  }
  if (extension === '.html') {
    htmlFiles += 1
  } else {
    cssFiles += 1
  }

  const normalized = relative('.', path)
  const lines = readFileSync(path, 'utf8').split('\n')
  lines.forEach((line, index) => {
    for (const { name, regex } of patterns) {
      for (const match of line.matchAll(regex)) {
        violations.push({ path: normalized, line: index + 1, name, text: match[0].trim() })
      }
    }
  })
}

if (htmlFiles === 0) {
  stdout.write(`${ROOT} 下没有 HTML 产物，检查等于没做。请先运行 pnpm --dir web build。\n`)
  exit(1)
}

if (violations.length > 0) {
  stdout.write(`发现 ${violations.length} 处会被 CSP 拦截的产物内容：\n\n`)
  for (const v of violations) {
    stdout.write(`  ${v.path}:${v.line}  ${v.name}  ${v.text}\n`)
  }
  stdout.write('\nCSP 放宽策略属于 Decision Required。\n')
  exit(1)
}

if (argv.includes('--verbose')) {
  stdout.write(`CSP 扫描通过：${htmlFiles} 个 HTML、${cssFiles} 个 CSS 产物中无内联脚本、内联样式与外部主机资源。\n`)
}
