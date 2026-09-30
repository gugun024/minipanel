// Package services mengelola service sistem via systemctl.
// Nama service dibatasi pada daftar yang dikonfigurasi (whitelist)
// untuk mencegah penyalahgunaan perintah.
package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultServices adalah daftar service umum yang dipantau.
var DefaultServices = []string{
	"nginx", "apache2", "mysql", "mariadb", "php-fpm",
	"redis-server", "redis", "docker", "ssh", "sshd", "postgresql",
}

// ErrNoSystemctl dikembalikan bila systemctl tidak tersedia.
var ErrNoSystemctl = errors.New("systemctl tidak ditemukan di sistem ini")

// Service adalah status satu service.
type Service struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
}

// Manager mengatur daftar service yang diizinkan.
type Manager struct {
	names        []string
	allowed      map[string]bool
	hasSystemctl bool
}

// New membuat Manager. names boleh kosong (pakai DefaultServices).
func New(names []string) *Manager {
	if len(names) == 0 {
		names = DefaultServices
	}
	m := &Manager{names: names, allowed: make(map[string]bool, len(names))}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			m.allowed[n] = true
		}
	}
	_, err := exec.LookPath("systemctl")
	m.hasSystemctl = err == nil
	return m
}

// Available melaporkan apakah systemctl tersedia.
func (m *Manager) Available() bool { return m.hasSystemctl }

// run menjalankan systemctl dengan timeout.
func (m *Manager) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Status mengembalikan status semua service (paralel agar cepat).
func (m *Manager) Status(ctx context.Context) ([]Service, error) {
	if !m.hasSystemctl {
		return nil, ErrNoSystemctl
	}
	out := make([]Service, len(m.names))
	var wg sync.WaitGroup
	for i, name := range m.names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			s := Service{Name: name}
			if _, err := m.run(ctx, "is-active", "--quiet", name); err == nil {
				s.Active = true
			}
			if o, err := m.run(ctx, "is-enabled", "--quiet", name); err == nil && o != "" {
				s.Enabled = true
			}
			out[i] = s
		}(i, name)
	}
	wg.Wait()
	return out, nil
}

// validAction memastikan aksi yang diminta aman.
func validAction(a string) bool {
	switch a {
	case "start", "stop", "restart", "reload":
		return true
	}
	return false
}

// Action menjalankan start/stop/restart/reload pada service.
// Hanya service dalam whitelist yang boleh dioperasikan.
func (m *Manager) Action(ctx context.Context, name, action string) error {
	if !m.hasSystemctl {
		return ErrNoSystemctl
	}
	if !m.allowed[name] {
		return fmt.Errorf("service %q tidak ada dalam daftar yang diizinkan", name)
	}
	if !validAction(action) {
		return fmt.Errorf("aksi %q tidak valid", action)
	}
	out, err := m.run(ctx, action, name)
	if err != nil {
		if out != "" {
			return fmt.Errorf("systemctl %s %s gagal: %s", action, name, out)
		}
		return fmt.Errorf("systemctl %s %s gagal (butuh akses root?)", action, name)
	}
	return nil
}
