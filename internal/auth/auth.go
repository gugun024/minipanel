// Package auth menangani login, hashing password (bcrypt),
// dan session berbasis cookie httpOnly.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// CookieName adalah nama cookie session.
	CookieName = "minipanel_session"
	// sessionTTL adalah masa berlaku session.
	sessionTTL = 12 * time.Hour
)

// Store menyimpan kredensial (hash bcrypt) dan session aktif di memori.
type Store struct {
	mu       sync.Mutex
	username string
	passHash []byte
	sessions map[string]time.Time
}

// New membuat Store baru dari username & password plaintext.
// Password langsung di-hash dengan bcrypt dan tidak disimpan.
func New(username, password string) (*Store, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Store{
		username: username,
		passHash: hash,
		sessions: make(map[string]time.Time),
	}, nil
}

// Check memverifikasi username & password.
func (s *Store) Check(username, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1
	passOK := bcrypt.CompareHashAndPassword(s.passHash, []byte(password)) == nil
	return userOK && passOK
}

// NewSession membuat token session acak dan menyimpannya.
func (s *Store) NewSession() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Bersihkan session yang sudah kedaluwarsa.
	now := time.Now()
	for t, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, t)
		}
	}
	s.sessions[tok] = now.Add(sessionTTL)
	return tok, nil
}

// Valid mengecek apakah token session masih berlaku.
func (s *Store) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

// Revoke menghapus session (logout).
func (s *Store) Revoke(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

// SetCookie menulis cookie session httpOnly ke response.
func SetCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// ClearCookie menghapus cookie session.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// TokenFromRequest membaca token session dari request.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// Middleware menolak request tanpa session valid (401).
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Valid(TokenFromRequest(r)) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginHandler menangani POST /api/login.
func (s *Store) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	// Password tidak pernah di-log.
	if !s.Check(req.Username, req.Password) {
		http.Error(w, `{"error":"username atau password salah"}`, http.StatusUnauthorized)
		return
	}
	tok, err := s.NewSession()
	if err != nil {
		http.Error(w, `{"error":"gagal membuat session"}`, http.StatusInternalServerError)
		return
	}
	SetCookie(w, tok)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// LogoutHandler menangani POST /api/logout.
func (s *Store) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	s.Revoke(TokenFromRequest(r))
	ClearCookie(w)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
