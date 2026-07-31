package localvault

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/crypto"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

// FileName 是保险库在数据目录下的文件名。
const FileName = "vault.enc"

// FilePermission 是保险库文件必须具备的权限。
const FilePermission fs.FileMode = 0o600

// DefaultIdleTimeout 是自动锁定的默认闲置时长（REQ-CRED-004 §3 `[假设]`）。
const DefaultIdleTimeout = 15 * time.Minute

// MaxUnlockFailures 是触发锁定所需的连续失败次数（REQ-APPROVAL-005 AC2）。
const MaxUnlockFailures = 3

// LockoutDuration 是锁定时长（REQ-APPROVAL-005 AC2）。
const LockoutDuration = 60 * time.Second

// UnlockOutcome 报告一次解锁尝试之后保险库的处境。
//
// 单独返回而不是让调用方去查状态：账本上那条锁定记录只该写一次，
// 而「这一次失败是不是刚好触发了锁定」只有解锁那一刻的持锁者知道。
type UnlockOutcome struct {
	// LockoutBegan 为真表示**这一次失败**触发了锁定，调用方应记一条审计。
	LockoutBegan bool
	// LockedUntil 是锁定的结束时刻；未锁定时为零值。
	LockedUntil time.Time
}

// Options 是保险库的构造参数。
type Options struct {
	// Path 是保险库文件路径。
	Path string
	// Clock 提供当前时间。自动锁定是一条安全边界，用真实时钟测试只能靠 sleep
	Clock clock.Clock
	// IdleTimeout 为零时取 DefaultIdleTimeout。
	IdleTimeout time.Duration
}

// Vault 是本地加密保险库，本产品唯一存放凭据密文的地方（REQ-CRED-001）。
//
// 解锁后**不缓存任何条目明文**：
// 内存里只留主密码，每次取用都重新解密文件、取出那一个字段、清掉其余部分。
// 代价是每次取用一次 Argon2id（约 30ms），换来的是「进程被 dump 也只泄漏
// 一份主密码而不是整库凭据」——而主密码本来就是解锁期间必须在内存里的东西。
type Vault struct {
	path        string
	clock       clock.Clock
	idleTimeout time.Duration

	mutex sync.Mutex
	// master 非空即表示已解锁。锁定时清零。
	master     secret.Value
	lastActive time.Time

	// failures 是连续失败次数，成功解锁与锁定开始时都归零。
	failures int
	// lockedUntil 非零表示在此之前不再接受任何主密码。
	lockedUntil time.Time
}

// New 构造保险库。它不读文件，也不判断保险库是否存在 ——
// 那个判断只应发生在解锁时，且不对外区分（AC1）。
func New(options Options) (*Vault, error) {
	if options.Path == "" {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).WithDetail("保险库路径不能为空")
	}
	if options.Clock == nil {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).WithDetail("保险库需要一个时钟")
	}

	idleTimeout := options.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &Vault{path: options.Path, clock: options.Clock, idleTimeout: idleTimeout}, nil
}

// Create 建立一个空的保险库。
//
// 已存在时拒绝：覆盖会把原有凭据全部丢掉，而那是不可逆的。
func (v *Vault) Create(master secret.Value) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if _, err := os.Stat(v.path); err == nil {
		return apperr.New(apperr.CodeConflict).WithDetail("保险库已存在")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("检查保险库文件失败")
	}

	if err := v.writeEntries(master, map[string][]byte{}); err != nil {
		return err
	}
	v.unlockLocked(master)
	return nil
}

// Unlock 用主密码解锁。
//
// **主密码错误与保险库不存在返回同一个结果**（REQ-CRED-004 AC1）：
// 区分开来等于给攻击者一个「这台机器上有没有保险库」的探测接口。
//
// 连续失败 MaxUnlockFailures 次后进入 LockoutDuration 的锁定，期间**不再校验
// 任何主密码**（REQ-APPROVAL-005 AC2）—— 锁定期内仍然逐次校验的话，
// 攻击者只是被拖慢，而不是被挡住。
func (v *Vault) Unlock(master secret.Value) (UnlockOutcome, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	now := v.clock.Now()
	if now.Before(v.lockedUntil) {
		return UnlockOutcome{LockedUntil: v.lockedUntil}, lockedOut()
	}

	if _, err := v.readEntries(master); err != nil {
		v.lockLocked()
		v.failures++
		if v.failures < MaxUnlockFailures {
			return UnlockOutcome{}, unlockFailed()
		}
		// 计数归零：锁定结束后重新从零开始数，而不是让第四次失败又立刻锁一次。
		v.failures = 0
		v.lockedUntil = now.Add(LockoutDuration)
		return UnlockOutcome{LockoutBegan: true, LockedUntil: v.lockedUntil}, lockedOut()
	}

	v.failures = 0
	v.lockedUntil = time.Time{}
	v.unlockLocked(master)
	return UnlockOutcome{}, nil
}

