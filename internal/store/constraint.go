package store

import (
	"errors"
	"slices"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// ConstraintKind 区分数据库约束被违反的种类。
//
// 分类放在 store 是因为只有这里能导入驱动。
// 仓储需要它把「重复注册」与「引用了不存在的设备」翻译成不同的错误码，
// 否则两者都会变成 internal，用户与账本都看不出发生了什么。
type ConstraintKind int

const (
	// NoConstraintViolation 表示这不是一次约束违反。
	NoConstraintViolation ConstraintKind = iota
	// UniqueViolation 是唯一索引或主键冲突。
	UniqueViolation
	// ReferenceViolation 是外键指向了不存在的行。
	ReferenceViolation
	// StillReferencedViolation 是删除或改写了一行，而它仍被别的行引用
	// （ON DELETE RESTRICT 生效）。这与 ReferenceViolation 是相反的两件事：
	// 前者是「你给的引用不存在」，这里是「别人还在引用你」。
	StillReferencedViolation
	// ValueViolation 是 CHECK 或 NOT NULL 不通过，即写入的值本身不合法。
	ValueViolation
)

// ClassifyConstraint 判断 err 是哪一类约束违反。
// 不是约束违反（含 nil）时返回 NoConstraintViolation。
func ClassifyConstraint(err error) ConstraintKind {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return NoConstraintViolation
	}

	code := sqliteErr.Code()
	switch {
	case slices.Contains([]int{sqlitelib.SQLITE_CONSTRAINT_UNIQUE, sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY}, code):
		return UniqueViolation
	case code == sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY:
		return ReferenceViolation
	// SQLite 用内部触发器实现外键，因此「父行仍被引用」报的是
	// SQLITE_CONSTRAINT_TRIGGER 而不是 _FOREIGNKEY。本库不使用任何用户触发器，
	// 这个码只可能来自 RESTRICT。
	case code == sqlitelib.SQLITE_CONSTRAINT_TRIGGER:
		return StillReferencedViolation
	case slices.Contains([]int{sqlitelib.SQLITE_CONSTRAINT_CHECK, sqlitelib.SQLITE_CONSTRAINT_NOTNULL}, code):
		return ValueViolation
	default:
		return NoConstraintViolation
	}
}
