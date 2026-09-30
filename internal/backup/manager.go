package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"minipanel/internal/databases"
	"minipanel/internal/sites"
)

// Manager mengorkestrasi job backup: CRUD, scheduler cron in-process,
// eksekusi backup, retensi, dan restore. Toggle enabled langsung berlaku
// (entry cron ditambah/dihapus saat itu juga, tanpa restart panel).
type Manager struct {
	store   *Store
	dir     string // <data-dir>/backups
	sites   *sites.Store
	db      *databases.Manager
	cron    *cron.Cron
	mu      sync.Mutex
	entries map[string]cron.EntryID
	running map[string]bool
}

// New membuat Manager. dataDir adalah direktori data panel; file definisi
// job di <dataDir>/backups.json dan hasil backup di <dataDir>/backups/.
func New(dataDir string, siteStore *sites.Store, dbMgr *databases.Manager) (*Manager, error) {
	store, err := NewStore(dataDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{
		store:   store,
		dir:     dir,
		sites:   siteStore,
		db:      dbMgr,
		cron:    cron.New(),
		entries: make(map[string]cron.EntryID),
		running: make(map[string]bool),
	}, nil
}

// Start mendaftarkan semua job yang enabled ke scheduler dan memulai cron.
// Dipanggil sekali dari main setelah New.
func (m *Manager) Start() {
	for _, j := range m.store.List() {
		if j.Enabled {
			m.schedule(j)
		}
	}
	m.cron.Start()
}

// Stop menghentikan scheduler (job yang sedang berjalan tidak dipaksa berhenti).
func (m *Manager) Stop() {
	ctx := m.cron.Stop()
	<-ctx.Done()
}

// schedule mendaftarkan job ke cron (mengganti entry lama bila ada).
func (m *Manager) schedule(j Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.entries[j.ID]; ok {
		m.cron.Remove(id)
		delete(m.entries, j.ID)
	}
	if !j.Enabled {
		return
	}
	jobID := j.ID
	entryID, err := m.cron.AddFunc(j.Schedule, func() { m.runJob(jobID) })
	if err != nil {
		// Schedule sudah divalidasi saat job dibuat/diubah; bila tetap
		// gagal, jangan crash — job hanya tidak terjadwal.
		return
	}
	m.entries[j.ID] = entryID
}

// unschedule menghapus job dari cron.
func (m *Manager) unschedule(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entryID, ok := m.entries[id]; ok {
		m.cron.Remove(entryID)
		delete(m.entries, id)
	}
}

// ListJobs mengembalikan semua job.
func (m *Manager) ListJobs() []Job { return m.store.List() }

// GetJob mengambil satu job.
func (m *Manager) GetJob(id string) (Job, bool) { return m.store.Get(id) }

// validateCross memeriksa field yang bergantung package lain:
// domain harus terdaftar (untuk target website), database harus valid
// dan fitur DB terkonfigurasi (untuk target database).
func (m *Manager) validateCross(j *Job) error {
	if j.TargetType == TargetWebsite || j.TargetType == TargetBoth {
		j.Domain = sites.NormalizeDomain(j.Domain)
		site, ok := m.sites.Get(j.Domain)
		if !ok {
			return fmt.Errorf("domain %q tidak terdaftar di Websites", j.Domain)
		}
		if site.TargetType == sites.TargetProxy {
			return fmt.Errorf("domain %q adalah website tipe proxy (tidak punya document root untuk di-backup)", j.Domain)
		}
	}
	if j.TargetType == TargetDatabase || j.TargetType == TargetBoth {
		j.Database = strings.TrimSpace(j.Database)
		if !databases.ValidIdentifier(j.Database) {
			return errors.New("nama database tidak valid (hanya huruf, angka, underscore; maks 64 karakter)")
		}
		if databases.IsSystemDB(j.Database) {
			return errors.New("database sistem tidak boleh di-backup dari panel")
		}
		if !m.db.Configured() {
			return errors.New("fitur database belum dikonfigurasi (set MINIPANEL_DB_USER dan MINIPANEL_DB_PASS)")
		}
	}
	return nil
}

// CreateJob memvalidasi & menyimpan job baru, lalu menjadwalkannya
// bila enabled.
func (m *Manager) CreateJob(j Job) (Job, error) {
	j.ID = ""
	j.LastRun = nil
	j.LastStatus = ""
	if err := validateBasic(&j); err != nil {
		return Job{}, err
	}
	if err := m.validateCross(&j); err != nil {
		return Job{}, err
	}
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	j.ID = id
	j.CreatedAt = time.Now().UTC()
	if err := m.store.Add(j); err != nil {
		return Job{}, err
	}
	if j.Enabled {
		m.schedule(j)
	}
	return j, nil
}

// UpdateJob mengubah konfigurasi job (termasuk toggle enabled) dan
// langsung menyelaraskan scheduler — tanpa restart.
func (m *Manager) UpdateJob(id string, j Job) (Job, error) {
	j.ID = id
	if err := validateBasic(&j); err != nil {
		return Job{}, err
	}
	if err := m.validateCross(&j); err != nil {
		return Job{}, err
	}
	if err := m.store.Update(j); err != nil {
		return Job{}, err
	}
	updated, _ := m.store.Get(id)
	m.schedule(updated)
	return updated, nil
}

// DeleteJob menghapus definisi job dan entry cron-nya. File backup yang
// sudah ada di disk TIDAK ikut dihapus (konsisten dengan Websites).
func (m *Manager) DeleteJob(id string) error {
	if err := m.store.Delete(id); err != nil {
		return err
	}
	m.unschedule(id)
	return nil
}

// jobDir mengembalikan direktori file backup satu job.
func (m *Manager) jobDir(id string) string { return filepath.Join(m.dir, id) }

// RunNow menjalankan backup sekarang di goroutine terpisah (async).
// Status hasil terlihat di last_status job.
func (m *Manager) RunNow(id string) error {
	if _, ok := m.store.Get(id); !ok {
		return ErrNotFound
	}
	m.mu.Lock()
	if m.running[id] {
		m.mu.Unlock()
		return ErrRunning
	}
	m.mu.Unlock()
	go m.runJob(id)
	return nil
}

// runJob menjalankan satu job (dipakai scheduler & RunNow).
// Job yang sama tidak pernah berjalan bersamaan.
func (m *Manager) runJob(id string) {
	m.mu.Lock()
	if m.running[id] {
		m.mu.Unlock()
		return
	}
	m.running[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
	}()

	job, ok := m.store.Get(id)
	if !ok {
		return
	}
	start := time.Now().UTC()
	m.store.SetRunState(id, start, "running")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err := m.execute(ctx, job)

	status := "ok"
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		status = "error: " + msg
	}
	m.store.SetRunState(id, start, status)
}

