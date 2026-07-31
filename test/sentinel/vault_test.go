package sentinel_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/localvault"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 八个面的哨兵扫描之一：本地存储（REQ-CRED-004、REQ-NFR-002 AC1）。
 *
 * 保险库是本产品唯一存放凭据密文的地方。这一面扫的是数据目录下的**每一个文件**，
 * 而不只是保险库本身 —— 写入过程中的临时文件、备份文件同样不能留下明文。
 */

var vaultInstant = time.Date(2026, time.July, 28, 9, 15, 30, 123_000_000, time.UTC)

func TestVaultFace_NoSentinelInAnyFileUnderTheDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	vault, err := localvault.New(localvault.Options{
		Path:  filepath.Join(dataDir, localvault.FileName),
		Clock: clock.NewFixed(vaultInstant),
	})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}

	master := secret.NewString(sentinel.SentinelPassword)
	if createErr := vault.Create(master); createErr != nil {
		t.Fatalf("创建保险库失败：%v", createErr)
	}

	// 四个哨兵各存一条，条目名也用哨兵 —— 键名同样不该以明文落盘。
	for index, value := range sentinel.All() {
		reference := "service-" + value
		if putErr := vault.Put(reference, secret.NewString(value)); putErr != nil {
			t.Fatalf("写入第 %d 条失败：%v", index, putErr)
		}
	}

	// 导出一份备份放进同一个目录，它同样要接受扫描。
	backup, err := vault.ExportBackup()
	if err != nil {
		t.Fatalf("导出备份失败：%v", err)
	}
	backupPath := filepath.Join(dataDir, "vault.backup")
	if err := os.WriteFile(backupPath, backup, localvault.FilePermission); err != nil {
		t.Fatalf("写出备份失败：%v", err)
	}

	assertNoSentinelUnder(t, dataDir)
}

func TestVaultFace_LockedVaultKeepsNothingInItsFiles(t *testing.T) {
	// 锁定会清零内存中的主密码，但磁盘上的密文照旧 —— 它本来就该是密文。
	dataDir := t.TempDir()
	vault, err := localvault.New(localvault.Options{
		Path:  filepath.Join(dataDir, localvault.FileName),
		Clock: clock.NewFixed(vaultInstant),
	})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}

	if err := vault.Create(secret.NewString(sentinel.SentinelPassword)); err != nil {
		t.Fatalf("创建保险库失败：%v", err)
	}
	if err := vault.Put("github/token", secret.NewString(sentinel.SentinelToken)); err != nil {
		t.Fatalf("写入条目失败：%v", err)
	}
	vault.Lock()

	assertNoSentinelUnder(t, dataDir)
}

func TestVaultFace_ReverseControl_ProvesTheScanWorks(t *testing.T) {
	// 反向对照：同一个目录里放一份未加密的文件，扫描必须命中它。
	// 少了这一条，一个「什么都不看」的扫描也会全绿。
	dataDir := t.TempDir()
	leaked := filepath.Join(dataDir, "leaked.txt")
	if err := os.WriteFile(leaked, []byte(sentinel.SentinelToken), 0o600); err != nil {
		t.Fatalf("写出对照文件失败：%v", err)
	}

	if !containsSentinel(t, dataDir) {
		t.Fatal("扫描没有命中明文文件，说明它根本没在看这些文件")
	}
}

func assertNoSentinelUnder(t *testing.T, dataDir string) {
	t.Helper()

	if containsSentinel(t, dataDir) {
		t.Errorf("%s 下的文件中出现了哨兵", dataDir)
	}
}

// containsSentinel 遍历目录下的每一个文件，逐字节查找哨兵。
func containsSentinel(t *testing.T, dataDir string) bool {
	t.Helper()

	var found bool
	var scanned int
	walkErr := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		for _, value := range sentinel.All() {
			if strings.Contains(string(content), value) {
				t.Logf("%s 命中哨兵 %s", filepath.Base(path), value)
				found = true
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历数据目录失败：%v", walkErr)
	}
	if scanned == 0 {
		t.Fatal("目录里一个文件都没有，这次扫描什么也没证明")
	}
	return found
}
