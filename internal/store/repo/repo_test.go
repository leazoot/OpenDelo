package repo_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// repositories 是三个仓储的一组实例，共用同一个已迁移的数据库。
// db 保留下来是为了少数几条必须绕过仓储、直接看库里存了什么的用例。
type repositories struct {
	db         *store.DB
	devices    *repo.Devices
	workspaces *repo.Workspaces
	agents     *repo.Agents
}

func newRepositories(t *testing.T) repositories {
	t.Helper()

	db := fixtures.MigratedDB(t)
	return repositories{
		db:         db,
		devices:    repo.NewDevices(db),
		workspaces: repo.NewWorkspaces(db),
		agents:     repo.NewAgents(db),
	}
}

// seeded 准备好设备与工作区后返回三个仓储，供 Agent 用例使用。
func seeded(t *testing.T) repositories {
	t.Helper()

	all := newRepositories(t)
	if _, err := all.devices.CreateDevice(t.Context(), fixtures.Device()); err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}
	if _, err := all.workspaces.CreateWorkspace(t.Context(), fixtures.Workspace()); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}
	return all
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

func TestDevices_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := newRepositories(t)
	want := fixtures.Device()

	created, err := all.devices.CreateDevice(ctx, want)
	if err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := all.devices.DeviceByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byFingerprint, err := all.devices.DeviceByFingerprint(ctx, want.Fingerprint)
	if err != nil {
		t.Fatalf("按指纹读取失败：%v", err)
	}
	if byFingerprint != want {
		t.Errorf("按指纹读到 %+v，期望 %+v", byFingerprint, want)
	}
}

func TestDevices_DuplicateFingerprint_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	all := newRepositories(t)

	if _, err := all.devices.CreateDevice(ctx, fixtures.Device()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}

	_, err := all.devices.CreateDevice(ctx, fixtures.Device(
		fixtures.WithDeviceID("01K1DEVICE0000000000000002"),
	))
	assertCode(t, err, apperr.CodeConflict)
}

func TestDevices_Missing_ReportsNotFound(t *testing.T) {
	// 读不到不能被当成内部错误：调用方要据此走注册流程而不是报故障。
	ctx := t.Context()
	all := newRepositories(t)

	_, err := all.devices.DeviceByID(ctx, "01K1MISSING000000000000000")
	assertCode(t, err, apperr.CodeNotFound)

	_, err = all.devices.DeviceByFingerprint(ctx, "fp-unknown")
	assertCode(t, err, apperr.CodeNotFound)
}

func TestDevices_SetTrustStatus_UpdatesStatusAndTimestamp(t *testing.T) {
	ctx := t.Context()
	all := newRepositories(t)
	if _, err := all.devices.CreateDevice(ctx, fixtures.Device()); err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}

	at := fixtures.Instant.Add(time.Hour)
	updated, err := all.devices.SetDeviceTrustStatus(
		ctx, fixtures.DefaultDeviceID, agentauth.DeviceUntrusted, at)
	if err != nil {
		t.Fatalf("更新信任状态失败：%v", err)
	}
	if updated.TrustStatus != agentauth.DeviceUntrusted {
		t.Errorf("信任状态是 %q，期望 untrusted", updated.TrustStatus)
	}
	if !updated.UpdatedAt.Equal(at) {
		t.Errorf("updated_at 是 %s，期望 %s", updated.UpdatedAt, at)
	}
	if !updated.CreatedAt.Equal(fixtures.Instant) {
		t.Errorf("created_at 被改成了 %s", updated.CreatedAt)
	}
}

func TestDevices_UnknownTrustStatus_ReportsInvalidRequest(t *testing.T) {
	// CHECK 约束的违反必须翻译成「请求不合法」而不是内部错误。
	ctx := t.Context()
	all := newRepositories(t)

	_, err := all.devices.CreateDevice(ctx, fixtures.Device(fixtures.WithDeviceTrust("maybe")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestWorkspaces_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := newRepositories(t)
	want := fixtures.Workspace()

	if _, err := all.workspaces.CreateWorkspace(ctx, want); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}

	byID, err := all.workspaces.WorkspaceByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byPath, err := all.workspaces.WorkspaceByPath(ctx, want.Path)
	if err != nil {
		t.Fatalf("按路径读取失败：%v", err)
	}
	if byPath != want {
		t.Errorf("按路径读到 %+v，期望 %+v", byPath, want)
	}
}

func TestWorkspaces_DuplicatePath_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	all := newRepositories(t)

	if _, err := all.workspaces.CreateWorkspace(ctx, fixtures.Workspace()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}

	_, err := all.workspaces.CreateWorkspace(ctx, fixtures.Workspace(
		fixtures.WithWorkspaceID("01K1WORKSPACE00000000000002"),
	))
	assertCode(t, err, apperr.CodeConflict)
}

