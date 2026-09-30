package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gzipTarWriter struct {
	gz *gzip.Writer
	tw *tar.Writer
}

func newGzipTarWriter(w io.Writer) *gzipTarWriter {
	gz := gzip.NewWriter(w)
	return &gzipTarWriter{gz: gz, tw: tar.NewWriter(gz)}
}

func writeTarEntry(t *testing.T, w *gzipTarWriter, name, content string) {
	t.Helper()
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := w.tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w.tw, content); err != nil {
		t.Fatal(err)
	}
}

func (w *gzipTarWriter) close() {
	w.tw.Close()
	w.gz.Close()
}

func TestValidSchedule(t *testing.T) {
	valid := []string{"0 2 * * *", "*/15 * * * *", "0 0 * * 0", "@daily", "30 4 1 * *"}
	for _, s := range valid {
		if !ValidSchedule(s) {
			t.Errorf("ValidSchedule(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "abc", "0 2 * *", "61 * * * *", "0 2 * * * *", "* * *"}
	for _, s := range invalid {
		if ValidSchedule(s) {
			t.Errorf("ValidSchedule(%q) = true, want false", s)
		}
	}
}

func TestValidateBasic(t *testing.T) {
	base := Job{Name: "harian", Schedule: "0 2 * * *", TargetType: TargetWebsite}
	j := base
	if err := validateBasic(&j); err != nil {
		t.Fatalf("job valid ditolak: %v", err)
	}
	if j.Retention != 7 {
		t.Errorf("retention default = %d, want 7", j.Retention)
	}
	bad := []Job{
		{Name: "", Schedule: "0 2 * * *", TargetType: TargetWebsite},
		{Name: "x", Schedule: "bukan cron", TargetType: TargetWebsite},
		{Name: "x", Schedule: "0 2 * * *", TargetType: "file"},
		{Name: "a/b", Schedule: "0 2 * * *", TargetType: TargetWebsite},
		{Name: "x", Schedule: "0 2 * * *", TargetType: TargetWebsite, Retention: 999},
		{Name: "x", Schedule: "0 2 * * *", TargetType: TargetWebsite, Retention: -1},
	}
	for i, bj := range bad {
		bj := bj
		if err := validateBasic(&bj); err == nil {
			t.Errorf("kasus %d: job tidak valid lolos: %+v", i, bj)
		}
	}
}

func TestEscapeSQLString(t *testing.T) {
	cases := map[string]string{
		"plain":      `'plain'`,
		"a'b":        `'a\'b'`,
		`a\b`:        `'a\\b'`,
		"a\nb":       `'a\nb'`,
		"a\x00b":     `'a\0b'`,
		"a\x1ab":     `'a\Zb'`,
		`quote"here`: `'quote\"here'`,
	}
	for in, want := range cases {
		if got := EscapeSQLString(in); got != want {
			t.Errorf("EscapeSQLString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFormatSQLValue(t *testing.T) {
	if got := formatSQLValue(nil, "INT"); got != "NULL" {
		t.Errorf("nil = %s", got)
	}
	if got := formatSQLValue(int64(42), "INT"); got != "42" {
		t.Errorf("int = %s", got)
	}
	if got := formatSQLValue([]byte("halo"), "VARCHAR"); got != "'halo'" {
		t.Errorf("varchar bytes = %s", got)
	}
	if got := formatSQLValue([]byte{0xde, 0xad}, "BLOB"); got != "0xdead" {
		t.Errorf("blob = %s", got)
	}
	// DECIMAL dari driver datang sebagai []byte — harus tetap angka valid
	// setelah di-quote (MySQL mengkonversi string -> decimal).
	if got := formatSQLValue([]byte("19.99"), "DECIMAL"); got != "'19.99'" {
		t.Errorf("decimal = %s", got)
	}
}

func TestSplitSQLStatements(t *testing.T) {
	sqlText := "-- header komentar\n" +
		"SET NAMES utf8mb4;\n" +
		"INSERT INTO t VALUES ('a;b', 'it\\'s', \"x;y\");\n" +
		"/* blok; komentar */ CREATE TABLE `we;ird` (id INT); # trailing\n" +
		"INSERT INTO t VALUES ('-- bukan komentar');\n"
	stmts := SplitSQLStatements(sqlText)
	if len(stmts) != 4 {
		t.Fatalf("jumlah statement = %d, want 4: %q", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "SET NAMES") {
		t.Errorf("statement 1 salah: %q", stmts[0])
	}
	if !strings.Contains(stmts[1], "'a;b'") || !strings.Contains(stmts[1], `"x;y"`) {
		t.Errorf("statement 2 salah (titik koma dalam string ikut memotong): %q", stmts[1])
	}
	if !strings.Contains(stmts[2], "CREATE TABLE") {
		t.Errorf("statement 3 salah: %q", stmts[2])
	}
	if !strings.Contains(stmts[3], "'-- bukan komentar'") {
		t.Errorf("statement 4 salah: %q", stmts[3])
	}
	if !hasSQLContent(stmts[0]) {
		t.Error("statement berkomentar + SET harus dianggap punya konten")
	}
	if hasSQLContent("-- hanya komentar\n/* dan blok */") {
		t.Error("komentar murni tidak boleh dianggap konten")
	}
}

func TestResolveFilePathJail(t *testing.T) {
	dir := t.TempDir()
	ok, err := resolveFilePath(dir, "20251001-020000-site.tar.gz")
	if err != nil || ok != filepath.Join(dir, "20251001-020000-site.tar.gz") {
		t.Errorf("nama valid ditolak: %v %v", ok, err)
	}
	bad := []string{
		"../backups.json",
		"..%2F..%2Fbackups.json",
		"../../etc/passwd",
		"/etc/passwd",
		"subdir/20251001-020000-site.tar.gz",
		"20251001-020000-site.tar.gz/../../x",
		"file.txt", // akhiran tidak dikenal
		"",
		"..",
	}
	for _, name := range bad {
		if _, err := resolveFilePath(dir, name); err == nil {
			t.Errorf("nama berbahaya lolos: %q", name)
		}
	}
}

func TestTarRoundtrip(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>halo</h1>"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("isi A; dengan 'quote'"), 0o644)
	os.Symlink("sub/a.txt", filepath.Join(src, "link.txt"))

	arc := filepath.Join(t.TempDir(), "test-site.tar.gz")
	if err := createTarGz(src, arc); err != nil {
		t.Fatalf("createTarGz: %v", err)
	}
	dst := t.TempDir()
	if err := extractTarGz(arc, dst); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "index.html"))
	if err != nil || string(b) != "<h1>halo</h1>" {
		t.Errorf("index.html hasil restore salah: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dst, "sub", "a.txt"))
	if err != nil || string(b) != "isi A; dengan 'quote'" {
		t.Errorf("a.txt hasil restore salah: %q %v", b, err)
	}
	link, err := os.Readlink(filepath.Join(dst, "link.txt"))
	if err != nil || link != "sub/a.txt" {
		t.Errorf("symlink hasil restore salah: %q %v", link, err)
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	// Buat arsip jahat secara manual dengan entri ../jahat.txt.
	dir := t.TempDir()
	arc := filepath.Join(dir, "jahat-site.tar.gz")
	f, _ := os.Create(arc)
	gz := newGzipTarWriter(f)
	writeTarEntry(t, gz, "../jahat.txt", "boo")
	gz.close()
	f.Close()

	dst := filepath.Join(dir, "dst")
	os.MkdirAll(dst, 0o755)
	if err := extractTarGz(arc, dst); err == nil {
		t.Error("arsip zip-slip harus ditolak")
	}
	if _, err := os.Stat(filepath.Join(dir, "jahat.txt")); !os.IsNotExist(err) {
		t.Error("file jahat tertulis di luar direktori tujuan!")
	}
}

func TestPruneRetention(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"20250927-020000-site.tar.gz",
		"20250928-020000-site.tar.gz",
		"20250929-020000-site.tar.gz",
		"20250930-020000-site.tar.gz",
		"20250930-020000-db.sql",
	}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	if err := pruneRetention(dir, TargetWebsite, 2); err != nil {
		t.Fatal(err)
	}
	files, err := listFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Sisa: 2 site terbaru + 1 db (tipe lain tidak disentuh).
	if len(files) != 3 {
		t.Fatalf("sisa file = %d, want 3: %v", len(files), files)
	}
	for _, fi := range files {
		if fi.Name == "20250927-020000-site.tar.gz" || fi.Name == "20250928-020000-site.tar.gz" {
			t.Errorf("file lama tidak terhapus: %s", fi.Name)
		}
	}
}

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j := Job{ID: "abc123", Name: "test", Enabled: true, Schedule: "0 2 * * *",
		TargetType: TargetWebsite, Domain: "contoh.com", Retention: 7}
	if err := st.Add(j); err != nil {
		t.Fatal(err)
	}
	// Store baru dari disk harus membaca job yang sama.
	st2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st2.Get("abc123")
	if !ok || got.Name != "test" || !got.Enabled {
		t.Errorf("job tidak terbaca ulang: %+v ok=%v", got, ok)
	}
	// Update tidak boleh mengubah LastRun/LastStatus.
	st2.SetRunState("abc123", got.CreatedAt, "ok")
	j.Name = "diganti"
	if err := st2.Update(j); err != nil {
		t.Fatal(err)
	}
	got, _ = st2.Get("abc123")
	if got.Name != "diganti" || got.LastStatus != "ok" {
		t.Errorf("update salah: %+v", got)
	}
}
