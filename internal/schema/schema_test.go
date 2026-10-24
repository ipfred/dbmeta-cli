package schema

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sp(s string) *string { return &s }

func sample() *TableSchema {
	ts := &TableSchema{
		Instance: "order_prod", Database: "order", Table: "t_order",
		Server: "10.0.0.12:3306", ServerVersion: "8.0.32",
		Comment: "订单主表", Engine: "InnoDB", Charset: "utf8mb4", RowsEstimate: 12300000,
		Columns: []Column{
			{Ordinal: 1, Name: "id", Type: "bigint unsigned", Nullable: false, Default: nil, Key: "PRI", Extra: "auto_increment", Comment: "主键"},
			{Ordinal: 2, Name: "status", Type: "tinyint", Nullable: false, Default: sp("0"), Key: "MUL", Comment: "0=待付款|1=已付款"},
			{Ordinal: 3, Name: "remark", Type: "varchar(255)", Nullable: true, Default: sp(""), Comment: "备注\n第二行"},
			{Ordinal: 4, Name: "created_at", Type: "datetime", Nullable: false, Default: sp("CURRENT_TIMESTAMP"), Extra: "DEFAULT_GENERATED"},
		},
		Indexes: []Index{
			{Name: "PRIMARY", Unique: true, Columns: []string{"id"}, Type: "BTREE"},
			{Name: "idx_status_created", Unique: false, Columns: []string{"status", "created_at"}, Type: "BTREE", Comment: "状态+时间"},
		},
		DDL:       "CREATE TABLE `t_order` (\n  `id` bigint unsigned NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=100 DEFAULT CHARSET=utf8mb4",
		FetchedAt: time.Date(2026, 10, 24, 17, 50, 0, 0, time.FixedZone("", 8*3600)),
		Stale:     false,
	}
	ts.SchemaHash = ts.ComputeHash()
	return ts
}

func TestGolden(t *testing.T) {
	got := sample().Markdown()
	const path = "testdata/t_order.golden.md"
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("markdown differs from golden file (run with -update to refresh):\n%s", got)
	}
}

func TestRoundTrip(t *testing.T) {
	variants := map[string]func(*TableSchema){
		"plain":        func(*TableSchema) {},
		"stale+change": func(ts *TableSchema) { ts.Stale = true; c := ts.FetchedAt.Add(time.Hour); ts.ChangedAt = &c },
		"no comment":   func(ts *TableSchema) { ts.Comment = "" },
		"weird comment": func(ts *TableSchema) {
			ts.Comment = "line1\nline2 | \"quoted\" \\ back"
		},
		"view": func(ts *TableSchema) { ts.Engine = ""; ts.Charset = ""; ts.RowsEstimate = -1 },
		"tricky defaults": func(ts *TableSchema) {
			ts.Columns = []Column{
				{Ordinal: 1, Name: "a", Type: "varchar(8)", Default: sp("NULL")},
				{Ordinal: 2, Name: "b", Type: "varchar(8)", Default: sp("'x'")},
				{Ordinal: 3, Name: "c", Type: "varchar(8)", Default: sp("it's")},
				{Ordinal: 4, Name: "d", Type: "varchar(8)", Default: nil},
				{Ordinal: 5, Name: "e", Type: "varchar(8)", Default: sp(`a\|b`), Comment: `\n literal`},
				{Ordinal: 6, Name: "f", Type: "varchar(8)", Default: sp(" padded "), Comment: " lead and trail "},
			}
		},
		"ddl with fences": func(ts *TableSchema) { ts.DDL = "CREATE VIEW v AS SELECT '```' AS x\n-- ````\n" },
		"empty ddl":       func(ts *TableSchema) { ts.DDL = "" },
		"no cols":         func(ts *TableSchema) { ts.Columns = []Column{}; ts.Indexes = []Index{} },
	}
	for name, mut := range variants {
		t.Run(name, func(t *testing.T) {
			ts := sample()
			mut(ts)
			ts.SchemaHash = ts.ComputeHash()
			md := ts.Markdown()
			back, err := ParseMarkdown(md)
			if err != nil {
				t.Fatalf("%v\n%s", err, md)
			}
			if !back.FetchedAt.Equal(ts.FetchedAt) {
				t.Errorf("fetched_at %v != %v", back.FetchedAt, ts.FetchedAt)
			}
			back.FetchedAt = ts.FetchedAt
			if ts.ChangedAt != nil {
				if back.ChangedAt == nil || !back.ChangedAt.Equal(*ts.ChangedAt) {
					t.Errorf("changed_at lost")
				}
				back.ChangedAt = ts.ChangedAt
			}
			if !reflect.DeepEqual(back, ts) {
				bj, _ := json.MarshalIndent(back, "", " ")
				tj, _ := json.MarshalIndent(ts, "", " ")
				t.Errorf("round trip mismatch\n got: %s\nwant: %s", bj, tj)
			}
			if back.ComputeHash() != ts.SchemaHash {
				t.Error("hash changed across round trip")
			}
		})
	}
}

func TestHash(t *testing.T) {
	a := sample()
	h := a.ComputeHash()

	b := sample()
	b.DDL = strings.Replace(b.DDL, "AUTO_INCREMENT=100", "AUTO_INCREMENT=99999", 1)
	b.RowsEstimate = 1
	b.FetchedAt = b.FetchedAt.Add(time.Hour)
	if b.ComputeHash() != h {
		t.Error("auto_increment counter, rows and time must not affect the hash")
	}

	c := sample()
	c.Columns[1].Type = "smallint"
	if c.ComputeHash() == h {
		t.Error("column change must alter the hash")
	}
	d := sample()
	d.Indexes[1].Columns = []string{"created_at", "status"}
	if d.ComputeHash() == h {
		t.Error("index change must alter the hash")
	}
	e := sample()
	e.DDL += " COMMENT='x'"
	if e.ComputeHash() == h {
		t.Error("ddl change must alter the hash")
	}
}

func TestParseErrors(t *testing.T) {
	good := sample().Markdown()
	bad := map[string]string{
		"empty":         "",
		"no front":      "# hello",
		"unclosed":      "---\ninstance: a\n",
		"no table name": strings.Replace(good, "table: t_order", "table: ", 1),
		"bad time":      strings.Replace(good, "fetched_at: 2026-10-24T17:50:00+08:00", "fetched_at: yesterday", 1),
		"bad header":    strings.Replace(good, colHeader, "| x |", 1),
		"short row":     strings.Replace(good, "| 1 | id |", "| 1 |", 1),
	}
	for name, src := range bad {
		if _, err := ParseMarkdown(src); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestJSON(t *testing.T) {
	b, err := sample().JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"instance", "columns", "indexes", "ddl", "schema_hash", "fetched_at", "stale"} {
		if _, ok := m[k]; !ok {
			t.Errorf("json lacks %q", k)
		}
	}
	if _, ok := m["changed_at"]; ok {
		t.Error("changed_at should be omitted when nil")
	}
	cols := m["columns"].([]any)
	if cols[0].(map[string]any)["default"] != nil {
		t.Error("NULL default should be JSON null")
	}
	empty := &TableSchema{}
	b, _ = empty.JSON()
	if !strings.Contains(string(b), `"columns": []`) {
		t.Errorf("nil columns should serialize as []: %s", b)
	}
}
