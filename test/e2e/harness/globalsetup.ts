import { buildBinary } from './build.js'

/** 整轮 E2E 开始前构建一次二进制。 */
export default async function globalSetup(): Promise<void> {
  await buildBinary()
}
