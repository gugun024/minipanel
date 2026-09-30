// Package web menyajikan frontend (di-embed via go:embed)
// dan me-wire semua endpoint REST API.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"

	"minipanel/internal/apps"
	"minipanel/internal/auth"
	"minipanel/internal/backup"
	"minipanel/internal/databases"
	"minipanel/internal/files"
	"minipanel/internal/metrics"
	"minipanel/internal/services"
	"minipanel/internal/sites"
	"minipanel/internal/terminal"
)

//go:embed dist
var distFS embed.FS

// Deps adalah dependensi yang dibutuhkan handler web.
type Deps struct {
	Auth      *auth.Store
	Files     *files.Manager
	Services  *services.Manager
	Sites     *sites.Store
	Certs     *sites.CertManager
	Databases *databases.Manager
	Backups   *backup.Manager
	Apps      *apps.Manager
	DiskPath  string
	Username  string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Handler membangun seluruh router aplikasi.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	// Halaman utama: app bila sudah login, halaman login bila belum.
	// Didaftarkan tanpa batasan method agar tidak konflik dengan "/api/".
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		page := "login.html"
		if d.Auth.Valid(auth.TokenFromRequest(r)) {
			page = "index.html"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		b, err := fs.ReadFile(sub, page)
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		w.Write(b)
	})

	// Aset statis (css/js) boleh diakses publik — tidak sensitif.
	mux.Handle("GET /styles.css", fileServer)
	mux.Handle("GET /app.js", fileServer)
	mux.Handle("GET /xterm.css", fileServer)
	mux.Handle("GET /xterm.js", fileServer)
	mux.Handle("GET /xterm-addon-fit.js", fileServer)

	// Terminal web (WebSocket): hanya untuk user login — dibungkus
	// middleware yang sama dengan /api, dan handler juga mengecek
	// session sebelum upgrade.
	mux.Handle("GET /ws/terminal", d.Auth.Middleware(terminal.Handler(d.Auth)))

	// Auth (publik).
	mux.HandleFunc("POST /api/login", d.Auth.LoginHandler)
	mux.HandleFunc("POST /api/logout", d.Auth.LogoutHandler)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if !d.Auth.Valid(auth.TokenFromRequest(r)) {
			errJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": d.Username})
	})

	// Semua /api di bawah ini butuh login.
	api := http.NewServeMux()

	api.HandleFunc("GET /api/metrics", func(w http.ResponseWriter, r *http.Request) {
		snap, err := metrics.Collect(d.DiskPath)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	api.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		list, err := d.Services.Status(r.Context())
		if err != nil {
			if err == services.ErrNoSystemctl {
				errJSON(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"services": list})
	})

	api.HandleFunc("POST /api/services/action", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := d.Services.Action(r.Context(), req.Name, req.Action); err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	fm := d.Files
	api.HandleFunc("GET /api/files", func(w http.ResponseWriter, r *http.Request) {
		entries, err := fm.List(r.URL.Query().Get("path"))
		if err != nil {
			status := http.StatusInternalServerError
			if err == files.ErrEscape {
				status = http.StatusForbidden
			}
			errJSON(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": r.URL.Query().Get("path"), "entries": entries, "root": fm.Root()})
	})

	api.HandleFunc("GET /api/files/content", func(w http.ResponseWriter, r *http.Request) {
		b, err := fm.Read(r.URL.Query().Get("path"))
		if err != nil {
			status := http.StatusInternalServerError
			if err == files.ErrEscape {
				status = http.StatusForbidden
			}
			errJSON(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content": string(b)})
	})

	api.HandleFunc("PUT /api/files/content", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, files.MaxEditSize+1024)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := fm.Write(req.Path, []byte(req.Content)); err != nil {
			status := http.StatusInternalServerError
			if err == files.ErrEscape {
				status = http.StatusForbidden
			}
			errJSON(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/files/mkdir", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := fm.Mkdir(req.Path); err != nil {
			errJSON(w, fileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/files/rename", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := fm.Rename(req.Old, req.New); err != nil {
			errJSON(w, fileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/files/delete", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := fm.Delete(req.Path); err != nil {
			errJSON(w, fileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/files/upload", func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Query().Get("path")
		r.Body = http.MaxBytesReader(w, r.Body, files.MaxUploadSize+1<<20)
		if err := r.ParseMultipartForm(files.MaxUploadSize); err != nil {
			errJSON(w, http.StatusBadRequest, "upload gagal: "+err.Error())
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			errJSON(w, http.StatusBadRequest, "field 'file' tidak ditemukan")
			return
		}
		defer f.Close()
		if err := fm.SaveUpload(dir, hdr.Filename, f); err != nil {
			errJSON(w, fileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("GET /api/files/download", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		b, err := fm.Read(p)
		if err != nil {
			// File besar/biner tetap boleh diunduh; baca ulang tanpa batas editor.
			errJSON(w, fileStatus(err), err.Error())
			return
		}
		name := p
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})

	mux.Handle("/api/", d.Auth.Middleware(api))

	// --- Websites (butuh login) ---
	api.HandleFunc("GET /api/websites", func(w http.ResponseWriter, r *http.Request) {
		list := d.Sites.List()
		out := make([]map[string]any, 0, len(list))
		for _, s := range list {
			issued, exp := d.Certs.CertStatus(s.Domain)
			item := map[string]any{
				"domain":      s.Domain,
				"root":        s.Root,
				"target_type": s.TargetType,
				"proxy_port":  s.ProxyPort,
				"created_at":  s.CreatedAt,
				"cert_issued": issued,
			}
			if issued {
				item["cert_expires"] = exp
			}
			out = append(out, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"websites": out})
	})

	api.HandleFunc("POST /api/websites", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain     string `json:"domain"`
			Root       string `json:"root"`
			TargetType string `json:"target_type"` // "" | "static" | "proxy"
			ProxyPort  int    `json:"proxy_port"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		var site sites.Site
		var err error
		if req.TargetType == sites.TargetProxy {
			site, err = d.Sites.AddProxy(req.Domain, req.ProxyPort)
		} else {
			site, err = d.Sites.Add(req.Domain, req.Root)
		}
		if err != nil {
			status := http.StatusBadRequest
			if err == sites.ErrExists {
				status = http.StatusConflict
			}
			errJSON(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"domain": site.Domain, "root": site.Root,
			"target_type": site.TargetType, "proxy_port": site.ProxyPort,
			"created_at": site.CreatedAt,
		})
	})

	api.HandleFunc("DELETE /api/websites", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Sites.Delete(r.URL.Query().Get("domain")); err != nil {
			status := http.StatusBadRequest
			if err == sites.ErrNotFound {
				status = http.StatusNotFound
			}
			errJSON(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// --- Databases (butuh login) ---
	dbm := d.Databases

	// dbErr memetakan error database ke status HTTP yang tepat.
	dbErr := func(w http.ResponseWriter, err error) {
		if err == databases.ErrNotConfigured {
			errJSON(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
	}

	api.HandleFunc("GET /api/db/status", func(w http.ResponseWriter, r *http.Request) {
		configured, reachable, errMsg := dbm.Status(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": configured,
			"reachable":  reachable,
			"addr":       dbm.Addr(),
			"error":      errMsg,
		})
	})

	api.HandleFunc("GET /api/databases", func(w http.ResponseWriter, r *http.Request) {
		dbs, system, err := dbm.ListDatabases(r.Context())
		if err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"databases": dbs, "system": system})
	})

	api.HandleFunc("POST /api/databases", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := dbm.CreateDatabase(r.Context(), strings.TrimSpace(req.Name)); err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
	})

	api.HandleFunc("DELETE /api/databases", func(w http.ResponseWriter, r *http.Request) {
		if err := dbm.DropDatabase(r.Context(), r.URL.Query().Get("name")); err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("GET /api/databases/tables", func(w http.ResponseWriter, r *http.Request) {
		tables, err := dbm.ListTables(r.Context(), r.URL.Query().Get("name"))
		if err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
	})

	api.HandleFunc("GET /api/dbusers", func(w http.ResponseWriter, r *http.Request) {
		users, err := dbm.ListUsers(r.Context())
		if err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	})

	api.HandleFunc("POST /api/dbusers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			User     string `json:"user"`
			Host     string `json:"host"`
			Password string `json:"password"`
			Database string `json:"database"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if strings.TrimSpace(req.Host) == "" {
			req.Host = "localhost"
		}
		if err := dbm.CreateUser(r.Context(),
			strings.TrimSpace(req.User),
			strings.TrimSpace(req.Host),
			req.Password,
			strings.TrimSpace(req.Database)); err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
	})

	api.HandleFunc("DELETE /api/dbusers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := dbm.DropUser(r.Context(), q.Get("user"), q.Get("host")); err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/dbusers/password", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			User     string `json:"user"`
			Host     string `json:"host"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		if err := dbm.SetPassword(r.Context(),
			strings.TrimSpace(req.User),
			strings.TrimSpace(req.Host),
			req.Password); err != nil {
			dbErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// --- Backups (butuh login) ---
	bkm := d.Backups

	// bkErr memetakan error backup ke status HTTP yang tepat.
	bkErr := func(w http.ResponseWriter, err error) {
		switch err {
		case backup.ErrNotFound:
			errJSON(w, http.StatusNotFound, err.Error())
		case backup.ErrRunning:
			errJSON(w, http.StatusConflict, err.Error())
		case backup.ErrEscape:
			errJSON(w, http.StatusForbidden, err.Error())
		default:
			errJSON(w, http.StatusBadRequest, err.Error())
		}
	}

	// decodeJob membaca body JSON menjadi backup.Job.
	decodeJob := func(w http.ResponseWriter, r *http.Request) (backup.Job, bool) {
		var j backup.Job
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&j); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return backup.Job{}, false
		}
		return j, true
	}

	api.HandleFunc("GET /api/backups/jobs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": bkm.ListJobs()})
	})

	api.HandleFunc("POST /api/backups/jobs", func(w http.ResponseWriter, r *http.Request) {
		j, ok := decodeJob(w, r)
		if !ok {
			return
		}
		created, err := bkm.CreateJob(j)
		if err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})

	api.HandleFunc("PUT /api/backups/jobs", func(w http.ResponseWriter, r *http.Request) {
		j, ok := decodeJob(w, r)
		if !ok {
			return
		}
		updated, err := bkm.UpdateJob(r.URL.Query().Get("id"), j)
		if err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	})

	api.HandleFunc("DELETE /api/backups/jobs", func(w http.ResponseWriter, r *http.Request) {
		if err := bkm.DeleteJob(r.URL.Query().Get("id")); err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/backups/jobs/run", func(w http.ResponseWriter, r *http.Request) {
		if err := bkm.RunNow(r.URL.Query().Get("id")); err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	})

	api.HandleFunc("GET /api/backups/files", func(w http.ResponseWriter, r *http.Request) {
		list, err := bkm.ListFiles(r.URL.Query().Get("job"))
		if err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": list})
	})

	api.HandleFunc("GET /api/backups/files/download", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p, err := bkm.FilePath(q.Get("job"), q.Get("file"))
		if err != nil {
			bkErr(w, err)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(q.Get("file")))
		http.ServeContent(w, r, q.Get("file"), info.ModTime(), f)
	})

	api.HandleFunc("DELETE /api/backups/files", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := bkm.DeleteFile(q.Get("job"), q.Get("file")); err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	api.HandleFunc("POST /api/backups/files/restore", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Job  string `json:"job"`
			File string `json:"file"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		// Restore sinkron: untuk database, Manager memakai context sendiri
		// (timeout 30 menit) agar tidak terputus oleh siklus request.
		if err := bkm.Restore(req.Job, req.File); err != nil {
			bkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// --- Apps one-click berbasis Docker (butuh login) ---
	am := d.Apps

	appsErr := func(w http.ResponseWriter, err error) {
		if err == apps.ErrDockerUnavailable {
			errJSON(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
	}

	// Katalog aplikasi + status docker.
	api.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"docker_available": am.DockerAvailable(r.Context()),
			"apps":             am.Catalog(),
		})
	})

	// Install aplikasi (async — status dilacak lewat /api/apps/instances).
	api.HandleFunc("POST /api/apps/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AppID  string            `json:"app_id"`
			Domain string            `json:"domain"`
			Env    map[string]string `json:"env"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "request tidak valid")
			return
		}
		inst, err := am.Install(r.Context(), req.AppID, req.Domain, req.Env)
		if err != nil {
			appsErr(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"instance": inst})
	})

	// Daftar instance + status container terkini.
	api.HandleFunc("GET /api/apps/instances", func(w http.ResponseWriter, r *http.Request) {
		sts := am.Status(r.Context())
		out := make([]map[string]any, 0, len(sts))
		for _, st := range sts {
			containers := make([]map[string]any, 0, len(st.Containers))
			for _, c := range st.Containers {
				containers = append(containers, map[string]any{
					"name": c.Name, "image": c.Image, "state": c.State, "status": c.Status,
				})
			}
			out = append(out, map[string]any{
				"instance":   st.Instance,
				"containers": containers,
				"running":    st.Running,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"instances": out})
	})

	instanceAction := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.URL.Query().Get("id")
			var err error
			switch action {
			case "start":
				err = am.Start(r.Context(), id)
			case "stop":
				err = am.Stop(r.Context(), id)
			case "uninstall":
				err = am.Uninstall(r.Context(), id)
			}
			if err != nil {
				appsErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	}
	api.HandleFunc("POST /api/apps/instances/start", instanceAction("start"))
	api.HandleFunc("POST /api/apps/instances/stop", instanceAction("stop"))
	api.HandleFunc("POST /api/apps/instances/uninstall", instanceAction("uninstall"))

	return mux
}

func fileStatus(err error) int {
	if err == files.ErrEscape {
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}
