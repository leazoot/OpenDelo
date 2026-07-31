package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Devices 是 agentauth.DeviceRepository 的 SQLite 实现。
type Devices struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ agentauth.DeviceRepository = (*Devices)(nil)

// NewDevices 绑定到已迁移的数据库。读走读池、写走写池，读多的查询不会占住唯一的写连接。
func NewDevices(db *store.DB) *Devices {
	return &Devices{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (d *Devices) CreateDevice(ctx context.Context, device agentauth.Device) (agentauth.Device, error) {
	row, err := d.write.CreateDevice(ctx, queries.CreateDeviceParams{
		ID:          device.ID,
		Fingerprint: device.Fingerprint,
		Name:        device.Name,
		TrustStatus: string(device.TrustStatus),
		CreatedAt:   encodeTime(device.CreatedAt),
		UpdatedAt:   encodeTime(device.UpdatedAt),
	})
	if err != nil {
		return agentauth.Device{}, writeError(err, "写入设备 "+device.ID+" 失败")
	}
	return toDevice(row)
}

func (d *Devices) DeviceByID(ctx context.Context, id string) (agentauth.Device, error) {
	row, err := d.read.GetDeviceByID(ctx, id)
	if err != nil {
		return agentauth.Device{}, readError(err, "读取设备 "+id+" 失败")
	}
	return toDevice(row)
}

func (d *Devices) DeviceByFingerprint(ctx context.Context, fingerprint string) (agentauth.Device, error) {
	row, err := d.read.GetDeviceByFingerprint(ctx, fingerprint)
	if err != nil {
		// 指纹是设备的身份，不进错误详情：它在账本里等价于「哪台机器」。
		return agentauth.Device{}, readError(err, "按指纹读取设备失败")
	}
	return toDevice(row)
}

func (d *Devices) SetDeviceTrustStatus(
	ctx context.Context, id string, status agentauth.DeviceTrust, at time.Time,
) (agentauth.Device, error) {
	row, err := d.write.UpdateDeviceTrustStatus(ctx, queries.UpdateDeviceTrustStatusParams{
		TrustStatus: string(status),
		UpdatedAt:   encodeTime(at),
		ID:          id,
	})
	if err != nil {
		return agentauth.Device{}, writeError(err, "更新设备 "+id+" 的信任状态失败")
	}
	return toDevice(row)
}

func toDevice(row queries.Device) (agentauth.Device, error) {
	createdAt, err := decodeTime("devices.created_at", row.CreatedAt)
	if err != nil {
		return agentauth.Device{}, err
	}
	updatedAt, err := decodeTime("devices.updated_at", row.UpdatedAt)
	if err != nil {
		return agentauth.Device{}, err
	}

	return agentauth.Device{
		ID:          row.ID,
		Fingerprint: row.Fingerprint,
		Name:        row.Name,
		TrustStatus: agentauth.DeviceTrust(row.TrustStatus),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}
