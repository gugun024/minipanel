// Command minipanel adalah panel kontrol server minimalis:
// satu binary Go yang menyajikan web UI + REST API.
package main

import (
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"context"

	"minipanel/internal/apps"
	"minipanel/internal/auth"
	"minipanel/internal/backup"
	"minipanel/internal/databases"
	"minipanel/internal/files"
	"minipanel/internal/services"
	"minipanel/internal/sites"
	"minipanel/internal/web"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// randomPassword membuat password acak yang aman (16 karakter).
func randomPassword() string {
	const chars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// resolveFileRoot menentukan direktori root file manager.
//
// Bila MINIPANEL_ROOT di-set eksplisit, direktori itu dipakai apa
// adanya — perilaku lama tidak berubah (harus sudah ada dan berupa
// direktori; tidak dibuatkan otomatis).
//
// Bila tidak di-set, kandidat dicoba berurutan: /var/www, lalu
// direktori data panel (dataDir). Kandidat yang belum ada DIBUAT
// otomatis dengan os.MkdirAll, supaya di server fresh (mis. jalan
// via systemd tanpa /var/www dan tanpa $HOME) panel tetap bisa
// start. Init hanya gagal bila SEMUA kandidat gagal; pesan error
// menyebut semua kandidat yang dicoba beserta sebabnya.
func resolveFileRoot(dataDir string) (string, error) {
	if explicit := os.Getenv("MINIPANEL_ROOT"); explicit != "" {
		fm, err := files.New(explicit)
		if err != nil {
			return "", fmt.Errorf("MINIPANEL_ROOT=%s tidak valid: %w", explicit, err)
		}
		return fm.Root(), nil
	}
	candidates := []string{"/var/www", dataDir}
	var tried []string
	for _, c := range candidates {
		if err := os.MkdirAll(c, 0o755); err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", c, err))
			continue
		}
		fm, err := files.New(c)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", c, err))
			continue
		}
		return fm.Root(), nil
	}
	return "", fmt.Errorf("tidak ada direktori root yang valid; dicoba: %s (coba set MINIPANEL_ROOT)", strings.Join(tried, ", "))
}

// resolveDataDir menentukan direktori data panel: MINIPANEL_DATA,
// default ~/.minipanel.
func resolveDataDir() (string, error) {
	dir := getenv("MINIPANEL_DATA", "")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("tidak bisa menentukan home dir (set MINIPANEL_DATA)")
		}
		dir = filepath.Join(home, ".minipanel")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// portAddr mengubah nilai env port menjadi alamat listen.
// "8081" -> ":8081"; "127.0.0.1:8081" dipakai apa adanya.
func portAddr(v string) string {
	if strings.Contains(v, ":") {
		return v
	}
	return ":" + v
}

// envBool membaca env sebagai boolean ("1"/"true"/"yes" -> true).
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// serveOrWarn menjalankan server di goroutine. Bila bind gagal
// (mis. port 80/443 butuh root), hanya tampilkan peringatan dan lanjut.
func serveOrWarn(srv *http.Server, name string, tlsCfg *tls.Config) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Printf("PERINGATAN: %s gagal listen di %s: %v", name, srv.Addr, err)
		log.Printf("  -> serving website di port itu nonaktif; panel tetap jalan.")
		return
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() {
		proto := "http"
		if tlsCfg != nil {
			proto = "https"
		}
		log.Printf("%s berjalan di %s://%s", name, proto, srv.Addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("%s berhenti: %v", name, err)
		}
	}()
}

