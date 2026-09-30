// Test unit untuk package apps: validasi definisi, resolusi env,
// alokasi port, dan katalog.
package apps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"minipanel/internal/sites"
)

func TestBuiltinCatalogValid(t *testing.T) {
	cat, err := LoadCatalog(t.TempDir())
	if err != nil {
		t.Fatalf("LoadCatalog gagal: %v", err)
	}
	for _, id := range []string{"wordpress", "ghost", "node-demo"} {
		if _, ok := cat[id]; !ok {
			t.Errorf("app bawaan %q tidak ditemukan", id)
		}
	}
	if len(cat) < 3 {
		t.Errorf("katalog bawaan harus >= 3, dapat %d", len(cat))
	}
}

func TestAppDefValidate(t *testing.T) {
	bad := []AppDef{
		{ID: "Bad ID", Name: "x", Image: "node:22", ContainerPort: 80},
		{ID: "ok", Name: "", Image: "node:22", ContainerPort: 80},
		{ID: "ok", Name: "x", Image: "node:22; rm -rf /", ContainerPort: 80},
		{ID: "ok", Name: "x", Image: "node:22", ContainerPort: 0},
		{ID: "ok", Name: "x", Image: "node:22", ContainerPort: 70000},
		{ID: "ok", Name: "x", Image: "node:22", ContainerPort: 80,
			Env: []EnvVar{{Key: "1BAD"}}},
		{ID: "ok", Name: "x", Image: "node:22", ContainerPort: 80,
			Env: []EnvVar{{Key: "A"}, {Key: "A"}}},
		{ID: "ok", Name: "x", Image: "node:22", ContainerPort: 80,
			Volumes: []VolumeDef{{Name: "v", ContainerPath: "rel/path"}}},
	}
	for i, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("kasus %d: harusnya error, dapat nil", i)
		}
	}
	good := AppDef{ID: "ok-app", Name: "OK", Image: "node:22-alpine", ContainerPort: 3000,
		Env:     []EnvVar{{Key: "GREETING", Default: "hai"}},
		Volumes: []VolumeDef{{Name: "data", ContainerPath: "/data"}}}
	if err := good.Validate(); err != nil {
		t.Errorf("definisi valid ditolak: %v", err)
	}
}

func TestResolveEnv(t *testing.T) {
	list := []EnvVar{
		{Key: "A", Default: "da"},
		{Key: "B", Required: true},
		{Key: "SECRET", Required: true, Secret: true, Generate: true},
		{Key: "OPT"},
	}
	out, err := resolveEnv(list, map[string]string{"B": "bval", "A": "  override  "})
	if err != nil {
		t.Fatalf("resolveEnv gagal: %v", err)
	}
	if out["A"] != "override" {
		t.Errorf("A = %q, mau 'override'", out["A"])
	}
	if out["B"] != "bval" {
		t.Errorf("B = %q", out["B"])
	}
	if out["SECRET"] == "" {
		t.Error("SECRET generate harus terisi")
	}
	if _, ok := out["OPT"]; ok {
		t.Error("OPT kosong tidak boleh masuk map")
	}
	if _, err := resolveEnv(list, map[string]string{}); err == nil {
		t.Error("harus error bila required (B) kosong")
	}
}

func TestResolveCompanionEnvFromApp(t *testing.T) {
	c := CompanionDef{ID: "db", Image: "mysql:8.4", Env: []EnvVar{
		{Key: "MYSQL_PASSWORD", FromAppEnv: "WORDPRESS_DB_PASSWORD"},
		{Key: "MYSQL_RANDOM_ROOT_PASSWORD", Default: "yes"},
	}}
	appEnv := map[string]string{"WORDPRESS_DB_PASSWORD": "s3cret"}
	out, err := resolveCompanionEnv(c, appEnv, nil)
	if err != nil {
		t.Fatalf("resolveCompanionEnv gagal: %v", err)
	}
	if out["MYSQL_PASSWORD"] != "s3cret" {
		t.Errorf("MYSQL_PASSWORD = %q, mau disalin dari app env", out["MYSQL_PASSWORD"])
	}
	if out["MYSQL_RANDOM_ROOT_PASSWORD"] != "yes" {
		t.Errorf("default tidak diterapkan: %v", out)
	}
}