func TestWorkspaces_SetFingerprint_ChangesOnlyTheFingerprint(t *testing.T) {
	// REQ-IDENT-003 AC3 的前提：指纹要能被更新，路径与创建时间不受影响。
	ctx := t.Context()
	all := newRepositories(t)
	if _, err := all.workspaces.CreateWorkspace(ctx, fixtures.Workspace()); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}

	at := fixtures.Instant.Add(2 * time.Hour)
	updated, err := all.workspaces.SetWorkspaceFingerprint(
		ctx, fixtures.DefaultWorkspaceID, "fp-project-changed", at)
	if err != nil {
		t.Fatalf("更新指纹失败：%v", err)
	}

	want := fixtures.Workspace()
	want.ProjectFingerprint = "fp-project-changed"
	want.UpdatedAt = at
	if updated != want {
		t.Errorf("更新后是 %+v，期望 %+v", updated, want)
	}
}

func TestAgents_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := seeded(t)
	want := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)

	created, err := all.agents.CreateAgent(ctx, want)
	if err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := all.agents.AgentByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestAgents_BySessionKeyHash_FindsTheRegisteredAgent(t *testing.T) {
	// 每个 Agent 请求都要走这条查询，它是身份校验的入口。
	ctx := t.Context()
	all := seeded(t)
	want := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)
	if _, err := all.agents.CreateAgent(ctx, want); err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}

	found, err := all.agents.AgentBySessionKeyHash(ctx, want.SessionKeyHash)
	if err != nil {
		t.Fatalf("按会话密钥哈希读取失败：%v", err)
	}
	if found != want {
		t.Errorf("读到 %+v，期望 %+v", found, want)
	}

	_, err = all.agents.AgentBySessionKeyHash(ctx, "sha256:not-issued")
	assertCode(t, err, apperr.CodeNotFound)
}

func TestAgents_ErrorDetails_NeverCarryTheSessionKeyHash(t *testing.T) {
	// 会话密钥的派生物不进错误详情：apperr 的 Error() 会进日志。
	ctx := t.Context()
	all := seeded(t)

	_, err := all.agents.AgentBySessionKeyHash(ctx, "sha256:secret-lookup-value")
	if err == nil {
		t.Fatal("未注册的哈希竟然查到了 Agent")
	}
	if message := err.Error(); strings.Contains(message, "secret-lookup-value") {
		t.Errorf("错误信息里带上了会话密钥哈希：%s", message)
	}
}

func TestAgents_UnreportedVersion_RoundTripsAsEmpty(t *testing.T) {
	// 空版本落库为 NULL，读回来仍然是空 —— 不能变成 "0" 或让整行读取失败。
	ctx := t.Context()
	all := seeded(t)
	want := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID,
		fixtures.WithAgentVersion(""))
	if _, err := all.agents.CreateAgent(ctx, want); err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}

	found, err := all.agents.AgentByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("读取 Agent 失败：%v", err)
	}
	if found.Version != "" {
		t.Errorf("未上报的版本读回来是 %q，期望空字符串", found.Version)
	}

	// 未上报要落成 NULL 而不是空串：空串在 SQL 里是一个「上报了空版本」的取值，
	// 将来按版本筛选时两者的语义不同。
	var isNull bool
	if err := all.db.Reader().QueryRowContext(ctx,
		`SELECT version IS NULL FROM agents WHERE id = ?`, want.ID).Scan(&isNull); err != nil {
		t.Fatalf("读取 version 列失败：%v", err)
	}
	if !isNull {
		t.Error("未上报的版本被存成了空串而不是 NULL")
	}
}

func TestAgents_CorruptedRow_IsRejectedInsteadOfSilentlyDecoded(t *testing.T) {
	// Fail Closed：读到不认识的值就拒绝。时间解析失败退回零值会让「早已过期」
	// 变成「远未过期」，进程号被截断则会指向另一个进程。
	ctx := t.Context()

	cases := []struct {
		name      string
		statement string
		value     any
	}{
		{name: "时间列格式非法", statement: `UPDATE agents SET session_expires_at = ? WHERE id = ?`, value: "昨天"},
		{name: "进程号超出范围", statement: `UPDATE agents SET pid = ? WHERE id = ?`, value: int64(5_000_000_000)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			all := seeded(t)
			created, err := all.agents.CreateAgent(ctx,
				fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID))
			if err != nil {
				t.Fatalf("写入 Agent 失败：%v", err)
			}

			if _, corruptErr := all.db.Writer().ExecContext(
				ctx, testCase.statement, testCase.value, created.ID); corruptErr != nil {
				t.Fatalf("制造损坏行失败：%v", corruptErr)
			}

			_, err = all.agents.AgentByID(ctx, created.ID)
			assertCode(t, err, apperr.CodeInternal)
		})
	}
}

