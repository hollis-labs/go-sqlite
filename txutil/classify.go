package txutil

import (
	"errors"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsBusy reports whether err is or wraps a modernc SQLITE_BUSY result.
// All extended forms (SQLITE_BUSY_RECOVERY, SQLITE_BUSY_SNAPSHOT,
// SQLITE_BUSY_TIMEOUT) are covered because the check masks to the primary
// 8-bit code.
func IsBusy(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()&0xff == sqlite3.SQLITE_BUSY
	}
	return false
}

// IsLocked reports whether err is or wraps a modernc SQLITE_LOCKED result.
// SQLITE_LOCKED_SHAREDCACHE is covered by the primary-code mask.
func IsLocked(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()&0xff == sqlite3.SQLITE_LOCKED
	}
	return false
}

// IsRetryableLock is the default retry classifier used by [WithRetry] and
// [WithImmediateRetry]. It returns true for SQLITE_BUSY or SQLITE_LOCKED.
//
// SQLITE_CONSTRAINT, SQLITE_READONLY, and other non-contention errors are
// not retried.
func IsRetryableLock(err error) bool {
	return IsBusy(err) || IsLocked(err)
}
