// Package manager: lifecycle instance aplikasi one-click.
//
// Install: pull image -> cari port bebas -> buat network (bila ada companion)
// -> jalankan companion -> jalankan container utama -> daftarkan website
// tipe proxy ke domain user. Semua langkah dicatat; bila gagal di tengah,
// yang sudah dibuat di-rollback (best effort).
package apps

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"minipanel/internal/sites"
)

// Rentang port host untuk aplikasi (tidak perlu root, jauh dari port umum).
const (
	portMin = 10000
	portMax = 20000
)

// Instance adalah satu aplikasi yang ter-install.
type Instance struct {
	ID          string    `json:"id"` // mis. "wordpress-a1b2c3"
	AppID       string    `json:"app_id"`
	Name        string    `json:"name"` // nama tampilan, mis. "WordPress (blog.contoh.com)"
	Domain      string    `json:"domain"`
	HostPort    int       `json:"host_port"`
	Network     string    `json:"network,omitempty"`
	Containers  []string  `json:"containers"` // nama container (utama terakhir)
	Volumes     []string  `json:"volumes,omitempty"`
	Status      string    `json:"status"` // "installing" | "running" | "error"
	StatusError string    `json:"status_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Manager mengelola katalog, instance, dan docker.
type Manager struct {
	mu        sync.RWMutex
	dataDir   string
	storePath string
	docker    *Docker
	sites     *sites.Store
	catalog   map[string]AppDef
	instances map[string]*Instance
}

// New membuat Manager. Katalog dimuat dari bawaan + <dataDir>/apps/*.json.
func New(dataDir string, siteStore *sites.Store) (*Manager, error) {
	// Migrasi HARUS sebelum LoadCatalog: versi awal menyimpan instance di
	// <dataDir>/apps/instances.json, padahal folder apps/ dibaca sebagai
	// definisi katalog — file instance akan gagal dibaca sebagai AppDef.
	storePath := filepath.Join(dataDir, "instances.json")
	oldPath := filepath.Join(dataDir, "apps", "instances.json")
	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.MkdirAll(dataDir, 0o755); err == nil {
				_ = os.Rename(oldPath, storePath)
			}
		}
	}
	cat, err := LoadCatalog(dataDir)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		dataDir:   dataDir,
		storePath: storePath,
		docker:    NewDocker(),
		sites:     siteStore,
		catalog:   cat,
		instances: map[string]*Instance{},
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "apps"), 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(m.storePath); err == nil && len(b) > 0 {
		var list []*Instance
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("gagal membaca instances.json: %w", err)
		}
		for _, in := range list {
			if in != nil && in.ID != "" {
				m.instances[in.ID] = in
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

// DockerAvailable melaporkan apakah docker bisa dipakai.
func (m *Manager) DockerAvailable(ctx context.Context) bool {
	return m.docker.Available(ctx)
}

// Catalog mengembalikan daftar definisi aplikasi terurut.
func (m *Manager) Catalog() []AppDef {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SortedList(m.catalog)
}

// GetDef mengambil satu definisi aplikasi.
func (m *Manager) GetDef(id string) (AppDef, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.catalog[id]
	return a, ok
}

func (m *Manager) saveLocked() error {
	list := make([]*Instance, 0, len(m.instances))
	for _, in := range m.instances {
		list = append(list, in)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.storePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.storePath)
}

// List mengembalikan semua instance terurut.
func (m *Manager) List() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Instance, 0, len(m.instances))
	for _, in := range m.instances {
		cp := *in
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// Get mengambil satu instance.
func (m *Manager) Get(id string) (*Instance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	in, ok := m.instances[id]
	if !ok {
		return nil, false
	}
	cp := *in
	return &cp, true
}

// randomSuffix membuat suffix acak 6 karakter untuk id instance.
func randomSuffix() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// randomSecret membuat secret acak 24 karakter untuk password generate.
func randomSecret() string {
	const chars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// findFreePort mencari port bebas di rentang portMin-portMax yang belum
// dipakai instance lain. Dicek dengan bind sungguhan agar akurat.
func (m *Manager) findFreePort() (int, error) {
	m.mu.RLock()
	used := map[int]bool{}
	for _, in := range m.instances {
		used[in.HostPort] = true
	}
	m.mu.RUnlock()
	for p := portMin; p <= portMax; p++ {
		if used[p] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		ln.Close()
		return p, nil
	}
	return 0, errors.New("tidak ada port bebas di rentang 10000-20000")
}

// resolveEnv menggabungkan nilai user + default + generate untuk satu
// daftar EnvVar. Mengembalikan error bila ada required yang kosong.
func resolveEnv(list []EnvVar, userVals map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range list {
		v := strings.TrimSpace(userVals[e.Key])
		if v == "" {
			v = e.Default
		}
		if v == "" && e.Generate {
			v = randomSecret()
		}
		if v == "" && e.Required {
			label := e.Label
			if label == "" {
				label = e.Key
			}
			return nil, fmt.Errorf("env %q wajib diisi", label)
		}
		if v != "" {
			out[e.Key] = v
		}
	}
	return out, nil
}

// resolveCompanionEnv menyelesaikan env companion: FromAppEnv disalin
// dari env aplikasi utama yang sudah di-resolve.
func resolveCompanionEnv(c CompanionDef, appEnv, userVals map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range c.Env {
		var v string
		if e.FromAppEnv != "" {
			v = appEnv[e.FromAppEnv]
		} else {
			v = strings.TrimSpace(userVals["companion:"+c.ID+":"+e.Key])
			if v == "" {
				v = e.Default
			}
			if v == "" && e.Generate {
				v = randomSecret()
			}
		}
		if v == "" && e.Required {
			return nil, fmt.Errorf("env companion %s/%s wajib diisi", c.ID, e.Key)
		}
		if v != "" {
			out[e.Key] = v
		}
	}
	return out, nil
}

// rollbackBestEffort membersihkan container/network yang sempat dibuat
// saat install gagal di tengah jalan.
func (m *Manager) rollbackBestEffort(ctx context.Context, containers []string, network string) {
	if len(containers) > 0 {
		m.docker.Remove(ctx, containers...)
	}
	if network != "" {
		m.docker.NetworkRemove(ctx, network)
	}
}

// Install memasang aplikasi: pull image, jalankan container (+companion),
// daftarkan website proxy. Dijalankan async oleh API; status tercatat
// di instance ("installing" -> "running" / "error").
func (m *Manager) Install(ctx context.Context, appID, domain string, userVals map[string]string) (*Instance, error) {
	if !m.docker.Available(ctx) {
		return nil, ErrDockerUnavailable
	}
	m.mu.RLock()
	def, ok := m.catalog[appID]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("aplikasi tidak dikenal")
	}
	domain = sites.NormalizeDomain(domain)
	if !sites.ValidDomain(domain) {
		return nil, errors.New("nama domain tidak valid")
	}
	if _, exists := m.sites.Get(domain); exists {
		return nil, fmt.Errorf("domain %q sudah terdaftar di Websites", domain)
	}
	appEnv, err := resolveEnv(def.Env, userVals)
	if err != nil {
		return nil, err
	}
	hostPort, err := m.findFreePort()
	if err != nil {
		return nil, err
	}
	id := def.ID + "-" + randomSuffix()

	in := &Instance{
		ID:        id,
		AppID:     def.ID,
		Name:      def.Name + " (" + domain + ")",
		Domain:    domain,
		HostPort:  hostPort,
		Status:    "installing",
		CreatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	m.instances[id] = in
	_ = m.saveLocked()
	m.mu.Unlock()

	go m.doInstall(context.Background(), in, def, appEnv)
	cp := *in
	return &cp, nil
}

// setStatus mengupdate status instance secara thread-safe.
func (m *Manager) setStatus(id, status, statusErr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[id]; ok {
		in.Status = status
		in.StatusError = statusErr
		_ = m.saveLocked()
	}
}

// doInstall adalah pekerja async untuk Install.
func (m *Manager) doInstall(ctx context.Context, in *Instance, def AppDef, appEnv map[string]string) {
	fail := func(err error) {
		m.setStatus(in.ID, "error", err.Error())
	}
	// 1. Pull image aplikasi + companion. Bila pull gagal tapi image
	// sudah ada di lokal, lanjutkan dengan image lokal itu.
	pullOrLocal := func(image string) error {
		if err := m.docker.Pull(ctx, image); err != nil {
			if m.docker.ImageExists(ctx, image) {
				return nil
			}
			return fmt.Errorf("gagal pull image %s: %w", image, err)
		}
		return nil
	}
	if err := pullOrLocal(def.Image); err != nil {
		fail(err)
		return
	}
	for _, c := range def.Companions {
		if err := pullOrLocal(c.Image); err != nil {
			fail(err)
			return
		}
	}

	var createdContainers []string
	var network string
	rollback := func() { m.rollbackBestEffort(ctx, createdContainers, network) }

	// 2. Network privat bila ada companion.
	if len(def.Companions) > 0 {
		network = "mp-" + in.ID
		if err := m.docker.NetworkCreate(ctx, network); err != nil {
			fail(fmt.Errorf("gagal buat network: %w", err))
			return
		}
	}

	// 3. Jalankan companion dulu (mis. database).
	var volumes []string
	for _, c := range def.Companions {
		cEnv, err := resolveCompanionEnv(c, appEnv, nil)
		if err != nil {
			rollback()
			fail(err)
			return
		}
		var vols []string
		for _, v := range c.Volumes {
			vn := "mp-" + in.ID + "-" + v.Name
			vols = append(vols, vn+":"+v.ContainerPath)
			volumes = append(volumes, vn)
		}
		cname := in.ID + "-" + c.ID
		// Daftarkan SEBELUM Run: bila run gagal di tengah, container
		// mungkin sudah terlanjur dibuat (state "created") dan harus
		// tetap dibersihkan rollback.
		createdContainers = append(createdContainers, cname)
		if _, err := m.docker.Run(ctx, RunOptions{
			Name:         cname,
			Image:        c.Image,
			Network:      network,
			NetworkAlias: c.NetworkAlias,
			Env:          cEnv,
			Volumes:      vols,
			Labels:       map[string]string{"minipanel.instance": in.ID},
		}); err != nil {
			rollback()
			fail(fmt.Errorf("gagal menjalankan %s: %w", c.ID, err))
			return
		}
	}

	// 4. Jalankan container utama dengan port publish ke 127.0.0.1.
	var vols []string
	for _, v := range def.Volumes {
		vn := "mp-" + in.ID + "-" + v.Name
		vols = append(vols, vn+":"+v.ContainerPath)
		volumes = append(volumes, vn)
	}
	mainName := in.ID + "-app"
	publish := fmt.Sprintf("127.0.0.1:%d:%d", in.HostPort, def.ContainerPort)
	// Daftarkan sebelum Run — alasan sama seperti companion di atas.
	createdContainers = append(createdContainers, mainName)
	if _, err := m.docker.Run(ctx, RunOptions{
		Name:    mainName,
		Image:   def.Image,
		Command: def.Command,
		Publish: publish,
		Network: network,
		Env:     appEnv,
		Volumes: vols,
		Labels:  map[string]string{"minipanel.instance": in.ID},
	}); err != nil {
		rollback()
		fail(fmt.Errorf("gagal menjalankan aplikasi: %w", err))
		return
	}

	// 5. Daftarkan website tipe proxy.
	if _, err := m.sites.AddProxy(in.Domain, in.HostPort); err != nil {
		rollback()
		fail(fmt.Errorf("gagal mendaftarkan website: %w", err))
		return
	}

	m.mu.Lock()
	if cur, ok := m.instances[in.ID]; ok {
		cur.Containers = createdContainers
		cur.Volumes = volumes
		cur.Network = network
		cur.Status = "running"
		cur.StatusError = ""
		_ = m.saveLocked()
	}
	m.mu.Unlock()
}

// containerNames mengembalikan nama container instance (dari data tersimpan).
func (m *Manager) containerNames(id string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	in, ok := m.instances[id]
	if !ok {
		return nil, errors.New("instance tidak ditemukan")
	}
	return append([]string{}, in.Containers...), nil
}

// Start menjalankan kembali container instance yang berhenti.
func (m *Manager) Start(ctx context.Context, id string) error {
	if !m.docker.Available(ctx) {
		return ErrDockerUnavailable
	}
	names, err := m.containerNames(id)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("instance belum selesai di-install atau gagal")
	}
	return m.docker.Start(ctx, names...)
}

// Stop menghentikan container instance.
func (m *Manager) Stop(ctx context.Context, id string) error {
	if !m.docker.Available(ctx) {
		return ErrDockerUnavailable
	}
	names, err := m.containerNames(id)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("instance belum selesai di-install atau gagal")
	}
	return m.docker.Stop(ctx, names...)
}

// Uninstall menghapus total instance: container, network, volume,
// dan entri website proxy-nya. Data di volume ikut terhapus.
func (m *Manager) Uninstall(ctx context.Context, id string) error {
	if !m.docker.Available(ctx) {
		return ErrDockerUnavailable
	}
	m.mu.RLock()
	in, ok := m.instances[id]
	m.mu.RUnlock()
	if !ok {
		return errors.New("instance tidak ditemukan")
	}
	var errs []string
	names := in.Containers
	if len(names) == 0 {
		// Instance gagal install mungkin tak sempat menyimpan nama
		// container — cari lewat label sebagai cadangan.
		if cs, err := m.docker.PsInstance(ctx, id); err == nil {
			for _, c := range cs {
				names = append(names, c.Name)
			}
		}
	}
	if len(names) > 0 {
		if err := m.docker.Remove(ctx, names...); err != nil {
			errs = append(errs, "hapus container: "+err.Error())
		}
	}
	if in.Network != "" {
		if err := m.docker.NetworkRemove(ctx, in.Network); err != nil {
			errs = append(errs, "hapus network: "+err.Error())
		}
	}
	for _, v := range in.Volumes {
		if err := m.docker.VolumeRemove(ctx, v); err != nil {
			errs = append(errs, "hapus volume "+v+": "+err.Error())
		}
	}
	// Hapus website proxy-nya (abaikan bila sudah dihapus manual).
	_ = m.sites.Delete(in.Domain)
	m.mu.Lock()
	delete(m.instances, id)
	_ = m.saveLocked()
	m.mu.Unlock()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// InstanceStatus menggabungkan info instance dengan status container
// terkini dari docker.
type InstanceStatus struct {
	Instance   *Instance
	Containers []Container
	Running    int // jumlah container berstatus running
}

// Status mengambil status terkini semua instance.
func (m *Manager) Status(ctx context.Context) []InstanceStatus {
	list := m.List()
	byInst := map[string][]Container{}
	if m.docker.Available(ctx) {
		if cs, err := m.docker.Ps(ctx); err == nil {
			for _, c := range cs {
				// Nama container selalu "<instance-id>-..." — cocokkan prefix.
				for _, in := range list {
					if strings.HasPrefix(c.Name, in.ID+"-") || c.Name == in.ID {
						byInst[in.ID] = append(byInst[in.ID], c)
						break
					}
				}
			}
		}
	}
	out := make([]InstanceStatus, 0, len(list))
	for _, in := range list {
		cs := byInst[in.ID]
		running := 0
		for _, c := range cs {
			if c.State == "running" {
				running++
			}
		}
		out = append(out, InstanceStatus{Instance: in, Containers: cs, Running: running})
	}
	return out
}
