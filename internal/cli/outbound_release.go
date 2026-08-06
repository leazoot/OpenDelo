//go:build !e2e

package cli

/*
 * 出站地址在正式构建里是编译期常量。
 *
 * 这个文件是那句话的实现：`outboundBaseURLs` 恒为空，因此
 * `assembleAdapters` 拿到的每个 Adapter 都用自己声明的 Base URL。
 * 分发出去的二进制里**没有任何运行期输入能改写它** —— 环境变量、配置文件、
 * 命令行参数都不行。
 *
 * 之所以要说这句话：出站地址与凭据注入是同一条路径上的两端。一个能被改写的
 * 出站地址等于「把 GitHub 令牌发给任意主机」，那是 `.claude/rules/security.md`
 * §10 要挡的东西，不该为了测试方便留在正式构建里。
 *
 * E2E 需要指向本地假服务，走的是 `-tags e2e` 的另一份实现（outbound_e2e.go）。
 * 两份实现互斥，正式构建里那份代码根本不参与编译。
 */

// outboundBaseURLs 返回按服务名覆盖出站地址的表。
//
// 正式构建恒为 nil：取不到的键得到空字符串，各 Adapter 因此回落到自己的
// DefaultBaseURL。
func outboundBaseURLs() map[string]string { return nil }
