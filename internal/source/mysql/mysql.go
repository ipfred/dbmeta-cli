// Package mysql reads table structure from MySQL-protocol servers (MySQL,
// MariaDB, TiDB). Every statement it can send is a constant in this file.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/schema"
	"dbmeta-cli/internal/source"
)

// The complete SQL allowlist. Nothing else is ever sent to the server.
const (
	sqlPing    = `SELECT 1`
	sqlVersion = `SELECT VERSION()`

	sqlTables = `SELECT TABLE_COMMENT, ENGINE, TABLE_COLLATION, TABLE_ROWS
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`

	sqlColumns = `SELECT ORDINAL_POSITION, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`

	sqlIndexes = `SELECT INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME, SUB_PART, INDEX_TYPE, INDEX_COMMENT
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX`

	// sqlShowCreate is a format string; its two operands are identifiers that
	// have already passed guard.Ident and so cannot contain a backtick.
	sqlShowCreate = "SHOW CREATE TABLE `%s`.`%s`"

	// Session hardening. All but the read-only switch are best effort because
	// MariaDB and older servers spell the timeout differently or lack it.
	sqlReadOnly      = `SET SESSION TRANSACTION READ ONLY`
	sqlMaxExecMS     = `SET SESSION max_execution_time = 30000`
	sqlMaxStatementS = `SET SESSION max_statement_time = 30`
)

const (
	connectTimeout = 10 * time.Second
	ioTimeout      = 30 * time.Second
)

// Options tune a connection. Warn receives non-fatal hardening problems.
type Options struct {
	Warn func(format string, args ...any)
}

type mysqlSource struct {
	conn   *sql.Conn
	db     *sql.DB
	server string
	warn   func(string, ...any)
}

// Open dials the instance and hardens the session. The password must already
// be non-empty; the caller checks that before any network traffic.
func Open(ctx context.Context, inst *config.Instance, opts Options) (source.Source, error) {
	cfg := gomysql.NewConfig()
	cfg.User = inst.User
	cfg.Passwd = inst.Password
	cfg.Net = "tcp"
	cfg.Addr = inst.Addr()
	cfg.TLSConfig = inst.EffectiveTLS()
	cfg.Timeout = connectTimeout
	cfg.ReadTimeout = ioTimeout
	cfg.WriteTimeout = ioTimeout
	cfg.AllowNativePasswords = true

	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", source.ErrConn, err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return fromDB(ctx, db, inst.Addr(), opts)
}

// fromDB takes ownership of db. Split out so tests can supply sqlmock.
func fromDB(ctx context.Context, db *sql.DB, server string, opts Options) (source.Source, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, classify(err)
	}
	s := &mysqlSource{conn: conn, db: db, server: server, warn: opts.Warn}
	if s.warn == nil {
		s.warn = func(string, ...any) {}
	}
	if _, err := conn.ExecContext(ctx, sqlReadOnly); err != nil {
		s.warn("无法将会话设为只读(%v);请确认使用的是只读账号", err)
	}
	if _, err := conn.ExecContext(ctx, sqlMaxExecMS); err != nil {
		if _, err2 := conn.ExecContext(ctx, sqlMaxStatementS); err2 != nil {
			s.warn("无法设置语句超时,依赖连接读写超时 %s", ioTimeout)
		}
	}
	return s, nil
}

func (s *mysqlSource) Close() error {
	err := s.conn.Close()
	if e := s.db.Close(); err == nil {
		err = e
	}
	return err
}

func (s *mysqlSource) Ping(ctx context.Context) error {
	var one int
	if err := s.conn.QueryRowContext(ctx, sqlPing).Scan(&one); err != nil {
		return classify(err)
	}
	return nil
}

