// Package fixtures 提供测试夹具，避免每个测试文件各造一份「差不多的 Agent」
//
// 构造器的默认值一律合法，用例只写自己关心的差异。
package fixtures

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/store"
)

// Instant 是夹具使用的固定时刻。用固定值而不是当前时间，使断言可以精确比较。
var Instant = time.Date(2026, time.July, 28, 9, 15, 30, 123_000_000, time.UTC)

// 夹具默认使用的主键。用例需要第二行时用对应的 With* 选项换掉。
const (
	DefaultDeviceID     = "01K1DEVICE0000000000000000"
	DefaultWorkspaceID  = "01K1WORKSPACE00000000000000"
	DefaultAgentID      = "01K1AGENT00000000000000000"
	DefaultProviderID   = "01K1PROVIDER000000000000000"
	DefaultReferenceID  = "01K1REFERENCE00000000000000"
	DefaultIdentityID   = "01K1IDENTITY000000000000000"
	DefaultAdapterID    = "01K1ADAPTER000000000000000"
	DefaultRequestID    = "01K1REQUEST000000000000000"
	DefaultDecisionID   = "01K1DECISION00000000000000"
	DefaultApprovalID   = "01K1APPROVAL00000000000000"
	DefaultLeaseID      = "01K1LEASE00000000000000000"
	DefaultMemoryID     = "01K1MEMORY0000000000000000"
	DefaultEventID      = "01K1EVENT00000000000000000"
	DefaultServiceLabel = "github"
)

// MigratedDB 在独立的临时目录里开一个已迁移到最新版本的数据库。
// 每个用例一份，用例之间不共享任何状态。
func MigratedDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), store.FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭数据库失败：%v", err)
		}
	})

	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db
}

// DeviceOption 调整设备夹具。
type DeviceOption func(*agentauth.Device)

// WithDeviceID 换掉设备主键。
func WithDeviceID(id string) DeviceOption {
	return func(device *agentauth.Device) { device.ID = id }
}

// WithDeviceFingerprint 换掉设备指纹。
func WithDeviceFingerprint(fingerprint string) DeviceOption {
	return func(device *agentauth.Device) { device.Fingerprint = fingerprint }
}

// WithDeviceTrust 换掉设备信任状态。
func WithDeviceTrust(status agentauth.DeviceTrust) DeviceOption {
	return func(device *agentauth.Device) { device.TrustStatus = status }
}

// Device 构造一台合法的设备。
func Device(options ...DeviceOption) agentauth.Device {
	device := agentauth.Device{
		ID:          DefaultDeviceID,
		Fingerprint: "fp-device-0001",
		Name:        "Tester 的 MacBook",
		TrustStatus: agentauth.DeviceTrusted,
		CreatedAt:   Instant,
		UpdatedAt:   Instant,
	}
	for _, apply := range options {
		apply(&device)
	}
	return device
}

// WorkspaceOption 调整工作区夹具。
type WorkspaceOption func(*agentauth.Workspace)

// WithWorkspaceID 换掉工作区主键。
func WithWorkspaceID(id string) WorkspaceOption {
	return func(workspace *agentauth.Workspace) { workspace.ID = id }
}

// WithWorkspacePath 换掉工作区路径。
func WithWorkspacePath(path string) WorkspaceOption {
	return func(workspace *agentauth.Workspace) { workspace.Path = path }
}

// Workspace 构造一个合法的工作区。
func Workspace(options ...WorkspaceOption) agentauth.Workspace {
	workspace := agentauth.Workspace{
		ID:                 DefaultWorkspaceID,
		Path:               "/Users/tester/projects/opendelo",
		ProjectFingerprint: "fp-project-0001",
		CreatedAt:          Instant,
		UpdatedAt:          Instant,
	}
	for _, apply := range options {
		apply(&workspace)
	}
	return workspace
}

// AgentOption 调整 Agent 夹具。
type AgentOption func(*agentauth.Agent)

// WithAgentID 换掉 Agent 主键。
func WithAgentID(id string) AgentOption {
	return func(agent *agentauth.Agent) { agent.ID = id }
}

// WithSessionKeyHash 换掉会话密钥哈希。
func WithSessionKeyHash(hash string) AgentOption {
	return func(agent *agentauth.Agent) { agent.SessionKeyHash = hash }
}

// WithAgentVersion 换掉上报的版本；传空字符串表示未上报。
func WithAgentVersion(version string) AgentOption {
	return func(agent *agentauth.Agent) { agent.Version = version }
}

// WithAgentType 换掉接入类型。
func WithAgentType(agentType agentauth.AgentType) AgentOption {
	return func(agent *agentauth.Agent) { agent.Type = agentType }
}

// WithParentPID 换掉父进程号；0 表示没有父进程。
func WithParentPID(parentPID int) AgentOption {
	return func(agent *agentauth.Agent) { agent.ParentPID = parentPID }
}

// Agent 构造一个合法的 Agent。设备与工作区必须由调用方给出：
// 没有它们的 Agent 在数据库层面就不合法（外键 NOT NULL）。
func Agent(deviceID, workspaceID string, options ...AgentOption) agentauth.Agent {
	agent := agentauth.Agent{
		ID:               DefaultAgentID,
		Name:             "claude-code",
		Type:             agentauth.TypeClaudeCode,
		Version:          "2.1.0",
		ExecutableHash:   "sha256:6f1d0c9a2b",
		ExecutablePath:   "/usr/local/bin/claude",
		PID:              4321,
		ParentPID:        4320,
		OSUser:           "tester",
		DeviceID:         deviceID,
		WorkspaceID:      workspaceID,
		StartedAt:        Instant,
		SessionKeyHash:   "sha256:session-0001",
		SessionExpiresAt: Instant.Add(15 * time.Minute),
		TrustLevel:       agentauth.TrustUnverified,
		Status:           agentauth.StatusActive,
		LastSeenAt:       Instant,
		CreatedAt:        Instant,
		UpdatedAt:        Instant,
	}
	for _, apply := range options {
		apply(&agent)
	}
	return agent
}

// RegistrationOption 调整注册请求夹具。
//
// 这里不预先给出一堆具名选项：REQ-AGENT-001 的用例大多是「抹掉某一项绑定看还能不能
// 注册」，需要能改到任意字段，直接传闭包比为每个字段各写一个选项清楚得多。
type RegistrationOption func(*agentauth.Registration)

// Registration 构造一个合法的注册请求，九项身份绑定齐全。
func Registration(options ...RegistrationOption) agentauth.Registration {
	registration := agentauth.Registration{
		Name:               "claude-code",
		Type:               agentauth.TypeClaudeCode,
		Version:            "2.1.0",
		ExecutableHash:     "sha256:6f1d0c9a2b",
		ExecutablePath:     "/usr/local/bin/claude",
		PID:                4321,
		ParentPID:          4320,
		OSUser:             "tester",
		DeviceFingerprint:  "fp-device-0001",
		DeviceName:         "Tester 的 MacBook",
		WorkspacePath:      "/Users/tester/projects/opendelo",
		ProjectFingerprint: "fp-project-0001",
		StartedAt:          Instant,
	}
	for _, apply := range options {
		apply(&registration)
	}
	return registration
}