// execute melakukan backup sesuai target job dan menerapkan retensi
// per tipe file yang berhasil dibuat.
func (m *Manager) execute(ctx context.Context, job Job) error {
	dir := m.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := time.Now().Format("20060102-150405")
	var errs []string

	if job.TargetType == TargetWebsite || job.TargetType == TargetBoth {
		if err := m.backupWebsite(job, dir, base); err != nil {
			errs = append(errs, "website: "+err.Error())
		} else if err := pruneRetention(dir, TargetWebsite, job.Retention); err != nil {
			errs = append(errs, "retensi website: "+err.Error())
		}
	}
	if job.TargetType == TargetDatabase || job.TargetType == TargetBoth {
		if err := m.backupDatabase(ctx, job, dir, base); err != nil {
			errs = append(errs, "database: "+err.Error())
		} else if err := pruneRetention(dir, TargetDatabase, job.Retention); err != nil {
			errs = append(errs, "retensi database: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// backupWebsite mengarsipkan document root domain job menjadi tar.gz.
func (m *Manager) backupWebsite(job Job, dir, base string) error {
	site, ok := m.sites.Get(job.Domain)
	if !ok {
		return fmt.Errorf("domain %q tidak lagi terdaftar", job.Domain)
	}
	if site.TargetType == sites.TargetProxy {
		return fmt.Errorf("domain %q adalah website tipe proxy (tidak punya document root)", job.Domain)
	}
	dest := uniquePath(dir, base, siteSuffix)
	if err := createTarGz(site.Root, dest); err != nil {
		return err
	}
	return nil
}

// backupDatabase mendump database job menjadi file .sql lewat koneksi Go.
func (m *Manager) backupDatabase(ctx context.Context, job Job, dir, base string) error {
	db, err := m.db.Connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	dest := uniquePath(dir, base, dbSuffix)
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	dumpErr := dumpDatabase(ctx, db, job.Database, f)
	closeErr := f.Close()
	if dumpErr != nil {
		os.Remove(tmp)
		return dumpErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dest)
}

// ListFiles mengembalikan daftar file backup satu job.
func (m *Manager) ListFiles(jobID string) ([]FileInfo, error) {
	if _, ok := m.store.Get(jobID); !ok {
		return nil, ErrNotFound
	}
	return listFiles(m.jobDir(jobID))
}

// FilePath memvalidasi nama file secara ketat dan mengembalikan path-nya
// untuk download. Traversal selalu ditolak.
func (m *Manager) FilePath(jobID, name string) (string, error) {
	if _, ok := m.store.Get(jobID); !ok {
		return "", ErrNotFound
	}
	p, err := resolveFilePath(m.jobDir(jobID), name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", ErrNotFound
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotFound
	}
	return p, nil
}

// DeleteFile menghapus satu file backup.
func (m *Manager) DeleteFile(jobID, name string) error {
	p, err := m.FilePath(jobID, name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Restore mengembalikan satu file backup ke target job-nya:
// website -> ekstrak tar.gz menimpa document root domain job;
// database -> eksekusi file .sql ke server (database dari isi file,
// yang selalu database job ini saat dibuat).
// AKSI DESTRUKTIF: menimpa data saat ini.
func (m *Manager) Restore(jobID, name string) error {
	job, ok := m.store.Get(jobID)
	if !ok {
		return ErrNotFound
	}
	p, err := m.FilePath(jobID, name)
	if err != nil {
		return err
	}
	switch kindOf(name) {
	case TargetWebsite:
		site, ok := m.sites.Get(job.Domain)
		if !ok {
			return fmt.Errorf("domain %q tidak lagi terdaftar", job.Domain)
		}
		if site.TargetType == sites.TargetProxy {
			return fmt.Errorf("domain %q adalah website tipe proxy (tidak punya document root)", job.Domain)
		}
		return extractTarGz(p, site.Root)
	case TargetDatabase:
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		db, err := m.db.Connect(ctx)
		if err != nil {
			return err
		}
		defer db.Close()
		return restoreDatabase(ctx, db, content)
	}
	return ErrEscape
}
