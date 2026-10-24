// Package source defines the one thing dbmeta may ask of a database: the
// structure of a single, named table. There is deliberately no method that
// accepts SQL, lists databases, or lists tables.
package source

import (
	"context"
	"errors"

	"dbmeta-cli/internal/schema"
)

// Errors the CLI maps to exit codes. Implementations wrap them with detail.
var (
	ErrAuth     = errors.New("认证失败")     // exit 3
	ErrNotFound = errors.New("表不存在或无权限") // exit 4
	ErrConn     = errors.New("连接失败")     // exit 6
)

type Source interface {
	// Ping connects and runs a trivial statement to prove the account works.
	Ping(ctx context.Context) error
	// FetchTable returns the structure of db.table. Instance, FetchedAt and
	// SchemaHash are filled in by the caller.
	FetchTable(ctx context.Context, db, table string) (*schema.TableSchema, error)
	Close() error
}
