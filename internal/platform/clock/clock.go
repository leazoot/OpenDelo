package clock

import (
	"sync"
	"time"
)

// Clock 是时间的唯一来源。
//
// 业务代码一律通过它取时间，不直接调用 time.Now：Lease 过期、审批超时、Trust Memory
// 失效都是本产品的安全边界，用真实时钟测试只能靠 sleep，既慢又不可靠。
// 这条约定在 `internal/core` 由 test/arch 的 AST 扫描强制。
type Clock interface {
	// Now 返回当前时间。实现必须返回 UTC，与数据库中一律 UTC 的约定一致。
	Now() time.Time
}

// System 是走真实时间的实现，进程运行时使用。
type System struct{}

// Now 返回当前 UTC 时间。这是全仓库唯一允许调用 time.Now 的地方。
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed 是可精确控制的实现，只在测试中使用。
//
// 并发安全：用例可以在多个 goroutine 里读时间的同时推进它，-race 下不报警。
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed 构造一个停在 start 的时钟。start 会被转成 UTC。
func NewFixed(start time.Time) *Fixed {
	return &Fixed{now: start.UTC()}
}

// Now 返回当前停留的时间点。
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance 把时间向前推进。负的 elapsed 会让时间倒退，这在测试「时钟回拨」时是需要的。
func (f *Fixed) Advance(elapsed time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(elapsed)
}

// Set 把时间设到指定时刻。
func (f *Fixed) Set(instant time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = instant.UTC()
}

var (
	_ Clock = System{}
	_ Clock = (*Fixed)(nil)
)