func (s *mysqlSource) FetchTable(ctx context.Context, db, table string) (*schema.TableSchema, error) {
	// Re-validate here: this package must be safe even if a caller forgets to.
	if err := guard.Ident("库名", db); err != nil {
		return nil, err
	}
	if err := guard.Ident("表名", table); err != nil {
		return nil, err
	}

	ts := &schema.TableSchema{Database: db, Table: table, Server: s.server}

	if err := s.conn.QueryRowContext(ctx, sqlVersion).Scan(&ts.ServerVersion); err != nil {
		return nil, classify(err)
	}

	var comment, engine, collation sql.NullString
	var rows sql.NullInt64
	err := s.conn.QueryRowContext(ctx, sqlTables, db, table).Scan(&comment, &engine, &collation, &rows)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s.%s", source.ErrNotFound, db, table)
	}
	if err != nil {
		return nil, classify(err)
	}
	ts.Comment, ts.Engine = comment.String, engine.String
	ts.Charset = charsetOf(collation.String)
	ts.RowsEstimate = -1
	if rows.Valid {
		ts.RowsEstimate = rows.Int64
	}

	if ts.Columns, err = s.columns(ctx, db, table); err != nil {
		return nil, err
	}
	if ts.Indexes, err = s.indexes(ctx, db, table); err != nil {
		return nil, err
	}
	if ts.DDL, err = s.ddl(ctx, db, table); err != nil {
		return nil, err
	}
	return ts, nil
}

func (s *mysqlSource) columns(ctx context.Context, db, table string) ([]schema.Column, error) {
	rows, err := s.conn.QueryContext(ctx, sqlColumns, db, table)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	cols := []schema.Column{}
	for rows.Next() {
		var c schema.Column
		var nullable string
		var def sql.NullString
		if err := rows.Scan(&c.Ordinal, &c.Name, &c.Type, &nullable, &def, &c.Key, &c.Extra, &c.Comment); err != nil {
			return nil, classify(err)
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return cols, nil
}

func (s *mysqlSource) indexes(ctx context.Context, db, table string) ([]schema.Index, error) {
	rows, err := s.conn.QueryContext(ctx, sqlIndexes, db, table)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()

	byName := map[string]*schema.Index{}
	var order []string
	for rows.Next() {
		var name, typ, comment string
		var nonUnique, seq int
		var col sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &seq, &col, &sub, &typ, &comment); err != nil {
			return nil, classify(err)
		}
		idx, ok := byName[name]
		if !ok {
			idx = &schema.Index{Name: name, Unique: nonUnique == 0, Type: typ, Comment: comment}
			byName[name] = idx
			order = append(order, name)
		}
		part := "(表达式)"
		if col.Valid {
			part = col.String
		}
		if sub.Valid {
			part += "(" + strconv.FormatInt(sub.Int64, 10) + ")"
		}
		idx.Columns = append(idx.Columns, part)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	// PRIMARY first, the rest alphabetical (already the query order).
	sort.SliceStable(order, func(a, b int) bool {
		return order[a] == "PRIMARY" && order[b] != "PRIMARY"
	})
	out := make([]schema.Index, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// ddl reads SHOW CREATE TABLE generically: tables return (name, ddl) and views
// return (name, ddl, charset, collation); the DDL is always the second column.
func (s *mysqlSource) ddl(ctx context.Context, db, table string) (string, error) {
	rows, err := s.conn.QueryContext(ctx, fmt.Sprintf(sqlShowCreate, db, table))
	if err != nil {
		return "", classify(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", classify(err)
	}
	if len(cols) < 2 {
		return "", fmt.Errorf("SHOW CREATE TABLE 返回了意外的列数 %d", len(cols))
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", classify(err)
		}
		return "", fmt.Errorf("%w: %s.%s", source.ErrNotFound, db, table)
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", classify(err)
	}
	return string(vals[1]), nil
}

func charsetOf(collation string) string {
	cs, _, _ := strings.Cut(collation, "_")
	return cs
}

// classify maps driver errors onto the sentinel errors the CLI understands.
func classify(err error) error {
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1045, 1698, 1251: // access denied, auth plugin problems
			return fmt.Errorf("%w: %s", source.ErrAuth, me.Message)
		case 1044, 1142, 1143, 1049, 1146, 1109, 1051:
			return fmt.Errorf("%w: %s", source.ErrNotFound, me.Message)
		}
		return err
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.As(err, &ne), errors.Is(err, gomysql.ErrInvalidConn), errors.Is(err, sql.ErrConnDone):
		return fmt.Errorf("%w: %v", source.ErrConn, err)
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return fmt.Errorf("%w: %v", source.ErrConn, err)
	}
	return err
}
