package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrParse means a cache file is not a valid dbmeta Markdown document.
var ErrParse = errors.New("无法解析表结构文件")

const (
	colHeader = "| # | 字段 | 类型 | 可空 | 默认值 | 键 | 额外 | 注释 |"
	idxHeader = "| 名称 | 唯一 | 字段(按顺序) | 类型 | 注释 |"
)

// Markdown renders the cache/stdout form.
func (t *TableSchema) Markdown() string {
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	line("---")
	line("instance: %s", t.Instance)
	line("database: %s", t.Database)
	line("table: %s", t.Table)
	line("server: %s", t.Server)
	line("server_version: %s", t.ServerVersion)
	line("engine: %s", t.Engine)
	line("charset: %s", t.Charset)
	line("rows_estimate: %d", t.RowsEstimate)
	line("comment: %s", jsonString(t.Comment))
	line("fetched_at: %s", t.FetchedAt.Format(time.RFC3339))
	line("schema_hash: %s", t.SchemaHash)
	line("stale: %t", t.Stale)
	if t.ChangedAt != nil {
		line("changed_at: %s", t.ChangedAt.Format(time.RFC3339))
	}
	line("---")
	line("# %s / %s / %s", t.Instance, t.Database, t.Table)
	line("")

	var facts []string
	if c := oneLine(t.Comment); c != "" {
		facts = append(facts, c+" ")
	}
	if t.Engine != "" {
		facts = append(facts, "引擎: "+t.Engine)
	}
	if t.Charset != "" {
		facts = append(facts, "字符集: "+t.Charset)
	}
	if t.RowsEstimate >= 0 {
		facts = append(facts, "估算行数: ~"+groupDigits(t.RowsEstimate)+"(估算值)")
	}
	if len(facts) > 0 {
		line("> %s", strings.Join(facts, " | "))
		line("")
	}

	line("## 字段")
	line(colHeader)
	line("|---|---|---|---|---|---|---|---|")
	for _, c := range t.Columns {
		line("%s", row(strconv.Itoa(c.Ordinal), c.Name, c.Type, yesNo(c.Nullable), encodeDefault(c.Default), c.Key, c.Extra, c.Comment))
	}
	line("")

	line("## 索引")
	line(idxHeader)
	line("|---|---|---|---|---|")
	for _, i := range t.Indexes {
		line("%s", row(i.Name, yesNo(i.Unique), strings.Join(i.Columns, ", "), i.Type, i.Comment))
	}
	line("")

	fence := fenceFor(t.DDL)
	line("## DDL")
	line("%ssql", fence)
	b.WriteString(t.DDL)
	b.WriteString("\n")
	line("%s", fence)
	return b.String()
}

// ParseMarkdown reads what Markdown wrote.
func ParseMarkdown(src string) (*TableSchema, error) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, fmt.Errorf("%w: 缺少 front matter", ErrParse)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("%w: front matter 未结束", ErrParse)
	}

	fm := map[string]string{}
	for _, l := range lines[1:end] {
		k, v, ok := strings.Cut(l, ": ")
		if !ok {
			k, v, _ = strings.Cut(l, ":")
		}
		fm[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}

	t := &TableSchema{
		Instance:      fm["instance"],
		Database:      fm["database"],
		Table:         fm["table"],
		Server:        fm["server"],
		ServerVersion: fm["server_version"],
		Engine:        fm["engine"],
		Charset:       fm["charset"],
		SchemaHash:    fm["schema_hash"],
		Stale:         fm["stale"] == "true",
		RowsEstimate:  -1,
	}
	if t.Instance == "" || t.Database == "" || t.Table == "" {
		return nil, fmt.Errorf("%w: front matter 缺少 instance/database/table", ErrParse)
	}
	var err error
	if t.FetchedAt, err = time.Parse(time.RFC3339, fm["fetched_at"]); err != nil {
		return nil, fmt.Errorf("%w: fetched_at 无效", ErrParse)
	}
	if v, ok := fm["changed_at"]; ok && v != "" {
		ct, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("%w: changed_at 无效", ErrParse)
		}
		t.ChangedAt = &ct
	}
	if v, ok := fm["rows_estimate"]; ok {
		if t.RowsEstimate, err = strconv.ParseInt(v, 10, 64); err != nil {
			return nil, fmt.Errorf("%w: rows_estimate 无效", ErrParse)
		}
	}
	if v, ok := fm["comment"]; ok {
		if t.Comment, err = unJSONString(v); err != nil {
			return nil, fmt.Errorf("%w: comment 无效", ErrParse)
		}
	}

	body := lines[end+1:]
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case "## 字段":
			rows, next, err := readTable(body, i+1, colHeader, 8)
			if err != nil {
				return nil, err
			}
			i = next - 1
			for _, r := range rows {
				n, err := strconv.Atoi(r[0])
				if err != nil {
					return nil, fmt.Errorf("%w: 字段序号 %q 无效", ErrParse, r[0])
				}
				def, err := decodeDefault(r[4])
				if err != nil {
					return nil, err
				}
				t.Columns = append(t.Columns, Column{Ordinal: n, Name: r[1], Type: r[2], Nullable: r[3] == "YES", Default: def, Key: r[5], Extra: r[6], Comment: r[7]})
			}
		case "## 索引":
			rows, next, err := readTable(body, i+1, idxHeader, 5)
			if err != nil {
				return nil, err
			}
			i = next - 1
			for _, r := range rows {
				var cols []string
				if r[2] != "" {
					cols = strings.Split(r[2], ", ")
				}
				t.Indexes = append(t.Indexes, Index{Name: r[0], Unique: r[1] == "YES", Columns: cols, Type: r[3], Comment: r[4]})
			}
		case "## DDL":
			if i+1 >= len(body) || !strings.HasSuffix(body[i+1], "sql") || !strings.HasPrefix(body[i+1], "```") {
				return nil, fmt.Errorf("%w: DDL 代码块缺失", ErrParse)
			}
			fence := strings.TrimSuffix(body[i+1], "sql")
			closeAt := -1
			for j := i + 2; j < len(body); j++ {
				if body[j] == fence {
					closeAt = j
				}
			}
			if closeAt < 0 {
				return nil, fmt.Errorf("%w: DDL 代码块未结束", ErrParse)
			}
			t.DDL = strings.Join(body[i+2:closeAt], "\n")
			i = closeAt
		}
	}
	if t.Columns == nil {
		t.Columns = []Column{}
	}
	if t.Indexes == nil {
		t.Indexes = []Index{}
	}
	return t, nil
}