// unlockFailed 是解锁路径上唯一的失败结果，不携带任何可区分的信息。
func unlockFailed() error {
	return apperr.New(apperr.CodeUnauthenticated).WithDetail("解锁失败")
}

// lockedOut 是锁定期内的答复。
//
// 与 unlockFailed 分开：这条信息不泄漏保险库存不存在，却是用户必须知道的
// —— 不说就变成「密码明明对却一直失败」。
func lockedOut() error {
	return apperr.New(apperr.CodeProviderLockedTimeout).
		WithDetail("连续失败三次，一分钟内不再接受主密码")
}

// CreateWith 用明文口令的字节建立一个空保险库。
//
// 与 UnlockWith 同理：接入面与用例都不该自己碰 secret.Value（ADR-002）。
// **调用方在这之后不得再使用 master**：装箱是接管而不是拷贝。
func (v *Vault) CreateWith(master []byte) error {
	value := secret.New(master)
	defer value.Zero()
	return v.Create(value)
}

// UnlockWith 用明文口令的字节解锁。
//
// 接入面拿不到也不该拿到 secret.Value —— 那个类型只在 credential 与 adapter
// 两个包的签名里可见（ADR-002，由 test/arch 强制）。所以由本包接过字节、
// 就地装箱、用完清零，调用方不必自己碰那个类型。
//
// **调用方在这之后不得再使用 master**：装箱是接管而不是拷贝，
// 返回时底层字节已被清零。
func (v *Vault) UnlockWith(master []byte) (UnlockOutcome, error) {
	value := secret.New(master)
	defer value.Zero()
	return v.Unlock(value)
}

// Lock 立即锁定并清零内存中的主密码（REQ-CRED-004 §4）。
func (v *Vault) Lock() {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	v.lockLocked()
}

// IsUnlocked 报告当前是否处于解锁状态，顺带执行一次自动锁定检查。
func (v *Vault) IsUnlocked() bool {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	return v.activeLocked() == nil
}

// Get 取出一个条目。
//
// 锁定状态下返回 vault_locked（REQ-CRED-004 AC2）；调用方据此进入等待，
// 超时后拒绝，而不是把请求放行。
func (v *Vault) Get(reference string) (secret.Value, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if err := v.activeLocked(); err != nil {
		return secret.Value{}, err
	}

	entries, err := v.readEntries(v.master)
	if err != nil {
		return secret.Value{}, err
	}
	defer zeroEntries(entries)

	stored, present := entries[reference]
	if !present {
		return secret.Value{}, apperr.New(apperr.CodeNotFound).
			WithDetail("保险库中没有条目 " + reference)
	}

	// 复制一份再交出去：紧接着的 zeroEntries 会清掉底层数组。
	value := secret.New(append([]byte(nil), stored...))
	v.touchLocked()
	return value, nil
}

// Put 写入或覆盖一个条目。
func (v *Vault) Put(reference string, value secret.Value) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if reference == "" {
		return apperr.New(apperr.CodeInvalidRequest).WithDetail("条目名不能为空")
	}
	if err := v.activeLocked(); err != nil {
		return err
	}

	entries, err := v.readEntries(v.master)
	if err != nil {
		return err
	}
	defer zeroEntries(entries)

	entries[reference] = append([]byte(nil), value.Reveal()...)
	if err := v.writeEntries(v.master, entries); err != nil {
		return err
	}
	v.touchLocked()
	return nil
}

// Delete 移除一个条目。条目不存在时报 not_found 而不是当作成功：
// 调用方以为删掉了、实际没删，那份凭据下一次还会被取出来。
func (v *Vault) Delete(reference string) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if err := v.activeLocked(); err != nil {
		return err
	}

	entries, err := v.readEntries(v.master)
	if err != nil {
		return err
	}
	defer zeroEntries(entries)

	if _, present := entries[reference]; !present {
		return apperr.New(apperr.CodeNotFound).WithDetail("保险库中没有条目 " + reference)
	}
	delete(entries, reference)

	if err := v.writeEntries(v.master, entries); err != nil {
		return err
	}
	v.touchLocked()
	return nil
}

