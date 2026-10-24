package mysql

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	gomysql "github.com/go-sql-driver/mysql"

	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/source"
)

// newMock uses exact matching: any statement outside the expected list fails the test.
func newMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	return db, mock
}

func expectSession(mock sqlmock.Sqlmock) {
	mock.ExpectExec(sqlReadOnly).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sqlMaxExecMS).WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestFetchTableRunsOnlyAllowlistedSQL(t *testing.T) {
	db, mock := newMock(t)
	expectSession(mock)
	mock.ExpectQuery(sqlVersion).WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow("8.0.32"))
	mock.ExpectQuery(sqlTables).WithArgs("order", "t_order").
		WillReturnRows(sqlmock.NewRows([]string{"c", "e", "col", "r"}).AddRow("订单主表", "InnoDB", "utf8mb4_0900_ai_ci", int64(1234)))
	mock.ExpectQuery(sqlColumns).WithArgs("order", "t_order").
		WillReturnRows(sqlmock.NewRows([]string{"o", "n", "t", "nul", "d", "k", "e", "c"}).
			AddRow(int64(1), "id", "bigint", "NO", nil, "PRI", "auto_increment", "主键").
			AddRow(int64(2), "name", "varchar(32)", "YES", "x", "", "", ""))
	mock.ExpectQuery(sqlIndexes).WithArgs("order", "t_order").
		WillReturnRows(sqlmock.NewRows([]string{"n", "nu", "seq", "col", "sub", "type", "cm"}).
			AddRow("idx_name", int64(1), int64(1), "name", int64(10), "BTREE", "").
			AddRow("PRIMARY", int64(0), int64(1), "id", nil, "BTREE", ""))
	mock.ExpectQuery("SHOW CREATE TABLE `order`.`t_order`").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("t_order", "CREATE TABLE `t_order` (...)"))
	mock.ExpectClose()

	src, err := fromDB(context.Background(), db, "h:3306", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := src.FetchTable(context.Background(), "order", "t_order")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	if ts.ServerVersion != "8.0.32" || ts.Comment != "订单主表" || ts.Engine != "InnoDB" || ts.Charset != "utf8mb4" || ts.RowsEstimate != 1234 {
		t.Errorf("table meta wrong: %+v", ts)
	}
	if len(ts.Columns) != 2 || ts.Columns[0].Default != nil || ts.Columns[0].Nullable ||
		ts.Columns[1].Default == nil || *ts.Columns[1].Default != "x" || !ts.Columns[1].Nullable {
		t.Errorf("columns wrong: %+v", ts.Columns)
	}
	if len(ts.Indexes) != 2 || ts.Indexes[0].Name != "PRIMARY" || !ts.Indexes[0].Unique || ts.Indexes[1].Columns[0] != "name(10)" || ts.Indexes[1].Unique {
		t.Errorf("indexes wrong (PRIMARY must come first): %+v", ts.Indexes)
	}
	if ts.DDL != "CREATE TABLE `t_order` (...)" {
		t.Errorf("ddl = %q", ts.DDL)
	}
}

