// Package backup mengelola backup terjadwal: arsip website (tar.gz) dan
// dump database (SQL), dengan scheduler cron in-process. Definisi job
// disimpan di file JSON di direktori data panel (pola yang sama dengan
// package sites): <data-dir>/backups.json, ditulis atomik. File hasil
// backup disimpan di <data-dir>/backups/<job-id>/.
package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Target type yang didukung.
const (
	TargetWebsite  = "website"
	TargetDatabase = "database"
	TargetBoth     = "both"
)

var (
	// ErrNotFound dikembalikan bila job tidak ditemukan.
	ErrNotFound = errors.New("job backup tidak ditemukan")
	// ErrRunning dikembalikan bila job sedang berjalan.
	ErrRunning = errors.New("backup sedang berjalan untuk job ini")
	// ErrEscape dikembalikan bila nama file mencoba keluar dari jail.
	ErrEscape = errors.New("path tidak valid")
)

// Job adalah satu definisi backup terjadwal.
type Job struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Schedule   string     `json:"schedule"`    // cron expression 5 field, mis. "0 2 * * *"
	TargetType string     `json:"target_type"` // website | database | both
	Domain     string     `json:"domain,omitempty"`
	Database   string     `json:"database,omitempty"`
	Retention  int        `json:"retention"` // jumlah backup terakhir per tipe yang disimpan
	CreatedAt  time.Time  `json:"created_at"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"` // "ok" | "running" | "error: ..."
}

// clone menyalin job (termasuk pointer LastRun) agar aman dibagikan.
func (j *Job) clone() Job {
	c := *j
	if j.LastRun != nil {
		t := *j.LastRun
		c.LastRun = &t
	}
	return c
}

// cronParser memakai format standar 5 field (+ descriptor seperti @daily),
// sama dengan parser default scheduler robfig/cron.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ValidSchedule mengecek cron expression.
func ValidSchedule(spec string) bool {
	_, err := cronParser.Parse(strings.TrimSpace(spec))
	return err == nil
}

// validateBasic mengecek field yang tidak butuh akses ke package lain.
// Validasi silang (domain terdaftar, DB terkonfigurasi) ada di Manager.
func validateBasic(j *Job) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" {
		return errors.New("nama job tidak boleh kosong")
	}
	if len(j.Name) > 100 {
		return errors.New("nama job terlalu panjang (maks 100 karakter)")
	}
	if strings.ContainsAny(j.Name, "/\\\x00") {
		return errors.New("nama job mengandung karakter tidak valid")
	}
	j.Schedule = strings.TrimSpace(j.Schedule)
	if !ValidSchedule(j.Schedule) {
		return fmt.Errorf("jadwal cron tidak valid: %q (format: menit jam tanggal bulan hari, mis. \"0 2 * * *\")", j.Schedule)
	}
	switch j.TargetType {
	case TargetWebsite, TargetDatabase, TargetBoth:
	default:
		return errors.New("target_type harus salah satu dari: website, database, both")
	}
	if j.Retention == 0 {
		j.Retention = 7
	}
	if j.Retention < 1 || j.Retention > 365 {
		return errors.New("retention harus antara 1 dan 365")
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Store menyimpan definisi job di file JSON dengan akses aman-concurrent.
type Store struct {
	mu   sync.RWMutex
	path string
	jobs map[string]*Job
}

// NewStore membuka (atau membuat) store di dataDir/backups.json.
func NewStore(dataDir string) (*Store, error) {
	s := &Store{
		path: filepath.Join(dataDir, "backups.json"),
		jobs: make(map[string]*Job),
	}
	if b, err := os.ReadFile(s.path); err == nil && len(b) > 0 {
		var list []Job
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, errors.New("gagal membaca backups.json: " + err.Error())
		}
		for i := range list {
			j := list[i]
			if j.ID != "" {
				jc := j
				s.jobs[j.ID] = &jc
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// saveLocked menulis seluruh daftar ke disk secara atomik (temp + rename).
func (s *Store) saveLocked() error {
	list := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j.clone())
	}
	sort.Slice(list, func(i, k int) bool {
		if !list[i].CreatedAt.Equal(list[k].CreatedAt) {
			return list[i].CreatedAt.Before(list[k].CreatedAt)
		}
		return list[i].Name < list[k].Name
	})
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

// List mengembalikan semua job (salinan) terurut waktu pembuatan.
func (s *Store) List() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.clone())
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].CreatedAt.Equal(out[k].CreatedAt) {
			return out[i].CreatedAt.Before(out[k].CreatedAt)
		}
		return out[i].Name < out[k].Name
	})
	return out
}

// Get mengambil satu job berdasarkan ID.
func (s *Store) Get(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return j.clone(), true
}

// Add menyimpan job baru (ID & CreatedAt harus sudah diisi pemanggil).
func (s *Store) Add(j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jc := j.clone()
	s.jobs[j.ID] = &jc
	if err := s.saveLocked(); err != nil {
		delete(s.jobs, j.ID)
		return err
	}
	return nil
}

// Update mengganti job yang ada. Field ID, CreatedAt, LastRun, LastStatus
// selalu dipertahankan dari job lama — pemanggil hanya mengubah konfigurasi.
func (s *Store) Update(j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.jobs[j.ID]
	if !ok {
		return ErrNotFound
	}
	j.ID = old.ID
	j.CreatedAt = old.CreatedAt
	j.LastRun = old.LastRun
	j.LastStatus = old.LastStatus
	jc := j.clone()
	s.jobs[j.ID] = &jc
	return s.saveLocked()
}

// Delete menghapus job dari store (file backup di disk TIDAK dihapus).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return ErrNotFound
	}
	delete(s.jobs, id)
	return s.saveLocked()
}

// SetRunState memperbarui last_run & last_status setelah job dijalankan.
func (s *Store) SetRunState(id string, at time.Time, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	t := at
	j.LastRun = &t
	j.LastStatus = status
	_ = s.saveLocked() // kegagalan simpan status tidak fatal
}
