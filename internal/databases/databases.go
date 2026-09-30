// Package databases mengelola database & user MySQL/MariaDB dari panel.
// Koneksi memakai driver pure-Go go-sql-driver/mysql (tanpa cgo).
//
// CATATAN KEAMANAN: nama database/user/host tidak bisa di-parameterize
// di SQL, jadi semuanya divalidasi ketat (hanya [A-Za-z0-9_]) SEBELUM
// dipakai, lalu tetap di-quote dengan backtick sebagai pertahanan
// berlapis. Tidak ada satu pun input user yang diinterpolasi mentah
// ke query.
package databases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
)

// ErrNotConfigured dikembalikan bila kredensial DB belum di-set.
var ErrNotConfigured = errors.New("koneksi database belum dikonfigurasi (set MINIPANEL_DB_USER dan MINIPANEL_DB_PASS)")

// systemDBs adalah database bawaan MySQL/MariaDB yang disembunyikan
// dari daftar utama dan tidak boleh dihapus dari panel.
var systemDBs = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"performance_schema": true,
	"sys":                true,
}

// IsSystemDB mengecek apakah nama adalah database sistem.
func IsSystemDB(name string) bool { return systemDBs[name] }

// identRe: nama database & user MySQL hanya boleh huruf, angka, underscore,
// maksimal 64 karakter (batas MySQL).
var identRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// hostRe: host untuk user MySQL — hostname, IP, atau wildcard "%".
var hostRe = regexp.MustCompile(`^[A-Za-z0-9_.\-%]{1,255}$`)

// ValidIdentifier mengecek nama database/user.
func ValidIdentifier(s string) bool { return identRe.MatchString(s) }

// ValidHost mengecek host user (mis. "localhost", "%", "192.168.1.10").
func ValidHost(s string) bool { return s != "" && hostRe.MatchString(s) }

// quoteIdent meng-quote identifier MySQL dengan backtick.
// Backtick di dalam nama digandakan. Karena input sudah divalidasi
// ValidIdentifier, ini adalah pertahanan berlapis.
func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// QuoteIdent adalah versi ekspor dari quoteIdent, untuk package lain
// (mis. backup) yang perlu membangun query dengan identifier yang sudah
// divalidasi ValidIdentifier.
func QuoteIdent(s string) string { return quoteIdent(s) }

// quoteString meng-escape string literal MySQL (dipakai untuk host).
// Karena input sudah divalidasi ValidHost, escape ini pertahanan berlapis.
func quoteString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// Config adalah kredensial koneksi admin ke MySQL/MariaDB.
type Config struct {
	Host string
	Port string
	User string
	Pass string
}

// ConfigFromEnv membaca konfigurasi dari environment.
func ConfigFromEnv() Config {
	host := os.Getenv("MINIPANEL_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("MINIPANEL_DB_PORT")
	if port == "" {
		port = "3306"
	}
	return Config{
		Host: host,
		Port: port,
		User: os.Getenv("MINIPANEL_DB_USER"),
		Pass: os.Getenv("MINIPANEL_DB_PASS"),
	}
}

// Manager mengelola operasi database.
type Manager struct {
	cfg Config
}

// New membuat Manager. Tidak konek ke DB saat init — koneksi dibuka
// per operasi, jadi panel tidak crash bila DB mati/belum dikonfigurasi.
func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

// Configured mengecek apakah kredensial admin sudah di-set.
func (m *Manager) Configured() bool { return m.cfg.User != "" && m.cfg.Pass != "" }

// Addr mengembalikan alamat server DB (untuk ditampilkan di UI).
func (m *Manager) Addr() string { return m.cfg.Host + ":" + m.cfg.Port }

func (m *Manager) dsn() string {
	c := mysqldrv.NewConfig()
	c.User = m.cfg.User
	c.Passwd = m.cfg.Pass
	c.Net = "tcp"
	c.Addr = m.cfg.Host + ":" + m.cfg.Port
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 15 * time.Second
	c.WriteTimeout = 15 * time.Second
	c.ParseTime = true
	// InterpolateParams: parameter di-escape di sisi client. Wajib true
	// karena statement DDL (CREATE/ALTER USER) tidak mendukung placeholder
	// di server-side prepared statement — tanpa ini "?" dikirim mentah
	// dan query gagal dengan syntax error.
	c.InterpolateParams = true
	return c.FormatDSN()
}

// connect membuka koneksi dan memastikan server bisa dijangkau.
func (m *Manager) connect(ctx context.Context) (*sql.DB, error) {
	if !m.Configured() {
		return nil, ErrNotConfigured
	}
	db, err := sql.Open("mysql", m.dsn())
	if err != nil {
		return nil, fmt.Errorf("gagal membuka koneksi: %w", err)
	}
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("tidak bisa terhubung ke %s: %w", m.Addr(), err)
	}
	return db, nil
}

// Connect membuka koneksi ke server database (level server, tanpa memilih
// database tertentu). Dipakai fitur lain yang butuh akses mentah, mis.
// backup untuk dump & restore SQL. Pemanggil wajib Close() hasilnya.
func (m *Manager) Connect(ctx context.Context) (*sql.DB, error) {
	return m.connect(ctx)
}

// friendlyErr memetakan error umum MySQL ke pesan yang mudah dipahami.
func friendlyErr(op string, err error) error {
	var myErr *mysqldrv.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case 1007: // ER_DB_CREATE_EXISTS
			return errors.New("database sudah ada")
		case 1008: // ER_DB_DROP_EXISTS
			return errors.New("database tidak ditemukan")
		case 1396: // ER_CANNOT_USER
			return errors.New("user sudah ada / operasi user gagal")
		case 1399: // ER_PASSWORD_NO_MATCH (ALTER USER user tidak ada)
			return errors.New("user tidak ditemukan")
		case 1044: // ER_DBACCESS_DENIED_ERROR
			return fmt.Errorf("akses ditolak saat %s (cek hak akses user admin)", op)
		case 1045: // ER_ACCESS_DENIED_ERROR
			return errors.New("username/password database salah")
		case 1146:
			return errors.New("tabel tidak ditemukan")
		}
		return fmt.Errorf("gagal %s: %s", op, myErr.Message)
	}
	return fmt.Errorf("gagal %s: %w", op, err)
}