func TestViewShowCreateHasFourColumns(t *testing.T) {
	db, mock := newMock(t)
	expectSession(mock)
	mock.ExpectQuery(sqlVersion).WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow("8.0.32"))
	mock.ExpectQuery(sqlTables).WithArgs("d", "v1").
		WillReturnRows(sqlmock.NewRows([]string{"c", "e", "col", "r"}).AddRow("VIEW", nil, nil, nil))
	mock.ExpectQuery(sqlColumns).WithArgs("d", "v1").WillReturnRows(sqlmock.NewRows([]string{"o", "n", "t", "nul", "d", "k", "e", "c"}))
	mock.ExpectQuery(sqlIndexes).WithArgs("d", "v1").WillReturnRows(sqlmock.NewRows([]string{"n", "nu", "seq", "col", "sub", "type", "cm"}))
	mock.ExpectQuery("SHOW CREATE TABLE `d`.`v1`").
		WillReturnRows(sqlmock.NewRows([]string{"View", "Create View", "character_set_client", "collation_connection"}).AddRow("v1", "CREATE VIEW v1 AS SELECT 1", "utf8mb4", "x"))

	src, _ := fromDB(context.Background(), db, "h:3306", Options{})
	ts, err := src.FetchTable(context.Background(), "d", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if ts.DDL != "CREATE VIEW v1 AS SELECT 1" || ts.RowsEstimate != -1 || ts.Engine != "" {
		t.Errorf("%+v", ts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaliciousIdentifiersNeverReachTheServer(t *testing.T) {
	db, mock := newMock(t)
	expectSession(mock) // only the session setup is allowed; any further statement fails
	src, err := fromDB(context.Background(), db, "h:3306", Options{})
	if err != nil {
		t.Fatal(err)
	}
	bad := [][2]string{
		{"order", "t`; DROP TABLE x; --"}, {"order", "%"}, {"order", "a.b"}, {"order", ".."}, {"ord`er", "t"},
		{"*", "t"}, {"order", ""}, {"order", "t order"}, {"", "t"}, {"order", "t'"}, {"a/b", "t"},
	}
	for _, p := range bad {
		_, err := src.FetchTable(context.Background(), p[0], p[1])
		if !errors.Is(err, guard.ErrInvalid) {
			t.Errorf("%q.%q: want ErrInvalid, got %v", p[0], p[1], err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTableNotFoundDoesNotProbeFurther(t *testing.T) {
	db, mock := newMock(t)
	expectSession(mock)
	mock.ExpectQuery(sqlVersion).WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow("8.0.32"))
	mock.ExpectQuery(sqlTables).WithArgs("order", "nope").
		WillReturnRows(sqlmock.NewRows([]string{"c", "e", "col", "r"}))
	src, _ := fromDB(context.Background(), db, "h:3306", Options{})
	_, err := src.FetchTable(context.Background(), "order", "nope")
	if !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// No SHOW TABLES / LIKE suggestions: the mock would have rejected them.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionHardeningFallbacks(t *testing.T) {
	db, mock := newMock(t)
	mock.ExpectExec(sqlReadOnly).WillReturnError(errors.New("noop only"))
	mock.ExpectExec(sqlMaxExecMS).WillReturnError(&gomysql.MySQLError{Number: 1193, Message: "Unknown system variable"})
	mock.ExpectExec(sqlMaxStatementS).WillReturnResult(sqlmock.NewResult(0, 0))
	var warnings []string
	_, err := fromDB(context.Background(), db, "h:3306", Options{Warn: func(f string, a ...any) { warnings = append(warnings, f) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "只读") {
		t.Errorf("warnings = %v", warnings)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPing(t *testing.T) {
	db, mock := newMock(t)
	expectSession(mock)
	mock.ExpectQuery(sqlPing).WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	src, _ := fromDB(context.Background(), db, "h:3306", Options{})
	if err := src.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{&gomysql.MySQLError{Number: 1045, Message: "Access denied"}, source.ErrAuth},
		{&gomysql.MySQLError{Number: 1142, Message: "SELECT command denied"}, source.ErrNotFound},
		{&gomysql.MySQLError{Number: 1049, Message: "Unknown database"}, source.ErrNotFound},
		{context.DeadlineExceeded, source.ErrConn},
		{gomysql.ErrInvalidConn, source.ErrConn},
		{sql.ErrConnDone, source.ErrConn},
	}
	for _, c := range cases {
		if got := classify(c.err); !errors.Is(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.err, got, c.want)
		}
	}
	other := &gomysql.MySQLError{Number: 1064, Message: "syntax"}
	if got := classify(other); errors.Is(got, source.ErrAuth) || errors.Is(got, source.ErrConn) {
		t.Errorf("unrelated error misclassified: %v", got)
	}
}

// Static guard: every statement this package sends is one of the named constants.
func TestEveryCallSiteUsesAnAllowlistedConstant(t *testing.T) {
	src, err := os.ReadFile("mysql.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"sqlPing": true, "sqlVersion": true, "sqlTables": true, "sqlColumns": true, "sqlIndexes": true,
		"sqlReadOnly": true, "sqlMaxExecMS": true, "sqlMaxStatementS": true, "fmt.Sprintf(sqlShowCreate": true,
	}
	re := regexp.MustCompile(`\.(?:Query|QueryRow|Exec|Prepare)Context\(\s*ctx,\s*([^,)]+)`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 9 {
		t.Fatalf("found only %d call sites; the regexp is probably stale", len(matches))
	}
	for _, m := range matches {
		if !allowed[strings.TrimSpace(m[1])] {
			t.Errorf("call site uses non-allowlisted SQL: %s", m[1])
		}
	}
	if strings.Contains(string(src), ".Exec(") || strings.Contains(string(src), ".Query(") || strings.Contains(string(src), ".QueryRow(") {
		t.Error("context-free query calls are not allowed")
	}
}
