package jdbc

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		host    string
		port    int
		user    string
		tls     string
		db      string
		ignored []string
		notes   []string // substrings expected in Notes
	}{
		{name: "plain", in: "jdbc:mysql://10.0.0.12:3306/order?user=readonly&useSSL=false",
			host: "10.0.0.12", port: 3306, user: "readonly", tls: "false", db: "order"},
		{name: "default port", in: "jdbc:mysql://db.local/shop", host: "db.local", port: 3306, db: "shop"},
		{name: "no db", in: "jdbc:mysql://db.local:3307", host: "db.local", port: 3307},
		{name: "mariadb", in: "jdbc:mariadb://h:3310/x", host: "h", port: 3310, db: "x"},
		{name: "userinfo", in: "jdbc:mysql://alice@h:3306/db", host: "h", port: 3306, user: "alice", db: "db"},
		{name: "userinfo with password", in: "jdbc:mysql://alice:s3cret@h:3306/db", host: "h", port: 3306, user: "alice", db: "db",
			notes: []string{"密码"}},
		{name: "query password", in: "jdbc:mysql://h/db?user=u&password=s3cret", host: "h", port: 3306, user: "u", db: "db",
			notes: []string{"密码"}},
		{name: "query user beats userinfo", in: "jdbc:mysql://a@h/db?user=b", host: "h", port: 3306, user: "b", db: "db"},
		{name: "encoded user", in: "jdbc:mysql://h/db?user=a%40b", host: "h", port: 3306, user: "a@b", db: "db"},
		{name: "loadbalance multi host", in: "jdbc:mysql:loadbalance://h1:3306,h2:3307/db?user=u",
			host: "h1", port: 3306, user: "u", db: "db", notes: []string{"多主机"}},
		{name: "replication", in: "jdbc:mysql:replication://m:3306,s1:3306,s2/db", host: "m", port: 3306, db: "db", notes: []string{"多主机"}},
		{name: "address form", in: "jdbc:mysql://address=(host=h1)(port=3307),address=(host=h2)(port=3308)/db",
			host: "h1", port: 3307, db: "db", notes: []string{"多主机"}},
		{name: "address form default port", in: "jdbc:mysql://address=(host=h1)/db", host: "h1", port: 3306, db: "db"},
		{name: "key-value form", in: "jdbc:mysql://(host=h1,port=3308,user=kv,password=x)/db",
			host: "h1", port: 3308, user: "kv", db: "db", notes: []string{"密码"}},
		{name: "ipv6", in: "jdbc:mysql://[::1]:3307/db", host: "::1", port: 3307, db: "db"},
		{name: "ipv6 default port", in: "jdbc:mysql://[fe80::1]/db", host: "fe80::1", port: 3306, db: "db"},
		{name: "case insensitive scheme", in: "JDBC:MYSQL://h/db", host: "h", port: 3306, db: "db"},
		{name: "trims space", in: "  jdbc:mysql://h/db \n", host: "h", port: 3306, db: "db"},
		{name: "sslMode DISABLED", in: "jdbc:mysql://h/d?sslMode=DISABLED", host: "h", port: 3306, db: "d", tls: "false"},
		{name: "sslMode PREFERRED", in: "jdbc:mysql://h/d?sslMode=preferred", host: "h", port: 3306, db: "d", tls: "preferred"},
		{name: "sslMode REQUIRED", in: "jdbc:mysql://h/d?sslMode=REQUIRED", host: "h", port: 3306, db: "d", tls: "true"},
		{name: "sslMode VERIFY_CA", in: "jdbc:mysql://h/d?sslMode=VERIFY_CA", host: "h", port: 3306, db: "d", tls: "true"},
		{name: "sslMode VERIFY_IDENTITY", in: "jdbc:mysql://h/d?sslMode=VERIFY_IDENTITY", host: "h", port: 3306, db: "d", tls: "true"},
		{name: "useSSL true", in: "jdbc:mysql://h/d?useSSL=true", host: "h", port: 3306, db: "d", tls: "true"},
		{name: "sslMode beats useSSL", in: "jdbc:mysql://h/d?useSSL=false&sslMode=REQUIRED", host: "h", port: 3306, db: "d", tls: "true"},
		{name: "unknown sslMode ignored", in: "jdbc:mysql://h/d?sslMode=WEIRD", host: "h", port: 3306, db: "d", ignored: []string{"sslMode"}},
		{name: "ignored params", in: "jdbc:mysql://h/d?serverTimezone=UTC&characterEncoding=utf8&serverTimezone=UTC&user=u",
			host: "h", port: 3306, user: "u", db: "d", ignored: []string{"serverTimezone", "characterEncoding"}},
		{name: "empty path with query", in: "jdbc:mysql://h?user=u", host: "h", port: 3306, user: "u"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if r.Host != tc.host || r.Port != tc.port || r.User != tc.user || r.TLS != tc.tls || r.Database != tc.db {
				t.Errorf("got host=%q port=%d user=%q tls=%q db=%q", r.Host, r.Port, r.User, r.TLS, r.Database)
			}
			if !reflect.DeepEqual(r.Ignored, tc.ignored) {
				t.Errorf("ignored = %v, want %v", r.Ignored, tc.ignored)
			}
			for _, want := range tc.notes {
				if !strings.Contains(strings.Join(r.Notes, ";"), want) {
					t.Errorf("notes %v lack %q", r.Notes, want)
				}
			}
			if len(tc.notes) == 0 && len(r.Notes) != 0 {
				t.Errorf("unexpected notes %v", r.Notes)
			}
		})
	}
}

func TestParseNeverLeaksPassword(t *testing.T) {
	for _, in := range []string{
		"jdbc:mysql://u:TOPSECRET@h/db",
		"jdbc:mysql://h/db?password=TOPSECRET&user=u",
		"jdbc:mysql://(host=h,password=TOPSECRET)/db",
		"jdbc:mysql://h/db?other=TOPSECRET",
	} {
		r, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.Join(append(append([]string{r.Host, r.User, r.Database}, r.Ignored...), r.Notes...), "|"); strings.Contains(s, "TOPSECRET") {
			t.Errorf("%s leaked a secret: %s", in, s)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"", "mysql://h/db", "jdbc:postgresql://h/db", "jdbc:oracle:thin:@h:1521:x", "jdbc:mysql:h/db",
		"jdbc:mysql:///db", "jdbc:mysql://h:abc/db", "jdbc:mysql://h:0/db", "jdbc:mysql://h:70000/db",
		"jdbc:mysql://[::1/db", "jdbc:mysql://address=(port=3306)/db",
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
}
