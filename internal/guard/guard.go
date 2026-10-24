// Package guard validates every user-supplied identifier before it can reach
// a SQL statement or a file path, and enforces the per-instance database allowlist.
package guard

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalid means an identifier failed validation (exit code 2).
	ErrInvalid = errors.New("名称无效")
	// ErrNotAllowed means the database is outside allow_databases (exit code 7).
	ErrNotAllowed = errors.New("库不在白名单内")
)

var (
	identRe    = regexp.MustCompile(`^[\p{L}\p{N}_$-]{1,64}$`)
	instanceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Ident checks a database or table name. Wildcards, quotes, backticks, dots,
// whitespace and path separators are all rejected, which also keeps cache
// paths from escaping their directory.
func Ident(kind, s string) error {
	if !identRe.MatchString(s) {
		return fmt.Errorf("%w: %s %q 只允许字母、数字、下划线、$ 和连字符(最长 64 个字符)", ErrInvalid, kind, s)
	}
	return nil
}

// InstanceName checks a name used as a config key and cache directory.
func InstanceName(s string) error {
	if !instanceRe.MatchString(s) {
		return fmt.Errorf("%w: 实例名 %q 只允许字母、数字、下划线和连字符(最长 64 个字符)", ErrInvalid, s)
	}
	return nil
}

// Database applies the allowlist. An empty list means no restriction.
func Database(allow []string, db string) error {
	if len(allow) == 0 {
		return nil
	}
	for _, a := range allow {
		if a == db {
			return nil
		}
	}
	return fmt.Errorf("%w: 库 %q 不在 allow_databases 内", ErrNotAllowed, db)
}
