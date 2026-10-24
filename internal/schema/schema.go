// Package schema is the table-structure model plus its Markdown and JSON forms.
// The Markdown form is what lands in the cache, so Render and Parse must round-trip.
package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
)

type Column struct {
	Ordinal  int     `json:"ordinal"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"` // nil is SQL NULL
	Key      string  `json:"key"`
	Extra    string  `json:"extra"`
	Comment  string  `json:"comment"`
}

type Index struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"` // in index order; prefix lengths appear as "name(10)"
	Type    string   `json:"type"`
	Comment string   `json:"comment"`
}

type TableSchema struct {
	Instance      string `json:"instance"`
	Database      string `json:"database"`
	Table         string `json:"table"`
	Server        string `json:"server"`
	ServerVersion string `json:"server_version"`

	Comment      string `json:"comment"`
	Engine       string `json:"engine"`
	Charset      string `json:"charset"`
	RowsEstimate int64  `json:"rows_estimate"`

	Columns []Column `json:"columns"`
	Indexes []Index  `json:"indexes"`
	DDL     string   `json:"ddl"`

	FetchedAt  time.Time  `json:"fetched_at"`
	SchemaHash string     `json:"schema_hash"`
	Stale      bool       `json:"stale"`
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
}

// AUTO_INCREMENT=N in SHOW CREATE TABLE moves with every insert, so it must
// not count as a structure change.
var autoIncRe = regexp.MustCompile(`\s+AUTO_INCREMENT=\d+`)

// ComputeHash fingerprints columns, indexes and DDL. Row estimates, fetch
// metadata and the auto-increment counter are excluded.
func (t *TableSchema) ComputeHash() string {
	payload := struct {
		Columns []Column `json:"c"`
		Indexes []Index  `json:"i"`
		DDL     string   `json:"d"`
	}{t.Columns, t.Indexes, autoIncRe.ReplaceAllString(t.DDL, "")}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// JSON renders the structured form used by --format json.
func (t *TableSchema) JSON() ([]byte, error) {
	cp := *t
	if cp.Columns == nil {
		cp.Columns = []Column{}
	}
	if cp.Indexes == nil {
		cp.Indexes = []Index{}
	}
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
