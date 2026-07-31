package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Workspaces 是 agentauth.WorkspaceRepository 的 SQLite 实现。
type Workspaces struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ agentauth.WorkspaceRepository = (*Workspaces)(nil)

// NewWorkspaces 绑定到已迁移的数据库。
func NewWorkspaces(db *store.DB) *Workspaces {
	return &Workspaces{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (w *Workspaces) CreateWorkspace(
	ctx context.Context, workspace agentauth.Workspace,
) (agentauth.Workspace, error) {
	row, err := w.write.CreateWorkspace(ctx, queries.CreateWorkspaceParams{
		ID:                 workspace.ID,
		Path:               workspace.Path,
		ProjectFingerprint: workspace.ProjectFingerprint,
		CreatedAt:          encodeTime(workspace.CreatedAt),
		UpdatedAt:          encodeTime(workspace.UpdatedAt),
	})
	if err != nil {
		return agentauth.Workspace{}, writeError(err, "写入工作区 "+workspace.ID+" 失败")
	}
	return toWorkspace(row)
}

func (w *Workspaces) WorkspaceByID(ctx context.Context, id string) (agentauth.Workspace, error) {
	row, err := w.read.GetWorkspaceByID(ctx, id)
	if err != nil {
		return agentauth.Workspace{}, readError(err, "读取工作区 "+id+" 失败")
	}
	return toWorkspace(row)
}

func (w *Workspaces) WorkspaceByPath(ctx context.Context, path string) (agentauth.Workspace, error) {
	row, err := w.read.GetWorkspaceByPath(ctx, path)
	if err != nil {
		// 路径不进错误详情：它会暴露用户的目录结构。
		return agentauth.Workspace{}, readError(err, "按路径读取工作区失败")
	}
	return toWorkspace(row)
}

func (w *Workspaces) SetWorkspaceFingerprint(
	ctx context.Context, id, fingerprint string, at time.Time,
) (agentauth.Workspace, error) {
	row, err := w.write.UpdateWorkspaceFingerprint(ctx, queries.UpdateWorkspaceFingerprintParams{
		ProjectFingerprint: fingerprint,
		UpdatedAt:          encodeTime(at),
		ID:                 id,
	})
	if err != nil {
		return agentauth.Workspace{}, writeError(err, "更新工作区 "+id+" 的项目指纹失败")
	}
	return toWorkspace(row)
}

func toWorkspace(row queries.Workspace) (agentauth.Workspace, error) {
	createdAt, err := decodeTime("workspaces.created_at", row.CreatedAt)
	if err != nil {
		return agentauth.Workspace{}, err
	}
	updatedAt, err := decodeTime("workspaces.updated_at", row.UpdatedAt)
	if err != nil {
		return agentauth.Workspace{}, err
	}

	return agentauth.Workspace{
		ID:                 row.ID,
		Path:               row.Path,
		ProjectFingerprint: row.ProjectFingerprint,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
	}, nil
}
