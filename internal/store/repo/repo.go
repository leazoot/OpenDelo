package repo

import (
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
)

// timeFormat 是时间列的存储格式：UTC 的 RFC3339 带毫秒。
// 定长文本使字典序等于时间序，
// 按时间排序与分页因此可以直接走索引。
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

func encodeTime(instant time.Time) string {
	return instant.UTC().Format(timeFormat)
}

// decodeTime 解析时间列。解析失败意味着这一行不是本程序写的或已损坏，
// 按 Fail Closed 返回错误而不是退回零值时间（零值会让「已过期」变成「远未过期」）。
func decodeTime(column, value string) (time.Time, error) {
	instant, err := time.Parse(timeFormat, value)
	if err != nil {
		return time.Time{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("列 " + column + " 不是合法的时间")
	}
	return instant.UTC(), nil
}

// decodeProcessID 把数据库里的整数还原成进程号。
//
// 超出 int32 的值不可能是真实的 PID，只能是损坏的行；此时拒绝而不是截断，
// 截断出来的号码会指向另一个进程。
func decodeProcessID(column string, value int64) (int, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, apperr.New(apperr.CodeInternal).
			WithDetail("列 " + column + " 不是合法的进程号")
	}
	return int(value), nil
}

// optionalCount 把领域层的「0 即不设上限」转成可空列。
// 0 不能当成一个真实上限存下去 —— 那会让这条 Lease 一次都用不了。
func optionalCount(value int) sql.NullInt64 {
	if value <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(value), Valid: true}
}

// decodeOptionalCount 还原可空的计数列，NULL 还原为 0（不设上限）。
func decodeOptionalCount(column string, value sql.NullInt64) (int, error) {
	if !value.Valid {
		return 0, nil
	}
	return decodeCount(column, value.Int64)
}

// decodeCount 还原计数列。负数或超出 int32 的值只能来自损坏的行；
// 此时拒绝而不是截断，截断出来的次数会让上限判断失效。
func decodeCount(column string, value int64) (int, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, apperr.New(apperr.CodeInternal).
			WithDetail("列 " + column + " 不是合法的计数")
	}
	return int(value), nil
}

// optionalText 把领域层的「空即未提供」转成可空列。
func optionalText(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// optionalTime 把零值时间转成 NULL。零值不能当成一个真实时刻存下去 ——
// 「从未验证过」和「公元元年验证过」在后续判断里是两回事。
func optionalTime(instant time.Time) sql.NullString {
	if instant.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: encodeTime(instant), Valid: true}
}

// decodeOptionalTime 还原可空的时间列，NULL 还原为零值时间。
func decodeOptionalTime(column string, value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, nil
	}
	return decodeTime(column, value.String)
}

// encodeFlag 把布尔转成 SQLite 的 0/1。
func encodeFlag(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// decodeFlag 还原布尔列。0 与 1 以外的取值意味着这一行绕过了 CHECK 约束，
// 按 Fail Closed 拒绝而不是当成 true。
func decodeFlag(column string, value int64) (bool, error) {
	switch value {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, apperr.New(apperr.CodeInternal).
			WithDetail("列 " + column + " 不是合法的布尔值")
	}
}

// invalidLimit 报告列表查询的条数上限不合法。
func invalidLimit(limit int) error {
	return apperr.New(apperr.CodeInvalidRequest).
		WithDetail("列表查询的条数上限必须为正，收到 " + strconv.Itoa(limit))
}

// writeError 把写入错误翻译成对应的错误码。
//
// 约束违反是调用方的问题而不是网关故障，必须区分开：重复注册是 conflict，
// 引用了不存在的设备或写入了枚举外的取值是 invalid_request。
func writeError(err error, detail string) error {
	switch store.ClassifyConstraint(err) {
	// 「已经存在」与「还有人在用」都是 409：调用方看到的是同一件事的两面 ——
	// 当前状态与这次请求不相容，而不是请求本身写错了。
	case store.UniqueViolation, store.StillReferencedViolation:
		return apperr.Wrap(apperr.CodeConflict, err).WithDetail(detail)
	case store.ReferenceViolation, store.ValueViolation:
		return apperr.Wrap(apperr.CodeInvalidRequest, err).WithDetail(detail)
	default:
		return readError(err, detail)
	}
}

// readError 把读取错误翻译成对应的错误码。行不存在是 not_found，其余是内部错误。
func readError(err error, detail string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap(apperr.CodeNotFound, err).WithDetail(detail)
	}
	return apperr.Wrap(apperr.CodeInternal, err).WithDetail(detail)
}
