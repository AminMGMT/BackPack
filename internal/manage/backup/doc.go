// Package backup is everything that writes the machine's state to a file and
// puts it back: the backup archive, the restore, the nightly automatic copy,
// and the update snapshots that make a rollback possible.
//
// It is the second seam cut out of internal/manage, and it was chosen for the
// same reason as core: it is a leaf. It sits on internal/manage/core and on
// nothing else in that package, so lifting it created no cycle to break.
//
// The public names are re-exported from internal/manage so callers did not
// move; see internal/manage/core_alias.go.
package backup
