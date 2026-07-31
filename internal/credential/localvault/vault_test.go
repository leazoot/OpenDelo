package localvault_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/localvault"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

var openedAt = time.Date(2026, time.July, 28, 9, 15, 30, 123_000_000, time.UTC)

const (
	masterPassword = "correct horse battery staple"
	otherPassword  = "wrong horse battery staple"
	entryReference = "github/work/token"
	entrySecret    = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"
)

type fixture struct {
	vault *localvault.Vault
	path  string
	clock *clock.Fixed
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), localvault.FileName)
	fixedClock := clock.NewFixed(openedAt)
	vault, err := localvault.New(localvault.Options{Path: path, Clock: fixedClock})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}
	return fixture{vault: vault, path: path, clock: fixedClock}
}

// created 返回一个已创建并解锁的保险库。
func created(t *testing.T) fixture {
	t.Helper()

	f := newFixture(t)
	if err := f.vault.Create(secret.NewString(masterPassword)); err != nil {
		t.Fatalf("创建保险库失败：%v", err)
	}
	return f
}

func assertCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望错误码 %s，但没有出错", want)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) {
		t.Fatalf("错误不是 *apperr.Error：%v", err)
	}
	if appError.Code() != want {
		t.Errorf("错误码是 %s，期望 %s（%v）", appError.Code(), want, err)
	}
}

func TestVault_CreatePutGet_RoundTrips(t *testing.T) {
	f := created(t)

	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	value, err := f.vault.Get(entryReference)
	if err != nil {
		t.Fatalf("取出条目失败：%v", err)
	}
	if string(value.Reveal()) != entrySecret {
		t.Error("取出的内容与写入的不一致")
	}

	references, err := f.vault.References()
	if err != nil {
		t.Fatalf("列出条目失败：%v", err)
	}
	if len(references) != 1 || references[0] != entryReference {
		t.Errorf("条目清单是 %v", references)
	}
}

func TestVault_FileNeverContainsPlaintext(t *testing.T) {
	// REQ-CRED-001：保险库是唯一存密文的地方，而它存的必须是密文。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	content, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("读取保险库文件失败：%v", err)
	}
	if bytes.Contains(content, []byte(entrySecret)) {
		t.Error("保险库文件里出现了条目明文")
	}
	if bytes.Contains(content, []byte(masterPassword)) {
		t.Error("保险库文件里出现了主密码")
	}
	// 条目名也不该以明文出现 —— 它虽然不是凭据，但会泄漏「这台机器连了什么」。
	if bytes.Contains(content, []byte(entryReference)) {
		t.Error("保险库文件里出现了条目名明文")
	}
}

func TestVault_FilePermissionIsOwnerOnly(t *testing.T) {
	// 数字写死成 0600，不引用 localvault.FilePermission ——
	// 拿被测常量跟自己比是同义反复，把它改成 0644 用例照样通过。
	const ownerOnly fs.FileMode = 0o600

	if localvault.FilePermission != ownerOnly {
		t.Errorf("声明的文件权限是 %o，期望 %o",
			localvault.FilePermission, ownerOnly)
	}

	f := created(t)
	info, err := os.Stat(f.path)
	if err != nil {
		t.Fatalf("读取文件信息失败：%v", err)
	}
	if permission := info.Mode().Perm(); permission != ownerOnly {
		t.Errorf("保险库文件权限是 %o，期望 %o", permission, ownerOnly)
	}
}

func TestVault_DefaultIdleTimeout_IsFifteenMinutes(t *testing.T) {
	// 同样写死：REQ-CRED-004 §3 点名的是 15 分钟。
	// 上面那条自动锁定用例用 DefaultIdleTimeout 推进时钟，改大改小它都通过。
	if localvault.DefaultIdleTimeout != 15*time.Minute {
		t.Errorf("默认闲置时长是 %s，期望 15 分钟", localvault.DefaultIdleTimeout)
	}
}

func TestVault_WrongPasswordAndMissingVault_FailTheSameWay(t *testing.T) {
	// REQ-CRED-004 AC1：解锁失败不区分「密码错误」与「Vault 不存在」。
	// 区分开来等于给攻击者一个「这台机器上有没有保险库」的探测接口。
	missing := newFixture(t)
	_, missingErr := missing.vault.Unlock(secret.NewString(masterPassword))
	assertCode(t, missingErr, apperr.CodeUnauthenticated)

	existing := created(t)
	existing.vault.Lock()
	_, wrongErr := existing.vault.Unlock(secret.NewString(otherPassword))
	assertCode(t, wrongErr, apperr.CodeUnauthenticated)

	if missingErr.Error() != wrongErr.Error() {
		t.Errorf("两种失败的信息不同，可以据此判断保险库是否存在：\n%s\n%s",
			missingErr.Error(), wrongErr.Error())
	}

	// 正向对照：正确的主密码必须能解锁，否则上面两条断言恒成立。
	if _, err := existing.vault.Unlock(secret.NewString(masterPassword)); err != nil {
		t.Fatalf("正确的主密码解锁失败：%v", err)
	}
}