// References 列出条目名。条目名不是凭据，可以对外展示。
func (v *Vault) References() ([]string, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if err := v.activeLocked(); err != nil {
		return nil, err
	}

	entries, err := v.readEntries(v.master)
	if err != nil {
		return nil, err
	}
	defer zeroEntries(entries)

	references := make([]string, 0, len(entries))
	for reference := range entries {
		references = append(references, reference)
	}
	sort.Strings(references)
	v.touchLocked()
	return references, nil
}

// ExportBackup 导出加密备份（REQ-CRED-004 §5）。
//
// 返回的就是磁盘上那份密文本身，没有主密码解不开（AC3）。
// 导出**不要求解锁**：备份的意义之一正是在忘记主密码之前先存一份。
func (v *Vault) ExportBackup() ([]byte, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	sealed, err := os.ReadFile(v.path)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeNotFound, err).WithDetail("读取保险库失败")
	}
	// 备份必须是本程序认得的信封，否则恢复时才发现就晚了。
	if _, err := crypto.ParseHeader(sealed); err != nil {
		return nil, err
	}
	return sealed, nil
}

// RestoreBackup 用备份覆盖当前保险库，覆盖前先验证主密码解得开它。
//
// 验证是必须的：把一份解不开的备份写进去，等于用一次「恢复」抹掉了现有凭据。
func (v *Vault) RestoreBackup(backup []byte, master secret.Value) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	plaintext, err := crypto.Open(master.Reveal(), backup)
	if err != nil {
		v.lockLocked()
		return unlockFailed()
	}
	crypto.Zero(plaintext)

	if err := writeSealed(v.path, backup); err != nil {
		return err
	}
	v.unlockLocked(master)
	return nil
}

// activeLocked 检查解锁状态并执行自动锁定。调用前必须持有 mutex。
//
// 自动锁定在每次操作前判定，而不是靠后台定时器：定时器可能因为进程休眠、
// 时钟跳变而不触发，而「这次取用发生时是否已经闲置超时」才是真正要回答的问题。
func (v *Vault) activeLocked() error {
	if v.master.IsEmpty() {
		return locked()
	}
	if !v.lastActive.IsZero() && v.clock.Now().Sub(v.lastActive) >= v.idleTimeout {
		v.lockLocked()
		return locked()
	}
	return nil
}

func locked() error {
	return apperr.New(apperr.CodeVaultLocked)
}

func (v *Vault) unlockLocked(master secret.Value) {
	v.master = secret.New(append([]byte(nil), master.Reveal()...))
	v.lastActive = v.clock.Now()
}

func (v *Vault) lockLocked() {
	v.master.Zero()
	v.master = secret.Value{}
	v.lastActive = time.Time{}
}

func (v *Vault) touchLocked() {
	v.lastActive = v.clock.Now()
}

// readEntries 读文件并解密。文件不存在、格式不对、口令不对，
// 在 Unlock 那一层会被折叠成同一个错误。
func (v *Vault) readEntries(master secret.Value) (map[string][]byte, error) {
	sealed, err := os.ReadFile(v.path)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeNotFound, err).WithDetail("读取保险库失败")
	}

	plaintext, err := crypto.Open(master.Reveal(), sealed)
	if err != nil {
		return nil, err
	}
	defer crypto.Zero(plaintext)

	entries := map[string][]byte{}
	if err := json.Unmarshal(plaintext, &entries); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).WithDetail("保险库内容不是合法的 JSON")
	}
	return entries, nil
}

func (v *Vault) writeEntries(master secret.Value, entries map[string][]byte) error {
	plaintext, err := json.Marshal(entries)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("序列化保险库内容失败")
	}
	defer crypto.Zero(plaintext)

	sealed, err := crypto.Seal(master.Reveal(), plaintext, crypto.Default)
	if err != nil {
		return err
	}
	return writeSealed(v.path, sealed)
}

// writeSealed 原子地写入密文：先写临时文件再改名。
// 直接覆盖时若中途失败，保险库会停在一个半截的状态，那等于凭据全丢。
func writeSealed(path string, sealed []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("创建保险库目录失败")
	}

	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, sealed, FilePermission); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("写入保险库失败")
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("替换保险库文件失败")
	}
	return nil
}

// zeroEntries 清零一份解出来的条目表。每次取用后立即执行，
// 明文在内存里存在的时间只覆盖这一次调用。
func zeroEntries(entries map[string][]byte) {
	for _, value := range entries {
		crypto.Zero(value)
	}
}
