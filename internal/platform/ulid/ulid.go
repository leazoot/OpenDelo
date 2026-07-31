package ulid

import (
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	oklog "github.com/oklog/ulid/v2"

	"github.com/Runcoor/opendelo/internal/platform/clock"
)

// Generator 生成 ULID。
//
// ULID 而不是 UUID：ID 按时间有序，SQLite 主键索引的写入局部性更好，游标分页可以
// 直接按主键排序。
//
// 并发安全。
type Generator struct {
	clock clock.Clock

	mu      sync.Mutex
	entropy io.Reader
}

// New 构造一个生成器。熵源为 crypto/rand，并按 ULID 的单调模式递增，
// 使同一毫秒内生成的 ID 也严格有序。
func New(source clock.Clock) *Generator {
	return &Generator{
		clock:   source,
		entropy: oklog.Monotonic(rand.Reader, 0),
	}
}

// NewID 生成一个 ULID。
//
// 熵源失败或同一毫秒内单调递增溢出时返回错误，调用方据此让请求失败 ——
// 拿不到 ID 意味着这次操作无法被审计追溯，不能继续（ADR-004）。
func (g *Generator) NewID() (string, error) {
	timestamp := oklog.Timestamp(g.clock.Now())

	g.mu.Lock()
	defer g.mu.Unlock()

	id, err := oklog.New(timestamp, g.entropy)
	if err != nil {
		return "", fmt.Errorf("生成 ULID 失败: %w", err)
	}
	return id.String(), nil
}