func TestVault_FailedUnlock_LeavesItLocked(t *testing.T) {
	// 解锁失败之后不能停在半解锁状态。
	f := created(t)
	f.vault.Lock()

	if _, err := f.vault.Unlock(secret.NewString(otherPassword)); err == nil {
		t.Fatal("错误的主密码解锁成功了")
	}
	if f.vault.IsUnlocked() {
		t.Error("解锁失败后保险库仍处于解锁状态")
	}
	assertCode(t, mustFailGet(t, f), apperr.CodeVaultLocked)
}

func TestVault_Locked_RefusesEveryOperation(t *testing.T) {
	// REQ-CRED-004 AC2：锁定状态下任何取用返回 vault_locked。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}
	f.vault.Lock()

	_, getErr := f.vault.Get(entryReference)
	assertCode(t, getErr, apperr.CodeVaultLocked)
	assertCode(t, f.vault.Put(entryReference, secret.NewString("x")), apperr.CodeVaultLocked)
	assertCode(t, f.vault.Delete(entryReference), apperr.CodeVaultLocked)

	_, listErr := f.vault.References()
	assertCode(t, listErr, apperr.CodeVaultLocked)

	if f.vault.IsUnlocked() {
		t.Error("锁定后仍报告为解锁状态")
	}
}

func TestVault_AutoLocksAfterIdleTimeout(t *testing.T) {
	// REQ-CRED-004 §3：默认闲置 15 分钟自动锁定。
	// 用注入的时钟推进，不靠 sleep。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	// 差一点点还不该锁。
	f.clock.Advance(localvault.DefaultIdleTimeout - time.Second)
	if _, err := f.vault.Get(entryReference); err != nil {
		t.Fatalf("尚未超时就被锁定了：%v", err)
	}

	// 上一次取用刷新了计时，再推进不足超时仍不锁。
	f.clock.Advance(localvault.DefaultIdleTimeout - time.Second)
	if _, err := f.vault.Get(entryReference); err != nil {
		t.Fatalf("取用没有刷新闲置计时：%v", err)
	}

	// 推进到超时，下一次操作即锁定。
	f.clock.Advance(localvault.DefaultIdleTimeout)
	_, err := f.vault.Get(entryReference)
	assertCode(t, err, apperr.CodeVaultLocked)

	// 锁定是真的发生了，而不只是这一次被拒。
	if f.vault.IsUnlocked() {
		t.Error("超时后仍报告为解锁状态")
	}
}

