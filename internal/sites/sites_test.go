// Test untuk website tipe proxy dan migrasi data lama.
package sites

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAddProxyValidation(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProxy("contoh.com", 0); err == nil {
		t.Error("port 0 harus ditolak")
	}
	if _, err := s.AddProxy("contoh.com", 65536); err == nil {
		t.Error("port 65536 harus ditolak")
	}
	if _, err := s.AddProxy("bukan domain", 8080); err == nil {
		t.Error("domain tidak valid harus ditolak")
	}
	site, err := s.AddProxy("app.contoh.com", 10001)
	if err != nil {
		t.Fatalf("AddProxy gagal: %v", err)
	}
	if site.TargetType != TargetProxy || site.ProxyPort != 10001 {
		t.Errorf("site salah: %+v", site)
	}
	if _, err := s.AddProxy("app.contoh.com", 10002); err != ErrExists {
		t.Errorf("duplikat harus ErrExists, dapat %v", err)
	}
	// Domain yang sama tidak boleh dipakai static.
	if _, err := s.Add("app.contoh.com", ""); err != ErrExists {
		t.Errorf("static duplikat harus ErrExists, dapat %v", err)
	}
}

func TestMigrationOldData(t *testing.T) {
	dir := t.TempDir()
	old := `[{"domain":"lama.contoh.com","root":"/var/www/lama","created_at":"2024-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(dir, "websites.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	site, ok := s.Get("lama.contoh.com")
	if !ok {
		t.Fatal("website lama tidak termuat")
	}
	if site.TargetType != TargetStatic {
		t.Errorf("website lama harus jadi static, dapat %q", site.TargetType)
	}
}

// TestProxyRouting: website tipe proxy meneruskan request ke backend
// lokal, mempertahankan Host dan mengisi X-Forwarded-*.
func TestProxyRouting(t *testing.T) {
	var gotHost, gotXFHost, gotXFProto, gotXFF, gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFHost = r.Header.Get("X-Forwarded-Host")
		gotXFProto = r.Header.Get("X-Forwarded-Proto")
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotPath = r.URL.Path
		fmt.Fprint(w, "halo-backend")
	}))
	defer backend.Close()
	// backend.URL = http://127.0.0.1:PORT
	portStr := backend.URL[strings.LastIndex(backend.URL, ":")+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProxy("app.contoh.com", port); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("statis.contoh.com", ""); err != nil {
		t.Fatal(err)
	}
	cm, err := NewCertManager(s, filepath.Join(dir, "certs"), true)
	if err != nil {
		t.Fatal(err)
	}
	handler := cm.HTTPSHandler()

	// Request ke domain proxy.
	req := httptest.NewRequest("GET", "https://app.contoh.com/halo?x=1", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != "halo-backend" {
		t.Fatalf("body = %q (status %d), mau 'halo-backend'", body, rec.Code)
	}
	if gotHost != "app.contoh.com" {
		t.Errorf("backend melihat Host %q, mau domain asli", gotHost)
	}
	if gotXFHost != "app.contoh.com" || gotXFProto != "https" {
		t.Errorf("X-Forwarded-Host=%q Proto=%q", gotXFHost, gotXFProto)
	}
	if gotXFF != "203.0.113.9" {
		t.Errorf("X-Forwarded-For = %q, mau 203.0.113.9", gotXFF)
	}
	if gotPath != "/halo" {
		t.Errorf("path = %q", gotPath)
	}

	// Host tak dikenal -> 404.
	req2 := httptest.NewRequest("GET", "https://tidak-ada.contoh.com/", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("host tak dikenal harus 404, dapat %d", rec2.Code)
	}
}

// TestProxyBackendDown: backend mati -> 502, bukan crash/hang.
func TestProxyBackendDown(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Port 59999 hampir pasti tidak ada yang listen.
	if _, err := s.AddProxy("down.contoh.com", 59999); err != nil {
		t.Fatal(err)
	}
	cm, err := NewCertManager(s, filepath.Join(dir, "certs"), true)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://down.contoh.com/", nil)
	rec := httptest.NewRecorder()
	cm.HTTPSHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("backend mati harus 502, dapat %d", rec.Code)
	}
}
