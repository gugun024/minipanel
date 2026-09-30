// Package certs: CertManager membungkus autocert (Let's Encrypt)
// untuk penerbitan SSL otomatis, routing HTTPS per-domain,
// dan pembacaan status sertifikat dari cache disk.
package sites

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// acmeStagingURL adalah directory endpoint Let's Encrypt staging.
// Dipakai default agar aman dari rate limit saat development/testing.
const acmeStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// CertManager mengatur penerbitan & penyajian sertifikat TLS otomatis.
type CertManager struct {
	mgr   *autocert.Manager
	cache autocert.DirCache
	store *Store
}

// NewCertManager membuat CertManager. staging=true memakai Let's Encrypt
// staging (sertifikat TIDAK dipercaya browser, aman untuk dev).
// HostPolicy memastikan sertifikat hanya diterbitkan untuk domain yang
// terdaftar user — tidak bisa dipakai untuk domain sembarang.
func NewCertManager(store *Store, cacheDir string, staging bool) (*CertManager, error) {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	dirURL := acme.LetsEncryptURL
	if staging {
		dirURL = acmeStagingURL
	}
	mgr := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(cacheDir),
		HostPolicy: func(_ context.Context, host string) error {
			if _, ok := store.Get(host); ok {
				return nil
			}
			return fmt.Errorf("host %q tidak terdaftar di minipanel", host)
		},
		Client: &acme.Client{DirectoryURL: dirURL},
	}
	return &CertManager{mgr: mgr, cache: autocert.DirCache(cacheDir), store: store}, nil
}

// TLSConfig mengembalikan konfigurasi TLS untuk server HTTPS.
// Sertifikat diterbitkan lazy saat handshake pertama (atau diambil dari cache).
func (c *CertManager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: c.mgr.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// canonicalHost menormalkan Host header: buang port, huruf kecil,
// tanpa trailing dot.
func canonicalHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return NormalizeDomain(h)
}

// HTTPHandler untuk port 80: meneruskan ACME HTTP-01 challenge ke autocert,
// semua request lain di-redirect permanen ke HTTPS.
func (c *CertManager) HTTPHandler(httpsPort string) http.Handler {
	challenge := c.mgr.HTTPHandler(nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			challenge.ServeHTTP(w, r)
			return
		}
		host := canonicalHost(r.Host)
		if host == "" || !ValidDomain(host) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		target := "https://" + host
		if httpsPort != "" && httpsPort != "443" {
			target += ":" + httpsPort
		}
		target += r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// HTTPSHandler untuk port 443: routing berdasarkan Host header.
// Website "static" -> serve file dari document root.
// Website "proxy"  -> teruskan request ke 127.0.0.1:port (reverse proxy,
// mendukung WebSocket upgrade). Host tak dikenal -> 404.
func (c *CertManager) HTTPSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site, ok := c.store.Get(canonicalHost(r.Host))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if site.TargetType == TargetProxy {
			proxyToLocalhost(w, r, site.ProxyPort)
			return
		}
		// http.FileServer membersihkan path dan menolak ".." dengan sendirinya.
		http.FileServer(http.Dir(site.Root)).ServeHTTP(w, r)
	})
}

// proxyTransport dipakai ulang untuk semua reverse proxy agar koneksi
// keep-alive ke aplikasi lokal efisien.
var proxyTransport = &http.Transport{
	MaxIdleConns:          32,
	IdleConnTimeout:       90 * time.Second,
	DisableCompression:    true,
	ResponseHeaderTimeout: 30 * time.Second,
}

// proxyToLocalhost meneruskan request ke aplikasi di 127.0.0.1:port.
// Host asli dipertahankan agar aplikasi melihat domain sebenarnya,
// dan header X-Forwarded-* diisi. Upgrade WebSocket ditangani otomatis
// oleh httputil.ReverseProxy.
func proxyToLocalhost(w http.ResponseWriter, r *http.Request, port int) {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	p := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			origHost := req.Host
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
			req.URL.RawQuery = r.URL.RawQuery
			// Pertahankan Host asli — aplikasi (mis. WordPress) butuh
			// domain sebenarnya untuk generate URL yang benar.
			req.Host = origHost
			req.Header.Set("X-Forwarded-Host", origHost)
			req.Header.Set("X-Forwarded-Proto", "https")
			// X-Forwarded-For diisi otomatis oleh ReverseProxy.
		},
		Transport: proxyTransport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("aplikasi tidak merespons (port %d): %v", port, err), http.StatusBadGateway)
		},
	}
	p.ServeHTTP(w, r)
}

// singleJoiningSlash menggabungkan path seperti httputil (hindari import internal).
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// CertStatus membaca status sertifikat domain dari cache disk.
// Mengembalikan issued=true + tanggal kedaluwarsa bila ada sertifikat
// yang masih berlaku di cache.
func (c *CertManager) CertStatus(domain string) (issued bool, expires time.Time) {
	data, err := c.cache.Get(context.Background(), NormalizeDomain(domain))
	if err != nil {
		return false, time.Time{}
	}
	// Format cache autocert: PEM private key dulu, lalu blok-blok CERTIFICATE.
	// Ambil sertifikat pertama (leaf) untuk tanggal kedaluwarsa.
	rest := data
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			continue
		}
		if time.Now().After(cert.NotAfter) {
			return false, cert.NotAfter
		}
		return true, cert.NotAfter
	}
	return false, time.Time{}
}