func TestVault_CustomIdleTimeout_IsHonoured(t *testing.T) {
	path := filepath.Join(t.TempDir(), localvault.FileName)
	fixedClock := clock.NewFixed(openedAt)
	vault, err := localvault.New(localvault.Options{
		Path: path, Clock: fixedClock, IdleTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}
	if err := vault.Create(secret.NewString(masterPassword)); err != nil {
		t.Fatalf("创建保险库失败：%v", err)
	}

	fixedClock.Advance(time.Minute)
	if _, err := vault.References(); err == nil {
		t.Error("自定义的闲置时长没有生效")
	}
}

func TestVault_Create_RefusesToOverwriteAnExistingVault(t *testing.T) {
	// 覆盖会把原有凭据全部丢掉，而那是不可逆的。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	assertCode(t, f.vault.Create(secret.NewString(otherPassword)), apperr.CodeConflict)

	// 原有内容仍在。
	value, err := f.vault.Get(entryReference)
	if err != nil {
		t.Fatalf("原有条目丢了：%v", err)
	}
	if string(value.Reveal()) != entrySecret {
		t.Error("原有条目被改动了")
	}
}

func TestVault_Backup_CannotBeOpenedWithoutTheMasterPassword(t *testing.T) {
	// REQ-CRED-004 AC3。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	backup, err := f.vault.ExportBackup()
	if err != nil {
		t.Fatalf("导出备份失败：%v", err)
	}
	if bytes.Contains(backup, []byte(entrySecret)) {
		t.Error("备份里出现了条目明文")
	}

	restored := newFixture(t)
	assertCode(t, restored.vault.RestoreBackup(backup, secret.NewString(otherPassword)),
		apperr.CodeUnauthenticated)
	if restored.vault.IsUnlocked() {
		t.Error("恢复失败后保险库处于解锁状态")
	}
	if _, statErr := os.Stat(restored.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("恢复失败却写出了保险库文件")
	}

	// 用正确的主密码恢复到另一处，内容一致。
	if restoreErr := restored.vault.RestoreBackup(backup, secret.NewString(masterPassword)); restoreErr != nil {
		t.Fatalf("恢复备份失败：%v", restoreErr)
	}
	value, err := restored.vault.Get(entryReference)
	if err != nil {
		t.Fatalf("恢复后取不出条目：%v", err)
	}
	if string(value.Reveal()) != entrySecret {
		t.Error("恢复后的内容与备份前不一致")
	}
}

func TestVault_RestoreWithWrongPassword_LeavesTheCurrentVaultIntact(t *testing.T) {
	// 把一份解不开的备份写进去，等于用一次「恢复」抹掉现有凭据。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	other := created(t)
	if err := other.vault.Put("other", secret.NewString("another")); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}
	foreign, err := other.vault.ExportBackup()
	if err != nil {
		t.Fatalf("导出备份失败：%v", err)
	}

	assertCode(t, f.vault.RestoreBackup(foreign, secret.NewString(otherPassword)),
		apperr.CodeUnauthenticated)

	// 原保险库没有被动过：用原主密码仍能解锁并取出原条目。
	if _, unlockErr := f.vault.Unlock(secret.NewString(masterPassword)); unlockErr != nil {
		t.Fatalf("原保险库解不开了：%v", unlockErr)
	}
	value, err := f.vault.Get(entryReference)
	if err != nil {
		t.Fatalf("原条目丢了：%v", err)
	}
	if string(value.Reveal()) != entrySecret {
		t.Error("原条目被改动了")
	}
}