// Status mengecek konfigurasi & konektivitas (untuk UI).
func (m *Manager) Status(ctx context.Context) (configured, reachable bool, errMsg string) {
	if !m.Configured() {
		return false, false, ""
	}
	db, err := m.connect(ctx)
	if err != nil {
		return true, false, err.Error()
	}
	db.Close()
	return true, true, ""
}

// ListDatabases mengembalikan daftar database user dan database sistem
// (terpisah, agar UI bisa menampilkannya berbeda).
func (m *Manager) ListDatabases(ctx context.Context) (dbs, system []string, err error) {
	db, err := m.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, nil, friendlyErr("membaca daftar database", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, nil, err
		}
		if IsSystemDB(name) {
			system = append(system, name)
		} else {
			dbs = append(dbs, name)
		}
	}
	return dbs, system, rows.Err()
}

// CreateDatabase membuat database baru (utf8mb4).
func (m *Manager) CreateDatabase(ctx context.Context, name string) error {
	if !ValidIdentifier(name) {
		return errors.New("nama database tidak valid (hanya huruf, angka, underscore; maks 64 karakter)")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	q := "CREATE DATABASE " + quoteIdent(name) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	if _, err := db.ExecContext(ctx, q); err != nil {
		return friendlyErr("membuat database", err)
	}
	return nil
}

// DropDatabase menghapus database. Database sistem ditolak.
func (m *Manager) DropDatabase(ctx context.Context, name string) error {
	if !ValidIdentifier(name) {
		return errors.New("nama database tidak valid")
	}
	if IsSystemDB(name) {
		return errors.New("database sistem tidak boleh dihapus")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DROP DATABASE "+quoteIdent(name)); err != nil {
		return friendlyErr("menghapus database", err)
	}
	return nil
}

// DBUser adalah user MySQL/MariaDB beserta host-nya.
type DBUser struct {
	User string `json:"user"`
	Host string `json:"host"`
}

// ListUsers mengembalikan semua user.
func (m *Manager) ListUsers(ctx context.Context) ([]DBUser, error) {
	db, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT user, host FROM mysql.user ORDER BY user, host")
	if err != nil {
		return nil, friendlyErr("membaca daftar user", err)
	}
	defer rows.Close()
	var out []DBUser
	for rows.Next() {
		var u DBUser
		if err := rows.Scan(&u.User, &u.Host); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser membuat user baru dan memberi ALL PRIVILEGES ke satu database.
// Catatan: CREATE USER menyebabkan implicit commit di MySQL, jadi dibuat
// berurutan (bukan transaksi): user dibuat dulu, lalu grant.
func (m *Manager) CreateUser(ctx context.Context, user, host, password, database string) error {
	if !ValidIdentifier(user) {
		return errors.New("nama user tidak valid (hanya huruf, angka, underscore; maks 64 karakter)")
	}
	if !ValidHost(host) {
		return errors.New("host tidak valid")
	}
	if !ValidIdentifier(database) {
		return errors.New("nama database tidak valid")
	}
	if password == "" {
		return errors.New("password tidak boleh kosong")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	account := quoteIdent(user) + "@" + quoteString(host)
	if _, err := db.ExecContext(ctx, "CREATE USER "+account+" IDENTIFIED BY ?", password); err != nil {
		return friendlyErr("membuat user", err)
	}
	if _, err := db.ExecContext(ctx, "GRANT ALL PRIVILEGES ON "+quoteIdent(database)+".* TO "+account); err != nil {
		return friendlyErr("memberi hak akses", err)
	}
	_, _ = db.ExecContext(ctx, "FLUSH PRIVILEGES")
	return nil
}

// DropUser menghapus user.
func (m *Manager) DropUser(ctx context.Context, user, host string) error {
	if !ValidIdentifier(user) {
		return errors.New("nama user tidak valid")
	}
	if !ValidHost(host) {
		return errors.New("host tidak valid")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	account := quoteIdent(user) + "@" + quoteString(host)
	if _, err := db.ExecContext(ctx, "DROP USER "+account); err != nil {
		return friendlyErr("menghapus user", err)
	}
	_, _ = db.ExecContext(ctx, "FLUSH PRIVILEGES")
	return nil
}

// SetPassword mengganti password user.
func (m *Manager) SetPassword(ctx context.Context, user, host, password string) error {
	if !ValidIdentifier(user) {
		return errors.New("nama user tidak valid")
	}
	if !ValidHost(host) {
		return errors.New("host tidak valid")
	}
	if password == "" {
		return errors.New("password tidak boleh kosong")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	account := quoteIdent(user) + "@" + quoteString(host)
	if _, err := db.ExecContext(ctx, "ALTER USER "+account+" IDENTIFIED BY ?", password); err != nil {
		return friendlyErr("mengganti password", err)
	}
	return nil
}

// ListTables mengembalikan daftar tabel dalam satu database.
func (m *Manager) ListTables(ctx context.Context, name string) ([]string, error) {
	if !ValidIdentifier(name) {
		return nil, errors.New("nama database tidak valid")
	}
	db, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SHOW TABLES FROM "+quoteIdent(name))
	if err != nil {
		return nil, friendlyErr("membaca daftar tabel", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
