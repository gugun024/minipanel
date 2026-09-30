// Package sites mengelola daftar website (domain -> document root).
// Data disimpan di file JSON di direktori data panel. Setiap penambahan
// domain divalidasi ketat, dan document root selalu dibuat/di-jail agar
// tidak bisa menunjuk ke lokasi berbahaya seperti "/".
package sites

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tipe target website: "static" (serve file dari document root)
// atau "proxy" (teruskan request ke aplikasi di 127.0.0.1:port).
const (
	TargetStatic = "static"
	TargetProxy  = "proxy"
)

// Site adalah satu website yang dikelola panel.
type Site struct {
	Domain     string    `json:"domain"`
	Root       string    `json:"root"`
	TargetType string    `json:"target_type"` // "static" | "proxy"
	ProxyPort  int       `json:"proxy_port,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

var (
	// ErrNotFound dikembalikan bila domain tidak terdaftar.
	ErrNotFound = errors.New("website tidak ditemukan")
	// ErrExists dikembalikan bila domain sudah terdaftar.
	ErrExists = errors.New("domain sudah terdaftar")
)

// domainRe memvalidasi hostname secara ketat: label 1-63 karakter,
// alfanumerik dan strip (tidak di awal/akhir label), minimal satu titik,
// TLD minimal 2 huruf. Batas total 253 karakter dicek terpisah di
// ValidDomain karena regexp Go (RE2) tidak mendukung lookahead.
var domainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// NormalizeDomain merapikan input domain: huruf kecil, tanpa spasi,
// tanpa scheme (http://), tanpa path, tanpa trailing dot.
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	return s
}

// ValidDomain mengecek apakah domain valid untuk didaftarkan.
func ValidDomain(d string) bool {
	return len(d) <= 253 && domainRe.MatchString(d)
}

// Store menyimpan daftar website di file JSON dengan akses aman-concurrent.
type Store struct {
	mu    sync.RWMutex
	path  string // file websites.json
	base  string // direktori default untuk document root baru
	sites map[string]Site
}

// New membuat Store. dataDir dipakai untuk file JSON dan direktori
// default document root (<dataDir>/sites/<domain>).
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path:  filepath.Join(dataDir, "websites.json"),
		base:  filepath.Join(dataDir, "sites"),
		sites: make(map[string]Site),
	}
	if b, err := os.ReadFile(s.path); err == nil && len(b) > 0 {
		var list []Site
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, errors.New("gagal membaca websites.json: " + err.Error())
		}
		for _, site := range list {
			d := NormalizeDomain(site.Domain)
			if ValidDomain(d) {
				site.Domain = d
				// Migrasi data lama: website tanpa target_type dianggap static.
				if site.TargetType == "" {
					site.TargetType = TargetStatic
				}
				s.sites[d] = site
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// saveLocked menulis seluruh daftar ke disk secara atomik (temp + rename).
func (s *Store) saveLocked() error {
	list := make([]Site, 0, len(s.sites))
	for _, site := range s.sites {
		list = append(list, site)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Domain < list[j].Domain })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List mengembalikan semua website terurut alfabetis.
func (s *Store) List() []Site {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]Site, 0, len(s.sites))
	for _, site := range s.sites {
		list = append(list, site)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Domain < list[j].Domain })
	return list
}

// Get mengambil website berdasarkan domain (dinormalisasi dulu).
func (s *Store) Get(domain string) (Site, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	site, ok := s.sites[NormalizeDomain(domain)]
	return site, ok
}

// resolveRoot memvalidasi & menyiapkan document root.
// Root kosong -> default <base>/<domain>. Path harus absolut, tidak boleh
// "/" dan tidak boleh mengandung "..". Direktori dibuat bila belum ada.
func (s *Store) resolveRoot(domain, root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(s.base, domain)
	}
	for _, seg := range strings.Split(filepath.ToSlash(root), "/") {
		if seg == ".." {
			return "", errors.New("document root tidak valid")
		}
	}
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) {
		return "", errors.New("document root harus path absolut")
	}
	if clean == "/" {
		return "", errors.New("document root tidak boleh /")
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", errors.New("gagal menyiapkan document root: " + err.Error())
	}
	st, err := os.Stat(clean)
	if err != nil || !st.IsDir() {
		return "", errors.New("document root bukan direktori")
	}
	return clean, nil
}

// Add mendaftarkan website baru.
func (s *Store) Add(domain, root string) (Site, error) {
	domain = NormalizeDomain(domain)
	if !ValidDomain(domain) {
		return Site{}, errors.New("nama domain tidak valid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sites[domain]; exists {
		return Site{}, ErrExists
	}
	clean, err := s.resolveRoot(domain, root)
	if err != nil {
		return Site{}, err
	}
	site := Site{Domain: domain, Root: clean, TargetType: TargetStatic, CreatedAt: time.Now().UTC()}
	s.sites[domain] = site
	if err := s.saveLocked(); err != nil {
		delete(s.sites, domain)
		return Site{}, err
	}
	return site, nil
}

// ValidProxyPort mengecek port untuk website tipe proxy.
func ValidProxyPort(port int) bool {
	return port >= 1 && port <= 65535
}

// AddProxy mendaftarkan website tipe proxy: semua request HTTPS ke domain
// ini diteruskan ke aplikasi yang listen di 127.0.0.1:port.
// Tidak ada document root yang dibuat untuk tipe ini.
func (s *Store) AddProxy(domain string, port int) (Site, error) {
	domain = NormalizeDomain(domain)
	if !ValidDomain(domain) {
		return Site{}, errors.New("nama domain tidak valid")
	}
	if !ValidProxyPort(port) {
		return Site{}, errors.New("port proxy harus antara 1-65535")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sites[domain]; exists {
		return Site{}, ErrExists
	}
	site := Site{Domain: domain, TargetType: TargetProxy, ProxyPort: port, CreatedAt: time.Now().UTC()}
	s.sites[domain] = site
	if err := s.saveLocked(); err != nil {
		delete(s.sites, domain)
		return Site{}, err
	}
	return site, nil
}

// Delete menghapus website dari daftar (file di document root TIDAK dihapus).
func (s *Store) Delete(domain string) error {
	domain = NormalizeDomain(domain)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sites[domain]; !exists {
		return ErrNotFound
	}
	delete(s.sites, domain)
	return s.saveLocked()
}
