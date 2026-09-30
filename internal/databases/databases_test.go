package databases

import (
	"strings"
	"testing"
)

func TestValidIdentifier(t *testing.T) {
	valid := []string{"a", "db1", "my_db", "DB_2024", strings.Repeat("x", 64)}
	for _, s := range valid {
		if !ValidIdentifier(s) {
			t.Errorf("ValidIdentifier(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", "a-b", "a b", "a.b", "a;b", "a'b", `a"b`, "a`b",
		"db; DROP TABLE x;--", "ünïcode", strings.Repeat("x", 65),
		"../../etc", "a/b", "a*b",
	}
	for _, s := range invalid {
		if ValidIdentifier(s) {
			t.Errorf("ValidIdentifier(%q) = true, want false", s)
		}
	}
}

func TestValidHost(t *testing.T) {
	valid := []string{"localhost", "%", "127.0.0.1", "192.168.1.10", "db.internal", "host-name_1"}
	for _, s := range valid {
		if !ValidHost(s) {
			t.Errorf("ValidHost(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "a b", "a'b", `a"b`, "a;b", "a/b", "host!", "ünï"}
	for _, s := range invalid {
		if ValidHost(s) {
			t.Errorf("ValidHost(%q) = true, want false", s)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("mydb"); got != "`mydb`" {
		t.Errorf("quoteIdent(mydb) = %q", got)
	}
	// Backtick digandakan — tidak bisa "kabur" dari quoting.
	if got := quoteIdent("a`b"); got != "`a``b`" {
		t.Errorf("quoteIdent(a`b) = %q", got)
	}
}

func TestQuoteString(t *testing.T) {
	if got := quoteString("localhost"); got != "'localhost'" {
		t.Errorf("quoteString = %q", got)
	}
	// Single quote di-escape — tidak bisa kabur dari string literal.
	if got := quoteString("a'b"); got != `'a\'b'` {
		t.Errorf("quoteString(a'b) = %q", got)
	}
	if got := quoteString(`a\b`); got != `'a\\b'` {
		t.Errorf("quoteString(a\\b) = %q", got)
	}
}

func TestIsSystemDB(t *testing.T) {
	for _, s := range []string{"information_schema", "mysql", "performance_schema", "sys"} {
		if !IsSystemDB(s) {
			t.Errorf("IsSystemDB(%q) = false, want true", s)
		}
	}
	if IsSystemDB("tokodb") {
		t.Errorf("IsSystemDB(tokodb) = true, want false")
	}
}

// TestInjectionBlocked memastikan pola serangan injeksi umum tidak lolos
// validasi identifier — lapisan pertahanan pertama sebelum quoting.
func TestInjectionBlocked(t *testing.T) {
	attacks := []string{
		"x`; DROP DATABASE mysql;--",
		"admin'--",
		"' OR '1'='1",
		"db UNION SELECT * FROM mysql.user",
		"a\x00b", // null byte asli — harus ditolak
		"a\nb",
	}
	for _, s := range attacks {
		if ValidIdentifier(s) || ValidHost(s) {
			t.Errorf("serangan lolos validasi: %q", s)
		}
	}
}