func TestAgents_NoParentProcess_IsAccepted(t *testing.T) {
	// REQ-AGENT-001 AC3：无父进程时 parent_pid 为 0 是合法的。
	ctx := t.Context()
	all := seeded(t)

	created, err := all.agents.CreateAgent(ctx, fixtures.Agent(
		fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID, fixtures.WithParentPID(0)))
	if err != nil {
		t.Fatalf("父进程号为 0 的 Agent 被拒绝：%v", err)
	}
	if created.ParentPID != 0 {
		t.Errorf("父进程号读回来是 %d，期望 0", created.ParentPID)
	}
}

func TestAgents_UnknownDeviceOrWorkspace_ReportsInvalidRequest(t *testing.T) {
	// 外键指向不存在的行是调用方的问题，必须与网关故障区分开。
	ctx := t.Context()

	cases := []struct {
		name        string
		deviceID    string
		workspaceID string
	}{
		{name: "设备不存在", deviceID: "01K1MISSING000000000000000", workspaceID: fixtures.DefaultWorkspaceID},
		{name: "工作区不存在", deviceID: fixtures.DefaultDeviceID, workspaceID: "01K1MISSING000000000000000"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			all := seeded(t)
			_, err := all.agents.CreateAgent(ctx, fixtures.Agent(testCase.deviceID, testCase.workspaceID))
			assertCode(t, err, apperr.CodeInvalidRequest)
		})
	}
}

func TestAgents_DuplicateSessionKeyHash_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	all := seeded(t)

	if _, err := all.agents.CreateAgent(ctx,
		fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}

	_, err := all.agents.CreateAgent(ctx, fixtures.Agent(
		fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID,
		fixtures.WithAgentID("01K1AGENT00000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestAgents_UnknownType_ReportsInvalidRequest(t *testing.T) {
	ctx := t.Context()
	all := seeded(t)

	_, err := all.agents.CreateAgent(ctx, fixtures.Agent(
		fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID,
		fixtures.WithAgentType("rogue-agent")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestAgents_EveryDeclaredType_IsAccepted(t *testing.T) {
	// REQ-AGENT-003 AC1：五种类型都能注册。上一条用例只证明了非法值被拦，
	// 这条证明合法值没有被一起拦掉。
	ctx := t.Context()
	all := seeded(t)

	declared := []agentauth.AgentType{
		agentauth.TypeClaudeCode, agentauth.TypeCodex,
		agentauth.TypeGeminiCLI, agentauth.TypeOpenCode, agentauth.TypeGeneric,
	}

	for index, agentType := range declared {
		suffix := string(rune('A' + index))
		created, err := all.agents.CreateAgent(ctx, fixtures.Agent(
			fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID,
			fixtures.WithAgentID("01K1AGENTTYPE"+suffix+"0000000000000"),
			fixtures.WithSessionKeyHash("sha256:session-"+suffix),
			fixtures.WithAgentType(agentType)))
		if err != nil {
			t.Errorf("类型 %q 被拒绝：%v", agentType, err)
			continue
		}
		if created.Type != agentType {
			t.Errorf("类型读回来是 %q，期望 %q", created.Type, agentType)
		}
	}
}

func TestAgents_SetTrustLevel_MovesUnverifiedToKnown(t *testing.T) {
	// REQ-AGENT-002 AC3 的存储侧前提。
	ctx := t.Context()
	all := seeded(t)
	created, err := all.agents.CreateAgent(ctx,
		fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID))
	if err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	if created.TrustLevel != agentauth.TrustUnverified {
		t.Fatalf("新注册 Agent 的信任等级是 %q，期望 unverified", created.TrustLevel)
	}

	at := fixtures.Instant.Add(time.Hour)
	updated, err := all.agents.SetAgentTrustLevel(ctx, created.ID, agentauth.TrustKnown, at)
	if err != nil {
		t.Fatalf("更新信任等级失败：%v", err)
	}
	if updated.TrustLevel != agentauth.TrustKnown {
		t.Errorf("信任等级是 %q，期望 known", updated.TrustLevel)
	}
	if !updated.UpdatedAt.Equal(at) {
		t.Errorf("updated_at 是 %s，期望 %s", updated.UpdatedAt, at)
	}
}

func TestAgents_SetStatus_MarksTheAgentDisconnected(t *testing.T) {
	ctx := t.Context()
	all := seeded(t)
	created, err := all.agents.CreateAgent(ctx,
		fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID))
	if err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}

	at := fixtures.Instant.Add(30 * time.Minute)
	updated, err := all.agents.SetAgentStatus(ctx, created.ID, agentauth.StatusDisconnected, at)
	if err != nil {
		t.Fatalf("更新状态失败：%v", err)
	}
	if updated.Status != agentauth.StatusDisconnected {
		t.Errorf("状态是 %q，期望 disconnected", updated.Status)
	}
}

func TestAgents_Touch_AdvancesLastSeenAt(t *testing.T) {
	ctx := t.Context()
	all := seeded(t)
	created, err := all.agents.CreateAgent(ctx,
		fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID))
	if err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}

	seenAt := fixtures.Instant.Add(5 * time.Minute)
	updated, err := all.agents.TouchAgent(ctx, created.ID, seenAt)
	if err != nil {
		t.Fatalf("更新活动时间失败：%v", err)
	}
	if !updated.LastSeenAt.Equal(seenAt) {
		t.Errorf("last_seen_at 是 %s，期望 %s", updated.LastSeenAt, seenAt)
	}
	if !updated.StartedAt.Equal(created.StartedAt) {
		t.Errorf("started_at 被改成了 %s", updated.StartedAt)
	}
}

