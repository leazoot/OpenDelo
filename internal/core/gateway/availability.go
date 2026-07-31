package gateway

import (
	"sync/atomic"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 网关可用性（REQ-GATEWAY-003）。
 *
 * PRD §23.1 要求离线时做四件事，前三件在这里落地：
 * 停止接受新的受保护请求、让已运行的 Agent 收到明确错误、**不回退到直连**。
 * 第四件（Console 显示本地数据仍安全）在界面侧。
 *
 * 「不回退到直连」不是这里写一句 return 就成立的，它成立于三件事的合力：
 *   1. Agent 的出站被 HTTP_PROXY 指向 8788（`internal/cli/environ.go`）；
 *   2. 网关停止后 8788 上没有人应答，客户端得到的是连接失败而不是直连成功；
 *   3. 进程还活着但已经停止服务的那段时间里，本类型让每个请求都拿到拒绝。
 * 第三件是唯一能在代码里出错的，因此是本文件的全部内容。
 *
 * 状态**单向**：Stop 之后不会再回到服务中。REQ-GATEWAY-003 AC3 要求
 * 「恢复后之前失败的请求不自动重放」—— 一个能来回切换的开关会诱使人写出
 * 「等它回来再发一次」的重试，而那正是 AC3 要禁止的东西。恢复 = 重启进程。
 */

// Availability 是网关此刻是否接受受保护请求。
//
// 零值是**不服务**：一个忘了调用 Serve 的装配路径会拒绝一切请求，
// 而不是在没准备好的时候放行（Fail Closed）。
type Availability struct {
	serving atomic.Bool
	stopped atomic.Bool
}

// New 构造一个尚未开始服务的 Availability。
func New() *Availability { return &Availability{} }

// Serve 标记网关开始接受请求。已经停止过的网关不会因此重新开始。
//
// 返回是否真的进入了服务状态：调用方据此判断自己是不是在一个已经停掉的
// 进程里做装配，那种情况下继续监听端口没有意义。
func (a *Availability) Serve() bool {
	if a.stopped.Load() {
		return false
	}
	a.serving.Store(true)
	return true
}

// Stop 停止接受新的受保护请求。可以重复调用。
//
// 只改状态，不去中断正在进行中的请求：一次已经发到外部服务的调用，
// 中途掐断只会让本地账本与外部真实状态不一致，而账本是这个产品的底座。
func (a *Availability) Stop() {
	a.stopped.Store(true)
	a.serving.Store(false)
}

func (a *Availability) Serving() bool { return a.serving.Load() }

// Check 是接入面在每个受保护请求最前面调用的那一步。
//
// 不服务时返回 gateway_unavailable。错误里只有码表里的固定文本，
// 不含端口、路径或任何「稍后重试」的指引 —— Agent 拿到重试指引就会重试，
// 而 REQ-GATEWAY-003 AC3 要求恢复后不重放。
func (a *Availability) Check() error {
	if a.Serving() {
		return nil
	}
	return apperr.New(apperr.CodeGatewayUnavailable).
		WithDetail("网关未在服务状态，已拒绝该请求")
}

// Blocker 在网关离线时给出对应的 Fail Closed 阻断，供决策链路记账
// （Fail Closed 的第八条）。
//
// 与 Check 是同一个判断的两种用法：接入面用 Check 直接回绝，
// 已经进入决策链路的请求用 Blocker 让账本记下真正的起因。
func (a *Availability) Blocker() (decision.Blocker, bool) {
	if a.Serving() {
		return "", false
	}
	return decision.BlockerGatewayOffline, true
}
