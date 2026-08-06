/*
 * 认不出 ESM 的浏览器走这一条（REQ-NFR-003 AC2）。
 *
 * 只有 nomodule 的浏览器会执行本文件，因此这里写的是它们看得懂的语法：
 * 没有箭头函数、没有 const、没有模板串。用一个类名而不是直接改 style，
 * 是为了让「显示与否」只有 reset.css 一个来源。
 */
document.documentElement.className += ' no-modules'
