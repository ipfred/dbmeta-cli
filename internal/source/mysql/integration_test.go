package mysql

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/source"
)

// Runs against a real server when DBMETA_TEST_DSN is set, for example
//
//	DBMETA_TEST_DSN='readonly:secret@tcp(127.0.0.1:3306)/' go test ./internal/source/mysql -run Integration -v
//
// It reads information_schema.TABLES, which every server has.
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("DBMETA_TEST_DSN")
	if dsn == "" {
		t.Skip("DBMETA_TEST_DSN not set")
	}
	c, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	inst := &config.Instance{Host: c.Addr, User: c.User, Password: c.Passwd, TLS: "preferred"}
	if h, p, ok := strings.Cut(c.Addr, ":"); ok {
		inst.Host = h
		if n, err := parsePort(p); err == nil {
			inst.Port = n
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := Open(ctx, inst, Options{Warn: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := src.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	ts, err := src.FetchTable(ctx, "information_schema", "TABLES")
	if err != nil {
		t.Fatal(err)
	}
	if ts.ServerVersion == "" || len(ts.Columns) == 0 || ts.DDL == "" {
		t.Errorf("%+v", ts)
	}
	if _, err := src.FetchTable(ctx, "information_schema", "NO_SUCH_TABLE_XYZ"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func parsePort(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("bad port")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