func TestAgents_StoredTimes_AreUTCWithMillisecondPrecision(t *testing.T) {
	// 时间一律 UTC。写入带时区的时刻后读回来
	// 必须是同一时刻的 UTC 表示，否则 Lease 过期判定会差几个小时。
	ctx := t.Context()
	all := seeded(t)

	zone := time.FixedZone("UTC+8", 8*60*60)
	startedAt := fixtures.Instant.In(zone)

	agent := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)
	agent.StartedAt = startedAt

	created, err := all.agents.CreateAgent(ctx, agent)
	if err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	if !created.StartedAt.Equal(fixtures.Instant) {
		t.Errorf("started_at 是 %s，期望与 %s 同一时刻", created.StartedAt, fixtures.Instant)
	}
	if created.StartedAt.Location() != time.UTC {
		t.Errorf("started_at 的时区是 %s，期望 UTC", created.StartedAt.Location())
	}

	// 读回来是 UTC 还不够：带偏移量写进去的文本（…+08:00）解析后同样是正确时刻，
	// 却让「字典序等于时间序」不再成立，按时间排序与分页会错乱。所以要看列里存的文本。
	var stored string
	if err := all.db.Reader().QueryRowContext(ctx,
		`SELECT started_at FROM agents WHERE id = ?`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("读取 started_at 列失败：%v", err)
	}
	if stored != "2026-07-28T09:15:30.123Z" {
		t.Errorf("started_at 列里存的是 %q，期望 UTC 表示", stored)
	}
}

func TestAgentRepository_ThroughTheCoreInterface_Works(t *testing.T) {
	// 「接口定义在 core、实现在 store」是依赖倒置的落点。这里只经接口调用，
	// 证明 core 侧拿着接口就够用，不需要知道 SQLite 的存在。
	ctx := t.Context()
	all := seeded(t)

	var agents agentauth.AgentRepository = all.agents

	want := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)
	if _, err := agents.CreateAgent(ctx, want); err != nil {
		t.Fatalf("经接口写入失败：%v", err)
	}

	found, err := agents.AgentBySessionKeyHash(ctx, want.SessionKeyHash)
	if err != nil {
		t.Fatalf("经接口读取失败：%v", err)
	}
	if found != want {
		t.Errorf("经接口读到 %+v，期望 %+v", found, want)
	}
}

func TestClassifyConstraint_NonDatabaseError_IsNotAConstraintViolation(t *testing.T) {
	// 分类器不能把任意错误都当成约束违反，否则真正的内部故障会被报成 400。
	if kind := store.ClassifyConstraint(errors.New("连接被重置")); kind != store.NoConstraintViolation {
		t.Errorf("普通错误被分类为 %v", kind)
	}
	if kind := store.ClassifyConstraint(nil); kind != store.NoConstraintViolation {
		t.Errorf("nil 被分类为 %v", kind)
	}
}