func TestFindFreePort(t *testing.T) {
	mgr, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New gagal: %v", err)
	}
	p, err := mgr.findFreePort()
	if err != nil {
		t.Fatalf("findFreePort gagal: %v", err)
	}
	if p < portMin || p > portMax {
		t.Errorf("port %d di luar rentang", p)
	}
	// Port yang sudah "dipakai" instance harus dilewati.
	mgr.instances["x"] = &Instance{ID: "x", HostPort: portMin}
	p2, err := mgr.findFreePort()
	if err != nil {
		t.Fatalf("findFreePort gagal: %v", err)
	}
	if p2 == portMin {
		t.Errorf("port dipakai instance tidak dilewati: %d", p2)
	}
}

func TestDockerUnavailableGraceful(t *testing.T) {
	d := &Docker{bin: ""}
	if d.Available(context.Background()) {
		t.Error("docker tanpa binary tidak boleh available")
	}
	if _, err := d.Ps(context.Background()); err != ErrDockerUnavailable {
		t.Errorf("Ps harus ErrDockerUnavailable, dapat %v", err)
	}
	mgr, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New gagal: %v", err)
	}
	mgr.docker = &Docker{bin: ""}
	if _, err := mgr.Install(context.Background(), "ghost", "blog.contoh.com", map[string]string{"url": "https://blog.contoh.com"}); err != ErrDockerUnavailable {
		t.Errorf("Install tanpa docker harus ErrDockerUnavailable, dapat %v", err)
	}
}

func TestManagerRestartPersistence(t *testing.T) {
	// Regresi: instances.json TIDAK boleh berada di folder apps/ (folder
	// definisi katalog) — manager harus bisa start ulang setelah ada
	// instance tersimpan.
	dir := t.TempDir()
	ss, err := sites.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := New(dir, ss)
	if err != nil {
		t.Fatal(err)
	}
	m1.mu.Lock()
	m1.instances["wordpress-abc123"] = &Instance{ID: "wordpress-abc123", AppID: "wordpress", Domain: "blog.contoh.com", HostPort: 10001, Status: "error"}
	if err := m1.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m1.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "apps", "instances.json")); !os.IsNotExist(err) {
		t.Fatal("instances.json TIDAK boleh dibuat di folder apps/")
	}
	m2, err := New(dir, ss)
	if err != nil {
		t.Fatalf("manager gagal start ulang: %v", err)
	}
	if _, ok := m2.Get("wordpress-abc123"); !ok {
		t.Fatal("instance hilang setelah restart")
	}
	// Migrasi dari lokasi lama: file instance di apps/ dipindahkan.
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"id":"ghost-xyz789","app_id":"ghost","domain":"g.contoh.com","host_port":10002,"status":"running"}]`
	if err := os.WriteFile(filepath.Join(dir2, "apps", "instances.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m3, err := New(dir2, ss)
	if err != nil {
		t.Fatalf("migrasi lokasi lama gagal: %v", err)
	}
	if _, ok := m3.Get("ghost-xyz789"); !ok {
		t.Fatal("instance lama tidak termigrasi")
	}
	if _, err := os.Stat(filepath.Join(dir2, "apps", "instances.json")); !os.IsNotExist(err) {
		t.Fatal("file lama harusnya sudah dipindahkan dari apps/")
	}
}

func TestValidPublish(t *testing.T) {
	ok := []string{"127.0.0.1:10001:80", "8080:80", "127.0.0.1:10000:2368"}
	for _, p := range ok {
		if !validPublish(p) {
			t.Errorf("%q harusnya valid", p)
		}
	}
	bad := []string{"", "abc", "1:2:3:4", "0:80", "99999:80", "-p 80", "127.0.0.1:80:80\n"}
	for _, p := range bad {
		if validPublish(p) {
			t.Errorf("%q harusnya tidak valid", p)
		}
	}
}
