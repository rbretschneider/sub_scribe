package store

import (
	"database/sql"
	"time"
)

// Times are persisted as Unix seconds (INTEGER) and nullable times as NULL, so
// SQLite ordering and comparison on time columns is plain integer math. These
// helpers centralize the mapping so no repository open-codes it.

// toNullUnix converts an optional time to a nullable Unix-seconds column value.
func toNullUnix(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

// fromNullUnix converts a nullable Unix-seconds column into an optional UTC time.
func fromNullUnix(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

// fromUnix converts a non-null Unix-seconds column into a UTC time.
func fromUnix(secs int64) time.Time {
	return time.Unix(secs, 0).UTC()
}

// toNullID converts an optional foreign key into a nullable column value.
func toNullID(id *int64) sql.NullInt64 {
	if id == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *id, Valid: true}
}

// fromNullID converts a nullable foreign-key column into an optional id.
func fromNullID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}
