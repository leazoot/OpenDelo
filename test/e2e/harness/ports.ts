import net from 'node:net'

/*
 * 空闲端口分配。
 *
 * 每个用例的 Gateway 要占三个端口（8787 / 8788 / 8789 的对应物），假服务还要
 * 再占几个。用固定端口的话两个 worker 并行跑就会互相抢，而症状是「偶尔有一个
 * 用例起不来」—— 那种失败查起来很贵。
 */

/**
 * reservePorts 一次要到 count 个互不相同的空闲端口。
 *
 * 先把全部端口同时占住再一起释放：逐个「占了就放」会让第二次分配有机会拿到
 * 第一次刚放掉的那个端口。释放与真正启动之间仍有一个窗口，那是系统分配端口
 * 无法消除的部分。
 */
export async function reservePorts(count: number): Promise<number[]> {
  const holding: net.Server[] = []
  try {
    const ports: number[] = []
    for (let taken = 0; taken < count; taken++) {
      const server = net.createServer()
      holding.push(server)
      ports.push(await listenOnAnyPort(server))
    }
    return ports
  } finally {
    await Promise.all(holding.map(close))
  }
}

function listenOnAnyPort(server: net.Server): Promise<number> {
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (address === null || typeof address === 'string') {
        reject(new Error('监听地址不是 TCP 地址，取不到端口'))
        return
      }
      resolve(address.port)
    })
  })
}

function close(server: net.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((err) => {
      if (err) {
        reject(err)
        return
      }
      resolve()
    })
  })
}
