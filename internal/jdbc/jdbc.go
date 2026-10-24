// Package jdbc turns a MySQL/MariaDB JDBC URL into connection fields. It is a
// pure function: no network, no files. Passwords found in the URL are dropped.
package jdbc

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Result struct {
	Host     string
	Port     int    // 3306 when the URL has none
	User     string // may be empty
	TLS      string // "", "false", "preferred", "true"; "" means the URL did not say
	Database string // default database from the URL path; informational only
	// Ignored lists parameter names that were not mapped. Values are never reported.
	Ignored []string
	// Notes are human-readable remarks (multi-host, password dropped, ...).
	Notes []string
}

var (
	schemeRe = regexp.MustCompile(`(?i)^jdbc:(mysql|mariadb)(?::(loadbalance|replication|sequential|aurora|failover))?:`)
	pairRe   = regexp.MustCompile(`\(\s*([A-Za-z_]+)\s*=\s*([^)]*)\)`)
)

func Parse(raw string) (*Result, error) {
	raw = strings.TrimSpace(raw)
	m := schemeRe.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("不支持的 JDBC 链接,目前只支持 jdbc:mysql: 和 jdbc:mariadb:")
	}
	rest := raw[len(m[0]):]
	if !strings.HasPrefix(rest, "//") {
		return nil, fmt.Errorf("JDBC 链接缺少 //host 部分")
	}
	rest = rest[2:]

	authority, path, query := split(rest)
	r := &Result{Port: 3306}

	hosts := splitTop(authority, ',')
	if len(hosts) == 0 || strings.TrimSpace(hosts[0]) == "" {
		return nil, fmt.Errorf("JDBC 链接缺少 host")
	}
	if len(hosts) > 1 {
		r.Notes = append(r.Notes, "检测到多主机,已使用第一个")
	}

	var userinfoUser string
	var kvUser string
	passwordSeen := false
	first := strings.TrimSpace(hosts[0])

	switch {
	case strings.HasPrefix(strings.ToLower(first), "address="):
		kv := parsePairs(first)
		r.Host, r.Port = kv["host"], portOr(kv["port"], 3306)
		if _, err := checkPort(kv["port"]); err != nil {
			return nil, err
		}
	case strings.HasPrefix(first, "("):
		kv := parsePairsComma(first)
		r.Host, r.Port = kv["host"], portOr(kv["port"], 3306)
		if _, err := checkPort(kv["port"]); err != nil {
			return nil, err
		}
		kvUser = kv["user"]
		if _, ok := kv["password"]; ok {
			passwordSeen = true
		}
	default:
		if at := strings.LastIndex(first, "@"); at >= 0 {
			ui := first[:at]
			first = first[at+1:]
			u, p, hasP := strings.Cut(ui, ":")
			userinfoUser = unescape(u)
			passwordSeen = passwordSeen || hasP && p != ""
		}
		host, port, err := splitHostPort(first)
		if err != nil {
			return nil, err
		}
		r.Host = host
		if port != "" {
			n, err := checkPort(port)
			if err != nil {
				return nil, err
			}
			r.Port = n
		}
	}
	if r.Host == "" {
		return nil, fmt.Errorf("JDBC 链接缺少 host")
	}

	r.Database = unescape(strings.TrimPrefix(path, "/"))

	var queryUser, sslMode, useSSL string
	seen := map[string]bool{}
	for _, kv := range strings.Split(query, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		k, v = unescape(k), unescape(v)
		switch strings.ToLower(k) {
		case "user":
			queryUser = v
		case "password":
			passwordSeen = true
		case "sslmode":
			sslMode = strings.ToUpper(v)
		case "usessl":
			useSSL = strings.ToLower(v)
		default:
			if !seen[k] {
				seen[k] = true
				r.Ignored = append(r.Ignored, k)
			}
		}
	}

	switch {
	case queryUser != "":
		r.User = queryUser
	case kvUser != "":
		r.User = kvUser
	default:
		r.User = userinfoUser
	}
	if passwordSeen {
		r.Notes = append(r.Notes, "JDBC 链接中带有密码,已忽略,未写入配置")
	}

	// sslMode wins over the older useSSL flag, matching Connector/J.
	switch sslMode {
	case "DISABLED":
		r.TLS = "false"
	case "PREFERRED":
		r.TLS = "preferred"
	case "REQUIRED", "VERIFY_CA", "VERIFY_IDENTITY":
		r.TLS = "true"
	case "":
		switch useSSL {
		case "false":
			r.TLS = "false"
		case "true":
			r.TLS = "true"
		case "":
		default:
			r.Ignored = append(r.Ignored, "useSSL")
		}
	default:
		r.Ignored = append(r.Ignored, "sslMode")
	}
	return r, nil
}

// split cuts "authority/path?query" without being fooled by parentheses in the authority.
func split(s string) (authority, path, query string) {
	depth := 0
	cut := len(s)
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '/', '?':
			if depth == 0 {
				cut = i
			}
		}
		if cut != len(s) {
			break
		}
	}
	authority, s = s[:cut], s[cut:]
	if strings.HasPrefix(s, "/") {
		path, s, _ = strings.Cut(s, "?")
	}
	query = strings.TrimPrefix(s, "?")
	return
}

// splitTop splits on sep outside parentheses.
func splitTop(s string, sep rune) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch {
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + len(string(c))
		}
	}
	return append(out, s[start:])
}

func parsePairs(s string) map[string]string {
	kv := map[string]string{}
	for _, m := range pairRe.FindAllStringSubmatch(s, -1) {
		kv[strings.ToLower(m[1])] = strings.TrimSpace(m[2])
	}
	return kv
}

// parsePairsComma handles "(host=h,port=3306,user=u)".
func parsePairsComma(s string) map[string]string {
	kv := map[string]string{}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			kv[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return kv
}

func splitHostPort(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", fmt.Errorf("IPv6 地址缺少 ]")
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest == "" {
			return host, "", nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("无法解析 host: %q", s)
		}
		return host, rest[1:], nil
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:], nil
	}
	return s, "", nil
}

func checkPort(s string) (int, error) {
	if s == "" {
		return 3306, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("无效的端口 %q", s)
	}
	return n, nil
}

func portOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func unescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}
