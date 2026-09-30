// Package apps: one-click install aplikasi berbasis Docker.
//
// Prinsipnya: minipanel tidak perlu tahu isi aplikasi. Setiap aplikasi
// jalan sebagai container Docker yang listen di satu port; panel tinggal
// menjalankan container dan mem-proxy domain ke port itu (website tipe
// "proxy"). Docker diakses lewat CLI (exec), tanpa SDK berat.
package apps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ErrDockerUnavailable dikembalikan bila docker tidak tersedia di server.
var ErrDockerUnavailable = errors.New("docker tidak tersedia di server ini (install docker untuk memakai fitur Apps)")

// Label yang ditempel ke semua container yang dikelola panel, agar mudah
// diidentifikasi dan tidak mengganggu container lain milik user.
const (
	labelManaged  = "minipanel.managed=1"
	labelInstance = "minipanel.instance="
)

var (
	// containerNameRe: nama container yang aman untuk CLI docker.
	containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// envKeyRe: nama environment variable yang valid.
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// imageRe: nama image docker yang wajar (tanpa shell metachar).
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,255}$`)
	// volumeNameRe: nama docker volume.
	volumeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// labelKeyRe: key label docker (mendukung notasi domain berbalik, mis. minipanel.instance).
	labelKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)
)

// Docker membungkus docker CLI lewat exec (tanpa shell — semua argumen
// dilewatkan sebagai argv, sehingga injeksi perintah mustahil).
type Docker struct {
	bin string // path binary docker, "" bila tidak ditemukan
}

// NewDocker mencari binary docker di PATH.
func NewDocker() *Docker {
	bin, _ := exec.LookPath("docker")
	return &Docker{bin: bin}
}

// Available mengecek docker tersedia DAN daemon-nya merespons.
func (d *Docker) Available(ctx context.Context) bool {
	if d == nil || d.bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, d.bin, "info").Run() == nil
}

// run menjalankan docker dengan timeout dan mengembalikan stdout.
// Semua argumen divalidasi pemanggil; di sini tidak ada shell.
func (d *Docker) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if d.bin == "" {
		return "", ErrDockerUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// Potong output agar error tidak membanjiri log.
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", fmt.Errorf("docker %s gagal: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// Container adalah info ringkas satu container dari `docker ps`.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string // "running", "exited", ...
	Status string // teks human-readable docker
}

// Ps mengembalikan semua container yang dikelola panel (semua instance).
func (d *Docker) Ps(ctx context.Context) ([]Container, error) {
	out, err := d.run(ctx, 15*time.Second, "ps", "-a",
		"--filter", "label=minipanel.managed=1",
		"--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}")
	if err != nil {
		return nil, err
	}
	var list []Container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "|", 5)
		if len(f) != 5 {
			continue
		}
		list = append(list, Container{ID: f[0], Name: f[1], Image: f[2], State: f[3], Status: f[4]})
	}
	return list, nil
}

// PsInstance mengembalikan container milik satu instance.
func (d *Docker) PsInstance(ctx context.Context, instanceID string) ([]Container, error) {
	if !containerNameRe.MatchString(instanceID) {
		return nil, errors.New("id instance tidak valid")
	}
	out, err := d.run(ctx, 15*time.Second, "ps", "-a",
		"--filter", "label="+labelInstance+instanceID,
		"--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}")
	if err != nil {
		return nil, err
	}
	var list []Container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "|", 5)
		if len(f) != 5 {
			continue
		}
		list = append(list, Container{ID: f[0], Name: f[1], Image: f[2], State: f[3], Status: f[4]})
	}
	return list, nil
}

// Pull mengunduh image (bisa lama untuk image besar).
func (d *Docker) Pull(ctx context.Context, image string) error {
	if !imageRe.MatchString(image) {
		return errors.New("nama image tidak valid")
	}
	_, err := d.run(ctx, 10*time.Minute, "pull", image)
	return err
}

// ImageExists mengecek apakah image sudah ada di lokal.
func (d *Docker) ImageExists(ctx context.Context, image string) bool {
	if !imageRe.MatchString(image) {
		return false
	}
	_, err := d.run(ctx, 15*time.Second, "image", "inspect", image)
	return err == nil
}

// RunOptions adalah parameter `docker run`.
type RunOptions struct {
	Name          string   // nama container (divalidasi)
	Image         string   // image (divalidasi)
	Command       []string // override command (opsional)
	Publish       string   // mis. "127.0.0.1:10001:80" (opsional)
	Network       string   // nama network (opsional)
	NetworkAlias  string   // alias di network (opsional)
	Env           map[string]string
	Volumes       []string // format "volume-name:/container/path"
	Labels        map[string]string
	RestartPolicy string // default "unless-stopped"
}

// Run menjalankan container detached. Mengembalikan nama container.
func (d *Docker) Run(ctx context.Context, o RunOptions) (string, error) {
	if !containerNameRe.MatchString(o.Name) {
		return "", errors.New("nama container tidak valid")
	}
	if !imageRe.MatchString(o.Image) {
		return "", errors.New("nama image tidak valid")
	}
	for k := range o.Env {
		if !envKeyRe.MatchString(k) {
			return "", fmt.Errorf("nama env %q tidak valid", k)
		}
	}
	for _, v := range o.Volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) != 2 || !volumeNameRe.MatchString(parts[0]) || !strings.HasPrefix(parts[1], "/") {
			return "", fmt.Errorf("volume %q tidak valid", v)
		}
	}
	rp := o.RestartPolicy
	if rp == "" {
		rp = "unless-stopped"
	}
	args := []string{"run", "-d", "--name", o.Name,
		"--label", labelManaged,
		"--restart", rp,
	}
	for k, v := range o.Labels {
		if !labelKeyRe.MatchString(k) {
			return "", fmt.Errorf("nama label %q tidak valid", k)
		}
		if strings.ContainsAny(v, "\n\r") {
			return "", fmt.Errorf("nilai label %q tidak valid", k)
		}
		args = append(args, "--label", k+"="+v)
	}
	if o.Publish != "" {
		if !validPublish(o.Publish) {
			return "", fmt.Errorf("publish %q tidak valid", o.Publish)
		}
		args = append(args, "-p", o.Publish)
	}
	if o.Network != "" {
		if !containerNameRe.MatchString(o.Network) {
			return "", errors.New("nama network tidak valid")
		}
		args = append(args, "--network", o.Network)
		if o.NetworkAlias != "" {
			if !containerNameRe.MatchString(o.NetworkAlias) {
				return "", errors.New("network alias tidak valid")
			}
			args = append(args, "--network-alias", o.NetworkAlias)
		}
	}
	// Urutkan env agar deterministik (memudahkan testing & log).
	keys := make([]string, 0, len(o.Env))
	for k := range o.Env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+o.Env[k])
	}
	for _, v := range o.Volumes {
		args = append(args, "-v", v)
	}
	args = append(args, o.Image)
	args = append(args, o.Command...)
	out, err := d.run(ctx, 60*time.Second, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// validPublish memvalidasi format "127.0.0.1:PORT:PORT" atau "PORT:PORT".
func validPublish(p string) bool {
	parts := strings.Split(p, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "127.0.0.1" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprint(n) != part {
			return false
		}
	}
	return true
}

// Start menjalankan container yang sedang berhenti.
func (d *Docker) Start(ctx context.Context, names ...string) error {
	for _, n := range names {
		if !containerNameRe.MatchString(n) {
			return fmt.Errorf("nama container %q tidak valid", n)
		}
	}
	_, err := d.run(ctx, 60*time.Second, append([]string{"start"}, names...)...)
	return err
}

// Stop menghentikan container.
func (d *Docker) Stop(ctx context.Context, names ...string) error {
	for _, n := range names {
		if !containerNameRe.MatchString(n) {
			return fmt.Errorf("nama container %q tidak valid", n)
		}
	}
	_, err := d.run(ctx, 60*time.Second, append([]string{"stop"}, names...)...)
	return err
}

// Remove menghapus container (paksa).
func (d *Docker) Remove(ctx context.Context, names ...string) error {
	for _, n := range names {
		if !containerNameRe.MatchString(n) {
			return fmt.Errorf("nama container %q tidak valid", n)
		}
	}
	_, err := d.run(ctx, 60*time.Second, append([]string{"rm", "-f"}, names...)...)
	return err
}

// NetworkCreate membuat docker network.
func (d *Docker) NetworkCreate(ctx context.Context, name string) error {
	if !containerNameRe.MatchString(name) {
		return errors.New("nama network tidak valid")
	}
	_, err := d.run(ctx, 30*time.Second, "network", "create", name)
	return err
}

// NetworkRemove menghapus docker network.
func (d *Docker) NetworkRemove(ctx context.Context, name string) error {
	if !containerNameRe.MatchString(name) {
		return errors.New("nama network tidak valid")
	}
	_, err := d.run(ctx, 30*time.Second, "network", "rm", name)
	return err
}

// VolumeRemove menghapus named volume.
func (d *Docker) VolumeRemove(ctx context.Context, name string) error {
	if !volumeNameRe.MatchString(name) {
		return errors.New("nama volume tidak valid")
	}
	_, err := d.run(ctx, 30*time.Second, "volume", "rm", name)
	return err
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
