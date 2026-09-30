// Package terminal menyediakan terminal web interaktif:
// endpoint WebSocket yang menghubungkan browser (xterm.js) ke shell
// interaktif lewat PTY (github.com/creack/pty + github.com/gorilla/websocket).
//
// KEAMANAN: terminal = akses shell PENUH setara user yang menjalankan
// proses minipanel. Bila panel jalan sebagai root, terminal ini root.
// Karena itu endpoint hanya untuk user yang sudah login.
package terminal

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"minipanel/internal/auth"
)

const (
	defaultCols = 80
	defaultRows = 24
	maxDim      = 500 // batas wajar cols/rows dari client
)

// resizeMsg adalah pesan kontrol dari browser untuk mengubah ukuran PTY.
type resizeMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// CheckOrigin ketat: Origin (bila ada) harus satu host dengan panel.
	// Request tanpa Origin (client non-browser, mis. alat test) diizinkan —
	// autentikasi tetap dijaga cookie session.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

// Handler mengembalikan handler untuk GET /ws/terminal.
// Session dicek di sini (401 sebelum upgrade) — sama seperti endpoint
// API lain; pemanggil juga boleh membungkusnya dengan auth.Middleware.
func Handler(store *auth.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if !store.Valid(auth.TokenFromRequest(r)) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		serve(w, r)
	})
}

// pickShell memilih shell interaktif: $SHELL, lalu /bin/bash, lalu /bin/sh.
func pickShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	for _, cand := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/bin/sh"
}

func clampDim(v int, def int) uint16 {
	if v <= 0 {
		return uint16(def)
	}
	if v > maxDim {
		return uint16(maxDim)
	}
	return uint16(v)
}

// serve menjalankan satu sesi terminal: satu koneksi WebSocket = satu
// proses shell + satu PTY. Saat koneksi putus atau shell exit, proses
// dibunuh dan di-reap (cmd.Wait) agar tidak meninggalkan zombie.
func serve(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("terminal: upgrade gagal: %v", err)
		return
	}
	defer conn.Close()

	cols := clampDim(atoiSafe(r.URL.Query().Get("cols")), defaultCols)
	rows := clampDim(atoiSafe(r.URL.Query().Get("rows")), defaultRows)

	shell := pickShell()
	cmd := exec.Command(shell)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Pastikan shell interaktif login sederhana tidak mengunci bila
	// dijalankan dari direktori yang hilang.
	cmd.Dir = filepath.Clean(cmd.Dir)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		log.Printf("terminal: gagal start PTY: %v", err)
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n[ gagal memulai shell: "+err.Error()+" ]\r\n"))
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return
	}

	var once sync.Once
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }

	// Reaper: saat sesi selesai, tutup PTY dan bunuh shell;
	// goroutine Wait di bawah yang me-reap prosesnya (anti zombie).
	go func() {
		<-done
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	go func() {
		_ = cmd.Wait() // reap; error wajar bila proses di-kill
		finish()
	}()

	// Pump output: PTY -> channel -> satu-satunya penulis WebSocket
	// (gorilla/websocket hanya mengizinkan satu penulis konkuren).
	outCh := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				select {
				case outCh <- data:
				case <-done:
					return
				}
			}
			if err != nil {
				finish()
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case data := <-outCh:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
					finish()
					return
				}
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil,
					time.Now().Add(5*time.Second)); err != nil {
					finish()
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Loop input: WebSocket -> PTY. Pesan teks JSON {"type":"resize"}
	// dipakai untuk resize; pesan lain (teks/biner) diteruskan apa
	// adanya sebagai input keyboard.
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			continue
		}
		if mt == websocket.TextMessage && len(msg) > 0 && msg[0] == '{' {
			var rm resizeMsg
			if err := json.Unmarshal(msg, &rm); err == nil && rm.Type == "resize" {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Cols: clampDim(rm.Cols, defaultCols),
					Rows: clampDim(rm.Rows, defaultRows),
				})
				continue
			}
		}
		if _, err := ptmx.Write(msg); err != nil {
			break
		}
	}
	finish()
	// Beri waktu reaper membunuh & me-reap shell sebelum handler selesai.
	select {
	case <-time.After(300 * time.Millisecond):
	case <-done:
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0
		}
	}
	return n
}
