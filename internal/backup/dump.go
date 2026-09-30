package backup

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"minipanel/internal/databases"
)

// Dump database dilakukan lewat koneksi Go (driver go-sql-driver/mysql),
// BUKAN perintah mysqldump — supaya minipanel tetap satu binary tanpa
// dependensi eksternal. Hasilnya file .sql biasa yang bisa di-restore
// dengan `mysql < file.sql` maupun dari panel.
//
// Batasan dumper Go (didokumentasikan juga di README): tidak mencakup
// trigger, stored procedure/function, dan event. Tabel, view, dan data
// dicakup penuh.

// definerRe menghapus klausa DEFINER dari output SHOW CREATE VIEW agar
// view bisa dibuat ulang oleh user admin mana pun.
var definerRe = regexp.MustCompile("DEFINER=`[^`]*`@`[^`]*`\\s*")

// dumpDatabase menulis dump SQL lengkap satu database ke w.
func dumpDatabase(ctx context.Context, db *sql.DB, dbName string, w io.Writer) error {
	if !databases.ValidIdentifier(dbName) {
		return fmt.Errorf("nama database tidak valid: %q", dbName)
	}
	q := databases.QuoteIdent(dbName)

	var b strings.Builder
	fmt.Fprintf(&b, "-- ============================================\n")
	fmt.Fprintf(&b, "-- Backup database dibuat oleh minipanel\n")
	fmt.Fprintf(&b, "-- Database: %s\n", dbName)
	fmt.Fprintf(&b, "-- Waktu   : %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "-- ============================================\n\n")
	fmt.Fprintf(&b, "SET NAMES utf8mb4;\n")
	fmt.Fprintf(&b, "SET FOREIGN_KEY_CHECKS=0;\n\n")
	fmt.Fprintf(&b, "CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4;\n", q)
	fmt.Fprintf(&b, "USE %s;\n\n", q)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}

	// Daftar tabel & view dari information_schema (bisa di-parameterize).
	rows, err := db.QueryContext(ctx,
		"SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", dbName)
	if err != nil {
		return fmt.Errorf("gagal membaca daftar tabel: %w", err)
	}
	var tables, views []string
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			rows.Close()
			return err
		}
		if typ == "VIEW" {
			views = append(views, name)
		} else {
			tables = append(tables, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, t := range tables {
		if err := dumpTable(ctx, db, dbName, t, w); err != nil {
			return err
		}
	}
	for _, v := range views {
		if err := dumpView(ctx, db, dbName, v, w); err != nil {
			return err
		}
	}

	_, err = io.WriteString(w, "SET FOREIGN_KEY_CHECKS=1;\n")
	return err
}

// scanCreate menjalankan SHOW CREATE TABLE/VIEW dan mengembalikan DDL-nya
// (kolom kedua hasil query).
func scanCreate(ctx context.Context, db *sql.DB, stmt string) (string, error) {
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if len(cols) < 2 {
		return "", fmt.Errorf("hasil SHOW CREATE tidak terduga")
	}
	if !rows.Next() {
		return "", fmt.Errorf("SHOW CREATE kosong")
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	return string(vals[1]), nil
}

func dumpTable(ctx context.Context, db *sql.DB, dbName, table string, w io.Writer) error {
	qt := databases.QuoteIdent(dbName) + "." + databases.QuoteIdent(table)
	ddl, err := scanCreate(ctx, db, "SHOW CREATE TABLE "+qt)
	if err != nil {
		return fmt.Errorf("gagal membaca struktur tabel %s: %w", table, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- Struktur tabel %s\n", table)
	fmt.Fprintf(&b, "DROP TABLE IF EXISTS %s;\n", databases.QuoteIdent(table))
	fmt.Fprintf(&b, "%s;\n\n", ddl)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	return dumpTableData(ctx, db, dbName, table, w)
}

func dumpView(ctx context.Context, db *sql.DB, dbName, view string, w io.Writer) error {
	qv := databases.QuoteIdent(dbName) + "." + databases.QuoteIdent(view)
	ddl, err := scanCreate(ctx, db, "SHOW CREATE VIEW "+qv)
	if err != nil {
		return fmt.Errorf("gagal membaca struktur view %s: %w", view, err)
	}
	ddl = definerRe.ReplaceAllString(ddl, "")
	var b strings.Builder
	fmt.Fprintf(&b, "-- View %s\n", view)
	fmt.Fprintf(&b, "DROP VIEW IF EXISTS %s;\n", databases.QuoteIdent(view))
	fmt.Fprintf(&b, "%s;\n\n", ddl)
	_, err = io.WriteString(w, b.String())
	return err
}

// dumpTableData menulis data tabel sebagai INSERT batch (maks 200 baris
// atau ~512 KB per statement).
func dumpTableData(ctx context.Context, db *sql.DB, dbName, table string, w io.Writer) error {
	qt := databases.QuoteIdent(dbName) + "." + databases.QuoteIdent(table)
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+qt)
	if err != nil {
		return fmt.Errorf("gagal membaca data tabel %s: %w", table, err)
	}
	defer rows.Close()
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	n := len(colTypes)
	types := make([]string, n)
	for i, ct := range colTypes {
		types[i] = ct.DatabaseTypeName()
	}

	insertHead := "INSERT INTO " + databases.QuoteIdent(table) + " VALUES "
	var batch strings.Builder
	batchRows := 0
	flush := func() error {
		if batchRows == 0 {
			return nil
		}
		_, err := io.WriteString(w, batch.String()+";\n")
		batch.Reset()
		batchRows = 0
		return err
	}

	anyRow := false
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		anyRow = true
		if batchRows == 0 {
			batch.WriteString(insertHead)
		} else {
			batch.WriteString(",\n")
		}
		batch.WriteString("(")
		for i, v := range vals {
			if i > 0 {
				batch.WriteString(",")
			}
			batch.WriteString(formatSQLValue(v, types[i]))
		}
		batch.WriteString(")")
		batchRows++
		if batchRows >= 200 || batch.Len() > 512*1024 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if anyRow {
		_, err = io.WriteString(w, "\n")
	}
	return err
}

// isBinaryType mengecek tipe kolom yang nilainya harus ditulis sebagai
// literal hex (agar aman untuk data biner arbitrer).
func isBinaryType(dbType string) bool {
	switch strings.ToUpper(dbType) {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB",
		"BINARY", "VARBINARY", "BIT", "GEOMETRY",
		"POINT", "LINESTRING", "POLYGON",
		"MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION":
		return true
	}
	return false
}

// formatSQLValue memformat satu nilai hasil scan menjadi literal SQL.
func formatSQLValue(v any, dbType string) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		if isBinaryType(dbType) {
			return "0x" + hex.EncodeToString(t)
		}
		return EscapeSQLString(string(t))
	case string:
		return EscapeSQLString(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case bool:
		if t {
			return "1"
		}
		return "0"
	case time.Time:
		return EscapeSQLString(t.Format("2006-01-02 15:04:05.999999"))
	default:
		return EscapeSQLString(fmt.Sprintf("%v", t))
	}
}

// EscapeSQLString meng-quote string sebagai literal MySQL (mengikuti
// aturan escaping default MySQL: backslash escape aktif).
func EscapeSQLString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '"':
			b.WriteString(`\"`)
		case 0x1a:
			b.WriteString(`\Z`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// SplitSQLStatements membagi isi file .sql menjadi statement individual
// berdasarkan titik koma, dengan memperhatikan string ('...', "..."),
// backtick, dan komentar (-- , #, /* */). Komentar tetap menempel pada
// statement berikutnya (tidak berbahaya saat dieksekusi).
func SplitSQLStatements(s string) []string {
	var out []string
	var cur strings.Builder
	i, n := 0, len(s)
	var quote byte // 0 = di luar string; '\'' '"' '`'
	for i < n {
		c := s[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < n {
				cur.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		switch {
		case c == '\'' || c == '"' || c == '`':
			quote = c
			cur.WriteByte(c)
			i++
		case c == '-' && i+2 < n && s[i+1] == '-' && (s[i+2] == ' ' || s[i+2] == '\t' || s[i+2] == '\n' || s[i+2] == '\r'):
			// komentar baris: salin sampai akhir baris
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				cur.WriteString(s[i:])
				i = n
			} else {
				cur.WriteString(s[i : i+j+1])
				i += j + 1
			}
		case c == '#':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				cur.WriteString(s[i:])
				i = n
			} else {
				cur.WriteString(s[i : i+j+1])
				i += j + 1
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				cur.WriteString(s[i:])
				i = n
			} else {
				cur.WriteString(s[i : i+2+j+2])
				i += 2 + j + 2
			}
		case c == ';':
			cur.WriteByte(c)
			stmt := strings.TrimSpace(cur.String())
			if stmt != "" && stmt != ";" {
				out = append(out, stmt)
			}
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

// hasSQLContent mengecek apakah statement punya konten selain komentar
// dan whitespace (statement komentar murni akan error "query kosong"
// bila dikirim ke server).
func hasSQLContent(stmt string) bool {
	s := stmt
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return false
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "#"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return false
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return false
			}
			s = s[i+2:]
		default:
			return s != ""
		}
	}
}

// restoreDatabase mengeksekusi isi file .sql ke server (database target
// ditentukan oleh statement USE di dalam file; file dari minipanel selalu
// memuat CREATE DATABASE IF NOT EXISTS + USE untuk database job-nya).
func restoreDatabase(ctx context.Context, db *sql.DB, content []byte) error {
	stmts := SplitSQLStatements(string(content))
	for i, stmt := range stmts {
		if !hasSQLContent(stmt) {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("restore gagal di statement ke-%d: %w", i+1, err)
		}
	}
	return nil
}