func TestVault_ExportBackup_RejectsAFileThatIsNotAnEnvelope(t *testing.T) {
	// 导出一份本程序认不出的东西，等到恢复时才发现就晚了。
	f := newFixture(t)
	if err := os.WriteFile(f.path, []byte("not an envelope"), localvault.FilePermission); err != nil {
		t.Fatalf("构造损坏的保险库失败：%v", err)
	}

	_, err := f.vault.ExportBackup()
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestVault_MissingEntry_ReportsNotFound(t *testing.T) {
	f := created(t)

	_, err := f.vault.Get("nope")
	assertCode(t, err, apperr.CodeNotFound)
	assertCode(t, f.vault.Delete("nope"), apperr.CodeNotFound)
}

func TestVault_Delete_RemovesTheEntry(t *testing.T) {
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	if deleteErr := f.vault.Delete(entryReference); deleteErr != nil {
		t.Fatalf("删除条目失败：%v", deleteErr)
	}
	_, err := f.vault.Get(entryReference)
	assertCode(t, err, apperr.CodeNotFound)

	// 删除是持久的：锁定再解锁后仍然没有。
	f.vault.Lock()
	if _, unlockErr := f.vault.Unlock(secret.NewString(masterPassword)); unlockErr != nil {
		t.Fatalf("解锁失败：%v", unlockErr)
	}
	_, err = f.vault.Get(entryReference)
	assertCode(t, err, apperr.CodeNotFound)
}

func TestVault_Put_RejectsAnEmptyReference(t *testing.T) {
	f := created(t)

	assertCode(t, f.vault.Put("", secret.NewString(entrySecret)), apperr.CodeInvalidRequest)
}

func TestNew_MissingPathOrClock_IsRejected(t *testing.T) {
	if _, err := localvault.New(localvault.Options{Clock: clock.NewFixed(openedAt)}); err == nil {
		t.Error("没有路径的保险库被构造出来了")
	}
	if _, err := localvault.New(localvault.Options{Path: "vault.enc"}); err == nil {
		t.Error("没有时钟的保险库被构造出来了")
	}
}

func TestVault_ReturnedValue_SurvivesTheInternalZeroing(t *testing.T) {
	// 取出的值是一份副本：内部清零不该把调用方手里的东西一起抹掉。
	f := created(t)
	if err := f.vault.Put(entryReference, secret.NewString(entrySecret)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}

	value, err := f.vault.Get(entryReference)
	if err != nil {
		t.Fatalf("取出条目失败：%v", err)
	}

	// 再取一次，触发一轮完整的解密与清零。
	if _, secondErr := f.vault.Get(entryReference); secondErr != nil {
		t.Fatalf("再次取出失败：%v", secondErr)
	}
	if string(value.Reveal()) != entrySecret {
		t.Error("先前取出的值被内部清零抹掉了")
	}
}

func mustFailGet(t *testing.T, f fixture) error {
	t.Helper()

	_, err := f.vault.Get(entryReference)
	return err
}

// ——— 强认证的失败锁定（REQ-APPROVAL-005 AC2） ———

func TestUnlock_ThreeWrongPasswords_LockOutEvenTheCorrectOne(t *testing.T) {
	// 锁定期内不再校验任何主密码：仍然逐次校验的话，攻击者只是被拖慢，
	// 而不是被挡住 —— 正确的口令在锁定期内也解不开，这条用例守的就是这一点。
	f := created(t)
	f.vault.Lock()

	for attempt := 1; attempt <= localvault.MaxUnlockFailures; attempt++ {
		outcome, err := f.vault.Unlock(secret.NewString(otherPassword))
		if err == nil {
			t.Fatalf("第 %d 次错误口令竟然解开了", attempt)
		}
		wantBegan := attempt == localvault.MaxUnlockFailures
		if outcome.LockoutBegan != wantBegan {
			t.Errorf("第 %d 次报告 LockoutBegan=%v，期望 %v", attempt, outcome.LockoutBegan, wantBegan)
		}
	}

	outcome, err := f.vault.Unlock(secret.NewString(masterPassword))
	assertCode(t, err, apperr.CodeProviderLockedTimeout)
	if outcome.LockoutBegan {
		t.Error("锁定期内的尝试又报告了一次「刚刚开始锁定」，账本会重复记录")
	}
	if f.vault.IsUnlocked() {
		t.Error("锁定期内竟然解开了")
	}
}

func TestUnlock_AfterTheLockoutElapses_TheCorrectPasswordWorksAgain(t *testing.T) {
	// 锁定是限时的，不是把人永久挡在外面。
	f := created(t)
	f.vault.Lock()

	for range localvault.MaxUnlockFailures {
		if _, err := f.vault.Unlock(secret.NewString(otherPassword)); err == nil {
			t.Fatal("错误口令竟然解开了")
		}
	}

	f.clock.Advance(localvault.LockoutDuration)
	if _, err := f.vault.Unlock(secret.NewString(masterPassword)); err != nil {
		t.Fatalf("锁定结束后仍然解不开：%v", err)
	}
	if !f.vault.IsUnlocked() {
		t.Error("解锁成功却报告仍然锁着")
	}
}

func TestUnlock_ASuccessfulUnlock_ClearsTheFailureCount(t *testing.T) {
	// 数的是**连续**失败：中间成功过一次就重新从零开始，
	// 否则一天里零星打错三次也会把人锁在外面。
	f := created(t)
	f.vault.Lock()

	for range localvault.MaxUnlockFailures - 1 {
		if _, err := f.vault.Unlock(secret.NewString(otherPassword)); err == nil {
			t.Fatal("错误口令竟然解开了")
		}
	}
	if _, err := f.vault.Unlock(secret.NewString(masterPassword)); err != nil {
		t.Fatalf("解锁失败：%v", err)
	}
	f.vault.Lock()

	for attempt := 1; attempt <= localvault.MaxUnlockFailures-1; attempt++ {
		outcome, err := f.vault.Unlock(secret.NewString(otherPassword))
		assertCode(t, err, apperr.CodeUnauthenticated)
		if outcome.LockoutBegan {
			t.Fatalf("第 %d 次失败就锁定了，计数没有被成功解锁清零", attempt)
		}
	}
}

func TestUnlock_LockoutDoesNotRevealWhetherAVaultExists(t *testing.T) {
	// 锁定信息对「有没有保险库」必须同样沉默（REQ-CRED-004 AC1）。
	missing := newFixture(t)
	existing := created(t)
	existing.vault.Lock()

	for range localvault.MaxUnlockFailures {
		if _, err := missing.vault.Unlock(secret.NewString(masterPassword)); err == nil {
			t.Fatal("不存在的保险库竟然解开了")
		}
		if _, err := existing.vault.Unlock(secret.NewString(otherPassword)); err == nil {
			t.Fatal("错误口令竟然解开了")
		}
	}

	_, missingErr := missing.vault.Unlock(secret.NewString(masterPassword))
	_, existingErr := existing.vault.Unlock(secret.NewString(masterPassword))
	if missingErr.Error() != existingErr.Error() {
		t.Errorf("两条锁定信息不同：%q 与 %q", missingErr, existingErr)
	}
}