// readTable reads the header, separator and rows following a section title.
func readTable(body []string, start int, header string, ncols int) (rows [][]string, next int, err error) {
	if start+1 >= len(body) || body[start] != header {
		return nil, 0, fmt.Errorf("%w: 表头不符合预期", ErrParse)
	}
	i := start + 2
	for ; i < len(body) && strings.HasPrefix(body[i], "|"); i++ {
		cells, err := splitRow(body[i])
		if err != nil {
			return nil, 0, err
		}
		if len(cells) != ncols {
			return nil, 0, fmt.Errorf("%w: 行 %q 列数应为 %d", ErrParse, body[i], ncols)
		}
		rows = append(rows, cells)
	}
	return rows, i, nil
}

func row(cells ...string) string {
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cells {
		b.WriteString(" " + escapeCell(c) + " |")
	}
	return b.String()
}

func escapeCell(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `|`, `\|`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}

// splitRow splits on unescaped pipes and removes the single padding space on each side.
func splitRow(line string) ([]string, error) {
	if len(line) < 2 || line[0] != '|' || line[len(line)-1] != '|' {
		return nil, fmt.Errorf("%w: 表格行格式错误 %q", ErrParse, line)
	}
	inner := line[1 : len(line)-1]
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '\\' && i+1 < len(inner) {
			i++
			switch inner[i] {
			case 'n':
				cur.WriteByte('\n')
			case 'r':
				cur.WriteByte('\r')
			default: // \\ and \|
				cur.WriteByte(inner[i])
			}
			continue
		}
		if c == '|' {
			cells = append(cells, trimPad(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	cells = append(cells, trimPad(cur.String()))
	return cells, nil
}

func trimPad(s string) string {
	s = strings.TrimPrefix(s, " ")
	return strings.TrimSuffix(s, " ")
}

// Defaults: NULL stays bare; real strings that could be mistaken for it, or are
// empty, get single quotes.
func encodeDefault(d *string) string {
	if d == nil {
		return "NULL"
	}
	if *d == "" || *d == "NULL" || strings.HasPrefix(*d, "'") {
		return "'" + strings.ReplaceAll(*d, "'", "''") + "'"
	}
	return *d
}

func decodeDefault(s string) (*string, error) {
	if s == "NULL" {
		return nil, nil
	}
	if strings.HasPrefix(s, "'") {
		if len(s) < 2 || !strings.HasSuffix(s, "'") {
			return nil, fmt.Errorf("%w: 默认值 %q 引号不匹配", ErrParse, s)
		}
		v := strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		return &v, nil
	}
	return &s, nil
}

func yesNo(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func unJSONString(s string) (string, error) {
	var out string
	err := json.Unmarshal([]byte(s), &out)
	return out, err
}

func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.TrimSpace(s)
}

// fenceFor returns a backtick fence longer than any run inside the DDL.
func fenceFor(ddl string) string {
	longest, run := 0, 0
	for _, r := range ddl {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := 3
	if longest >= n {
		n = longest + 1
	}
	return strings.Repeat("`", n)
}

func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
