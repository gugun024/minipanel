package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Akhiran nama file per tipe backup. Nama file selalu berbentuk
// "<yyyymmdd-hhmmss>[-n]-site.tar.gz" atau "<yyyymmdd-hhmmss>[-n]-db.sql"
// sehingga urutan leksikografis = urutan kronologis.
const (
	siteSuffix = "-site.tar.gz"
	dbSuffix   = "-db.sql"
)

// FileInfo adalah metadata satu file backup.
type FileInfo struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // website | database
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// kindOf menentukan tipe backup dari nama file ("" bila tidak dikenal).
func kindOf(name string) string {
	switch {
	case strings.HasSuffix(name, siteSuffix):
		return TargetWebsite
	case strings.HasSuffix(name, dbSuffix):
		return TargetDatabase
	}
	return ""
}

// uniquePath mengembalikan path file yang belum ada: base+suffix, atau
// base+"-2"+suffix, "-3", ... bila sudah ada (dua run dalam detik sama).
func uniquePath(dir, base, suffix string) string {
	p := filepath.Join(dir, base+suffix)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, suffix))
	}
}

// listFiles membaca direktori satu job dan mengembalikan file backup-nya,
// terurut terbaru -> terlama.
func listFiles(dir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		kind := kindOf(e.Name())
		if kind == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{
			Name:    e.Name(),
			Kind:    kind,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name > out[k].Name })
	return out, nil
}

// pruneRetention menghapus file backup tipe tertentu yang melebihi jumlah
// retensi (yang terlama dihapus duluan).
func pruneRetention(dir, kind string, keep int) error {
	files, err := listFiles(dir)
	if err != nil {
		return err
	}
	var mine []FileInfo
	for _, f := range files {
		if f.Kind == kind {
			mine = append(mine, f)
		}
	}
	// mine sudah terurut terbaru -> terlama.
	for _, f := range mine[min(keep, len(mine)):] {
		if err := os.Remove(filepath.Join(dir, f.Name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// resolveFilePath memvalidasi nama file backup secara ketat dan
// mengembalikan path absolutnya di dalam direktori job. Nama harus berupa
// base name saja (tanpa separator), berakhiran yang dikenal, dan hasil
// join harus tetap di dalam dir — traversal selalu ditolak.
func resolveFilePath(dir, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") {
		return "", ErrEscape
	}
	if kindOf(name) == "" {
		return "", ErrEscape
	}
	p := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel != name {
		return "", ErrEscape
	}
	return p, nil
}

// createTarGz mengarsipkan isi srcDir menjadi file tar.gz di destPath
// (ditulis ke file sementara lalu di-rename agar atomik). Symlink disimpan
// sebagai symlink (tidak diikuti); file khusus (socket/fifo/device) dilewati.
func createTarGz(srcDir, destPath string) error {
	tmp := destPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == srcDir {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		info, err := d.Info() // untuk symlink: info link itu sendiri
		if err != nil {
			return err
		}
		var link string
		if d.Type()&fs.ModeSymlink != 0 {
			link, err = os.Readlink(p)
			if err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			if _, err := io.Copy(tw, in); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gagal mengarsipkan %s: %w", srcDir, err)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, destPath); err != nil {
		return err
	}
	ok = true
	return nil
}

// extractTarGz mengekstrak arsip tar.gz ke destDir, menimpa file yang sudah
// ada. Entri dengan path absolut / ".." ditolak (anti zip-slip). Symlink
// absolut dilewati demi keamanan; symlink relatif diekstrak apa adanya.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("file backup rusak (gzip): %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("file backup rusak (tar): %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return fmt.Errorf("entri tidak aman di arsip (%s): %w", hdr.Name, ErrEscape)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs.FileMode(hdr.Mode).Perm()|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			if hdr.ModTime.Year() > 1970 {
				_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) {
				continue // lewati symlink absolut demi keamanan
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Tipe lain (fifo, device, hardlink, dll.) dilewati.
		}
	}
}

// safeJoin menggabungkan root + nama entri arsip dan memastikan hasilnya
// tetap di dalam root.
func safeJoin(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("path absolut tidak diizinkan")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("path keluar dari direktori tujuan")
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path keluar dari direktori tujuan")
	}
	return target, nil
}
