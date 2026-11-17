// Package source defines the only things dbmeta may ask of a database: the
// structure of a single, named table, and the names of the databases the
// account can see (used to route "db.table" to instances). There is
// deliberately no method that accepts SQL or lists tables.
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
	// ListDatabases returns the user databases visible to the account: system
	// schemas and names that fail guard.Ident are dropped. Sorted, unique.
	ListDatabases(ctx context.Context) ([]string, error)
	Close() error
}
