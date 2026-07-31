/**
 * 拼接 CSS Modules 类名，忽略假值。
 *
 * `tsconfig` 开了 `noUncheckedIndexedAccess`，CSS Modules 的类名索引结果是
 * `string | undefined`，直接插值会得到 "undefined"。这里统一过滤掉。
 */
export function cx(...names: (string | false | undefined)[]): string {
  return names.filter((name): name is string => Boolean(name)).join(' ')
}
