// Package store is sub_scribe's persistence layer: a thin, well-scoped SQLite
// data mapper. It owns connection setup, schema migration, and repositories that
// translate between domain entities and rows using only parameterized queries.
// The rest of the app depends on the repository interfaces, not this package.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver: keeps the binary static, no CGo
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// maxOpenConns bounds the connection pool. WAL mode allows many concurrent
// readers alongside a single writer, so a pool lets web reads run in parallel
// with background workers instead of serializing through one connection. Writers
// still serialize at the SQLite level; the busy_timeout pragma makes a contended
// write wait briefly rather than fail.
const maxOpenConns = 16

// DB owns the SQLite connection pool and hands out repositories. It is created
// once at startup and shared; repositories are stateless views over it.
type DB struct {
	sql *sql.DB
}

// Open connects to the SQLite database at path, applies durability and
// concurrency pragmas, and runs migrations. Tests pass a temp-file path (not
// ":memory:", which would give each pooled connection its own database). WAL
// mode plus a small connection pool lets web reads run concurrently with
// background workers; the per-row writes here are far cheaper than the yt-dlp
// work they coordinate.
func Open(path string) (*DB, error) {
	// synchronous(NORMAL) with WAL skips the fsync on every commit — the
	// difference between ~7ms and sub-millisecond per insert, which is the whole
	// cost of indexing a large channel. In WAL mode NORMAL cannot corrupt the
	// database; the exposure is only that the very last commits may roll back
	// after a power cut, and every row here is rediscoverable by the next scan.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", path)
	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxOpenConns)

	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return db, nil
}

// Close releases the underlying connection pool.
func (db *DB) Close() error {
	return db.sql.Close()
}

// Tasks returns the repository for the background task queue.
func (db *DB) Tasks() *TaskRepo {
	return &TaskRepo{sql: db.sql}
}

// Sources returns the repository for tracked collections.
func (db *DB) Sources() *SourceRepo {
	return &SourceRepo{sql: db.sql}
}

// Media returns the repository for downloadable media items.
func (db *DB) Media() *MediaRepo {
	return &MediaRepo{sql: db.sql}
}

// Profiles returns the repository for media profiles.
func (db *DB) Profiles() *ProfileRepo {
	return &ProfileRepo{sql: db.sql}
}