func main() {
	addr := flag.String("addr", getenv("MINIPANEL_ADDR", ":8080"), "alamat listen, mis. :8080 atau 127.0.0.1:8080")
	flag.Parse()

	// --- Kredensial ---
	// MINIPANEL_USER default "admin". Jika MINIPANEL_PASS tidak di-set,
	// dibuatkan password acak yang dicetak sekali saat startup.
	username := getenv("MINIPANEL_USER", "admin")
	password := os.Getenv("MINIPANEL_PASS")
	generated := false
	if password == "" {
		password = randomPassword()
		generated = true
	}
	authStore, err := auth.New(username, password)
	if err != nil {
		log.Fatalf("gagal init auth: %v", err)
	}

	// --- Direktori data (dipakai juga sebagai fallback terakhir
	// root file manager) ---
	dataDir, err := resolveDataDir()
	if err != nil {
		log.Fatalf("gagal init direktori data: %v", err)
	}

	// --- File manager root ---
	fileRoot, err := resolveFileRoot(dataDir)
	if err != nil {
		log.Fatalf("gagal init file manager: %v", err)
	}
	fm, err := files.New(fileRoot)
	if err != nil {
		log.Fatalf("gagal init file manager: %v", err)
	}

	// --- Services ---
	var svcNames []string
	if v := os.Getenv("MINIPANEL_SERVICES"); v != "" {
		svcNames = strings.Split(v, ",")
	}
	svcMgr := services.New(svcNames)

	// --- Websites + SSL otomatis ---
	siteStore, err := sites.New(dataDir)
	if err != nil {
		log.Fatalf("gagal init websites: %v", err)
	}
	staging := envBool("MINIPANEL_ACME_STAGING", true)
	certMgr, err := sites.NewCertManager(siteStore, filepath.Join(dataDir, "certs"), staging)
	if err != nil {
		log.Fatalf("gagal init cert manager: %v", err)
	}

	// --- Databases (MySQL/MariaDB; opsional — nonaktif bila env kosong) ---
	dbMgr := databases.New(databases.ConfigFromEnv())

	// --- Backups terjadwal (scheduler cron in-process) ---
	backupMgr, err := backup.New(dataDir, siteStore, dbMgr)
	if err != nil {
		log.Fatalf("gagal init backup: %v", err)
	}
	backupMgr.Start()
	defer backupMgr.Stop()

	// --- Apps one-click berbasis Docker (opsional — nonaktif bila
	// docker tidak tersedia; panel tetap jalan) ---
	appsMgr, err := apps.New(dataDir, siteStore)
	if err != nil {
		log.Fatalf("gagal init apps: %v", err)
	}

	handler := web.Handler(web.Deps{
		Auth:      authStore,
		Files:     fm,
		Services:  svcMgr,
		Sites:     siteStore,
		Certs:     certMgr,
		Databases: dbMgr,
		Backups:   backupMgr,
		Apps:      appsMgr,
		DiskPath:  fileRoot,
		Username:  username,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// --- Server HTTP :80 (ACME challenge + redirect ke HTTPS) ---
	httpPort := portAddr(getenv("HTTP_PORT", "80"))
	httpsPort := portAddr(getenv("HTTPS_PORT", "443"))
	httpsPortNum := strings.TrimPrefix(httpsPort, ":")
	if i := strings.LastIndex(httpsPortNum, ":"); i >= 0 {
		httpsPortNum = httpsPortNum[i+1:]
	}
	httpSrv := &http.Server{
		Addr:              httpPort,
		Handler:           certMgr.HTTPHandler(httpsPortNum),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveOrWarn(httpSrv, "server HTTP", nil)

	// --- Server HTTPS :443 (serve website per-domain, TLS via autocert) ---
	httpsSrv := &http.Server{
		Addr:              httpsPort,
		Handler:           certMgr.HTTPSHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveOrWarn(httpsSrv, "server HTTPS", certMgr.TLSConfig())

	fmt.Println("==============================================")
	fmt.Println("  minipanel berjalan")
	showAddr := *addr
	if strings.HasPrefix(showAddr, ":") {
		showAddr = "localhost" + showAddr
	}
	fmt.Printf("  URL      : http://%s\n", showAddr)
	fmt.Printf("  Username : %s\n", username)
	if generated {
		fmt.Printf("  Password : %s  (dibuat otomatis — simpan!)\n", password)
	} else {
		fmt.Println("  Password : (dari MINIPANEL_PASS)")
	}
	fmt.Printf("  File root: %s\n", fileRoot)
	fmt.Printf("  Data dir : %s\n", dataDir)
	fmt.Printf("  Websites : %d terdaftar | HTTP %s -> HTTPS %s\n", len(siteStore.List()), httpPort, httpsPort)
	if staging {
		fmt.Println("  ACME     : STAGING (sertifikat tidak dipercaya browser; set MINIPANEL_ACME_STAGING=0 untuk produksi)")
	} else {
		fmt.Println("  ACME     : PRODUKSI (Let's Encrypt)")
	}
	if svcMgr.Available() {
		fmt.Println("  systemctl: tersedia")
	} else {
		fmt.Println("  systemctl: TIDAK tersedia — kelola service nonaktif")
	}
	if !dbMgr.Configured() {
		fmt.Println("  database : TIDAK dikonfigurasi — set MINIPANEL_DB_USER/MINIPANEL_DB_PASS untuk mengaktifkan")
	} else {
		fmt.Printf("  database : %s\n", dbMgr.Addr())
	}
	bkJobs := backupMgr.ListJobs()
	bkActive := 0
	for _, j := range bkJobs {
		if j.Enabled {
			bkActive++
		}
	}
	fmt.Printf("  Backup   : %d job (%d aktif) — file di %s\n", len(bkJobs), bkActive, filepath.Join(dataDir, "backups"))
	if appsMgr.DockerAvailable(context.Background()) {
		fmt.Printf("  Apps     : docker TERSEDIA — %d aplikasi di katalog\n", len(appsMgr.Catalog()))
	} else {
		fmt.Println("  Apps     : docker TIDAK tersedia — fitur one-click nonaktif")
	}
	fmt.Println("==============================================")

	log.Fatal(srv.ListenAndServe())
}
