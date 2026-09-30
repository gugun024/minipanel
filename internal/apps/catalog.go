// Package catalog: definisi aplikasi one-click.
//
// Definisi aplikasi adalah DATA, bukan kode: 3 aplikasi bawaan di bawah,
// dan user bisa menambah/menimpa lewat file JSON di <data-dir>/apps/*.json.
// Setiap definisi menjelaskan image docker, port container, env yang perlu
// diisi, volume, dan companion container (mis. database).
package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var appIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// EnvVar adalah satu environment variable yang bisa diisi user saat install.
type EnvVar struct {
	Key      string `json:"key"`               // nama env, mis. WORDPRESS_DB_PASSWORD
	Label    string `json:"label"`             // label untuk form, mis. "Password database"
	Default  string `json:"default,omitempty"` // nilai default
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`   // tampil sebagai input password
	Generate bool   `json:"generate,omitempty"` // buat acak bila kosong (untuk Secret)
	// FromAppEnv (khusus companion): salin nilai dari env aplikasi utama,
	// mis. password database yang sama. Diabaikan untuk env aplikasi utama.
	FromAppEnv string `json:"from_app_env,omitempty"`
}

// VolumeDef adalah satu named volume docker.
type VolumeDef struct {
	Name          string `json:"name"`           // suffix; nama asli: mp-<instance>-<name>
	ContainerPath string `json:"container_path"` // path di dalam container, mis. /var/www/html
}

// CompanionDef adalah container pendamping (mis. database) yang jalan
// di network privat yang sama dengan container utama.
type CompanionDef struct {
	ID           string      `json:"id"` // suffix nama container: <instance>-<id>
	Image        string      `json:"image"`
	Env          []EnvVar    `json:"env,omitempty"`
	Volumes      []VolumeDef `json:"volumes,omitempty"`
	NetworkAlias string      `json:"network_alias,omitempty"` // alias DNS di network, mis. "db"
}

// AppDef adalah definisi satu aplikasi one-click.
type AppDef struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Image         string         `json:"image"`
	Command       []string       `json:"command,omitempty"`
	ContainerPort int            `json:"container_port"`
	Env           []EnvVar       `json:"env,omitempty"`
	Volumes       []VolumeDef    `json:"volumes,omitempty"`
	Companions    []CompanionDef `json:"companions,omitempty"`
}

// validateEnv memeriksa daftar EnvVar: key valid, tidak duplikat.
func validateEnv(list []EnvVar, where string) error {
	seen := map[string]bool{}
	for _, e := range list {
		if !envKeyRe.MatchString(e.Key) {
			return fmt.Errorf("%s: nama env %q tidak valid", where, e.Key)
		}
		if seen[e.Key] {
			return fmt.Errorf("%s: env %q duplikat", where, e.Key)
		}
		seen[e.Key] = true
	}
	return nil
}

// Validate memeriksa definisi aplikasi secara menyeluruh.
func (a *AppDef) Validate() error {
	if !appIDRe.MatchString(a.ID) {
		return fmt.Errorf("id aplikasi %q tidak valid", a.ID)
	}
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("nama aplikasi wajib diisi")
	}
	if !imageRe.MatchString(a.Image) {
		return fmt.Errorf("image %q tidak valid", a.Image)
	}
	if a.ContainerPort < 1 || a.ContainerPort > 65535 {
		return fmt.Errorf("container_port %d tidak valid", a.ContainerPort)
	}
	if err := validateEnv(a.Env, "app "+a.ID); err != nil {
		return err
	}
	seenVol := map[string]bool{}
	for _, v := range a.Volumes {
		if !volumeNameRe.MatchString(v.Name) {
			return fmt.Errorf("app %s: nama volume %q tidak valid", a.ID, v.Name)
		}
		if !strings.HasPrefix(v.ContainerPath, "/") {
			return fmt.Errorf("app %s: container_path %q harus absolut", a.ID, v.ContainerPath)
		}
		if seenVol[v.Name] {
			return fmt.Errorf("app %s: volume %q duplikat", a.ID, v.Name)
		}
		seenVol[v.Name] = true
	}
	seenComp := map[string]bool{}
	for _, c := range a.Companions {
		if !appIDRe.MatchString(c.ID) {
			return fmt.Errorf("app %s: id companion %q tidak valid", a.ID, c.ID)
		}
		if seenComp[c.ID] {
			return fmt.Errorf("app %s: companion %q duplikat", a.ID, c.ID)
		}
		seenComp[c.ID] = true
		if !imageRe.MatchString(c.Image) {
			return fmt.Errorf("app %s: image companion %q tidak valid", a.ID, c.Image)
		}
		if err := validateEnv(c.Env, "companion "+a.ID+"/"+c.ID); err != nil {
			return err
		}
		if c.NetworkAlias != "" && !containerNameRe.MatchString(c.NetworkAlias) {
			return fmt.Errorf("app %s: network_alias %q tidak valid", a.ID, c.NetworkAlias)
		}
	}
	return nil
}

// builtinCatalog adalah 3 aplikasi bawaan.
func builtinCatalog() []AppDef {
	return []AppDef{
		{
			ID:            "wordpress",
			Name:          "WordPress",
			Description:   "CMS populer untuk blog & company profile. Otomatis dipasangkan dengan database MySQL di network privat.",
			Image:         "wordpress:latest",
			ContainerPort: 80,
			Env: []EnvVar{
				{Key: "WORDPRESS_DB_HOST", Label: "Host database", Default: "db"},
				{Key: "WORDPRESS_DB_NAME", Label: "Nama database", Default: "wordpress"},
				{Key: "WORDPRESS_DB_USER", Label: "User database", Default: "wordpress"},
				{Key: "WORDPRESS_DB_PASSWORD", Label: "Password database", Required: true, Secret: true, Generate: true},
			},
			Volumes: []VolumeDef{
				{Name: "wp", ContainerPath: "/var/www/html"},
			},
			Companions: []CompanionDef{
				{
					ID:           "db",
					Image:        "mysql:8.4",
					NetworkAlias: "db",
					Env: []EnvVar{
						// Nilai disalin dari env aplikasi utama (FromAppEnv).
						{Key: "MYSQL_DATABASE", Label: "Nama database", FromAppEnv: "WORDPRESS_DB_NAME"},
						{Key: "MYSQL_USER", Label: "User database", FromAppEnv: "WORDPRESS_DB_USER"},
						{Key: "MYSQL_PASSWORD", Label: "Password database", Secret: true, FromAppEnv: "WORDPRESS_DB_PASSWORD"},
						{Key: "MYSQL_RANDOM_ROOT_PASSWORD", Label: "", Default: "yes"},
					},
					Volumes: []VolumeDef{
						{Name: "db", ContainerPath: "/var/lib/mysql"},
					},
				},
			},
		},
		{
			ID:            "ghost",
			Name:          "Ghost",
			Description:   "Platform blogging modern & cepat (Node.js). Pakai SQLite bawaan — tanpa database terpisah.",
			Image:         "ghost:latest",
			ContainerPort: 2368,
			Env: []EnvVar{
				{Key: "url", Label: "URL publik (mis. https://blog.contoh.com)", Required: true},
			},
			Volumes: []VolumeDef{
				{Name: "data", ContainerPath: "/var/lib/ghost/content"},
			},
		},
		{
			ID:            "node-demo",
			Name:          "Node.js (contoh)",
			Description:   "Contoh aplikasi Node.js generik — server HTTP kecil. Contoh cara membungkus aplikasi Node/Next.js sendiri; lihat README untuk panduan.",
			Image:         "node:22-alpine",
			ContainerPort: 3000,
			Command: []string{"node", "-e",
				"const http=require('http');" +
					"const g=process.env.GREETING||'Halo dari minipanel!';" +
					"http.createServer((q,s)=>{s.writeHead(200,{'Content-Type':'text/plain; charset=utf-8'});s.end(g)}).listen(3000);"},
			Env: []EnvVar{
				{Key: "GREETING", Label: "Pesan sapaan", Default: "Halo dari minipanel!"},
			},
		},
	}
}

// LoadCatalog memuat katalog: bawaan + file JSON user di <dataDir>/apps/*.json.
// File user menimpa definisi bawaan bila ID-nya sama.
func LoadCatalog(dataDir string) (map[string]AppDef, error) {
	cat := map[string]AppDef{}
	for _, a := range builtinCatalog() {
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("definisi bawaan rusak: %w", err)
		}
		cat[a.ID] = a
	}
	dir := filepath.Join(dataDir, "apps")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return cat, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("gagal baca %s: %w", e.Name(), err)
		}
		var a AppDef
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("file %s bukan JSON valid: %w", e.Name(), err)
		}
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("file %s tidak valid: %w", e.Name(), err)
		}
		cat[a.ID] = a
	}
	return cat, nil
}

// SortedList mengembalikan definisi terurut berdasarkan nama.
func SortedList(cat map[string]AppDef) []AppDef {
	list := make([]AppDef, 0, len(cat))
	for _, a := range cat {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}
