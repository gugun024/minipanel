// Package files menyediakan operasi file yang di-jail ke satu
// direktori root. Semua path dari user dinormalisasi dan dipastikan
// tidak keluar dari root (proteksi path traversal).
package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Batasan ukuran agar server tidak kehabisan memori.
const (
	MaxEditSize   = 2 << 20  // 2 MB untuk baca/tulis via editor
	MaxUploadSize = 50 << 20 // 50 MB per upload
)

// ErrEscape dikembalikan bila path mencoba keluar dari root.
var ErrEscape = errors.New("path di luar direktori yang diizinkan")

// Entry adalah satu file/direktori hasil listing.
type Entry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Manager mengelola operasi file di dalam root.
type Manager struct {
	root string
}

// New membuat Manager dengan root yang sudah di-absolutkan.
// Root harus berupa direktori yang ada.
func New(root string) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("root bukan direktori: " + abs)
	}
	return &Manager{root: abs}, nil
}

// Root mengembalikan direktori root absolut.
func (m *Manager) Root() string { return m.root }

// resolve mengubah path relatif dari user menjadi path absolut
// di dalam root. Menolak ".." secara eksplisit (403) dan menjangkarkan
// sisa path ke root sehingga traversal tidak mungkin lolos.
func (m *Manager) resolve(p string) (string, error) {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "", ErrEscape
		}
	}
	// Awali dengan "/" agar Clean tidak menghasilkan path relatif
	// yang bisa lolos dari Join (defense in depth).
	cleaned := filepath.Clean("/" + p)
	full := filepath.Join(m.root, cleaned)
	rel, err := filepath.Rel(m.root, full)
	if err != nil {
		return "", ErrEscape
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrEscape
	}
	return full, nil
}

// List mengembalikan isi direktori (direktori dulu, lalu alfabetis).
func (m *Manager) List(p string) ([]Entry, error) {
	dir, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// Read membaca isi file teks (maks MaxEditSize).
func (m *Manager) Read(p string) ([]byte, error) {
	full, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("path adalah direktori")
	}
	if st.Size() > MaxEditSize {
		return nil, errors.New("file terlalu besar untuk diedit di browser")
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, errors.New("file biner tidak bisa diedit di browser")
	}
	return b, nil
}

// Write menulis (atau membuat) file teks. Direktori induk dibuat otomatis.
func (m *Manager) Write(p string, content []byte) error {
	if len(content) > MaxEditSize {
		return errors.New("konten melebihi batas 2 MB")
	}
	full, err := m.resolve(p)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(full); dir != m.root {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(full, content, 0o644)
}

// Mkdir membuat direktori (rekursif).
func (m *Manager) Mkdir(p string) error {
	full, err := m.resolve(p)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0o755)
}

// Rename memindahkan/mengganti nama file atau direktori.
func (m *Manager) Rename(oldP, newP string) error {
	oldFull, err := m.resolve(oldP)
	if err != nil {
		return err
	}
	newFull, err := m.resolve(newP)
	if err != nil {
		return err
	}
	if oldFull == m.root || newFull == m.root {
		return errors.New("tidak boleh mengubah root direktori")
	}
	return os.Rename(oldFull, newFull)
}

// Delete menghapus file atau direktori (rekursif). Root tidak boleh dihapus.
func (m *Manager) Delete(p string) error {
	full, err := m.resolve(p)
	if err != nil {
		return err
	}
	if full == m.root {
		return errors.New("tidak boleh menghapus root direktori")
	}
	return os.RemoveAll(full)
}

// SaveUpload menyimpan file upload ke direktori tujuan.
func (m *Manager) SaveUpload(dir string, name string, src io.Reader) error {
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "" || name == "." || name == ".." {
		return errors.New("nama file tidak valid")
	}
	target, err := m.resolve(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, io.LimitReader(src, MaxUploadSize+1))
	return err
}
