# minipanel

Panel kontrol server minimalis — **satu binary Go**, tanpa dependensi runtime,
tanpa build step frontend. Terinspirasi aaPanel, tapi hanya berisi yang penting:
dashboard metrik, kelola service, file manager, hosting website dengan
SSL otomatis, backup terjadwal, dan one-click install aplikasi via Docker.

## Fitur

| Fitur | Keterangan |
|---|---|
| 🔐 Login | Form login, session cookie `httpOnly`, password di-hash dengan bcrypt |
| 📊 Dashboard | CPU, RAM, disk, uptime, load average — dibaca dari `/proc`, auto-refresh tiap 3 detik |
| ⚙️ Layanan | Status + start/stop/restart via `systemctl` (whitelist nama service) |
| 📁 File manager | Browse, lihat/edit file teks, upload, buat file/folder, rename, hapus, download — di-jail ke satu direktori root |
| 🌐 Websites | Tambah domain → otomatis dilayani via **HTTPS** dengan sertifikat Let's Encrypt (tanpa nginx; TLS di-terminate langsung oleh minipanel). Dua tipe target: **statis** (file dari document root) atau **proxy** (teruskan ke aplikasi di `127.0.0.1:port`) |
| 🗄️ Databases | Kelola database & user MySQL/MariaDB: buat/hapus database, buat/hapus user + grant, ganti password (butuh kredensial admin via env) |
| 💻 Terminal | Terminal web interaktif (xterm.js + PTY via WebSocket) — shell penuh setara user yang menjalankan panel |
| 💾 Backups | Backup terjadwal (cron) website (tar.gz) & database (SQL): jadwal bisa diaktifkan/dinonaktifkan per job, retensi otomatis, download & restore dari panel |
| 📦 Apps | One-click install aplikasi berbasis **Docker** (WordPress, Ghost, contoh Node.js): container jalan di port bebas otomatis, domain langsung ter-proxy ke aplikasi |

## Install Otomatis (satu perintah)

Di VPS Debian/Ubuntu yang masih kosong, cukup satu perintah — binary diunduh
dari GitHub Releases, dipasang sebagai service systemd (auto-start saat boot),
dan Docker/MariaDB bisa ikut di-install bila dipilih:

```bash
curl -fsSL https://raw.githubusercontent.com/gugun024/minipanel/main/install.sh | sudo bash
```

Installer akan bertanya username & password admin panel (password kosong =
dibuatkan acak dan ditampilkan sekali di ringkasan akhir), lalu menampilkan
URL panel. Kredensial disimpan di `/etc/minipanel/minipanel.env` (chmod 600),
data panel di `/var/lib/minipanel`. Installer bersifat *idempotent* —
dijalankan ulang tidak menimpa kredensial yang sudah ada.

Mode non-interaktif (untuk provisioning otomatis):

```bash
curl -fsSL https://raw.githubusercontent.com/gugun024/minipanel/main/install.sh | \
  sudo env MP_ADMIN_USER=admin MP_ADMIN_PASS=rahasia123 \
           MP_INSTALL_DOCKER=1 MP_INSTALL_MARIADB=1 bash -s -- --yes
```

| Opsi | Keterangan |
|---|---|
| `--yes` | Non-interaktif, pakai env `MP_*` / default |
| `--dry-run` | Hanya cetak langkah yang akan dilakukan, tanpa mengubah sistem |
| `--uninstall` | Stop service, hapus unit systemd + binary (data dipertahankan) |
| `--uninstall --purge` | Uninstall + hapus `/var/lib/minipanel` dan `/etc/minipanel` |

Env installer: `MP_ADMIN_USER` (default `admin`), `MP_ADMIN_PASS`,
`MP_INSTALL_DOCKER=1`, `MP_INSTALL_MARIADB=1`.

> ⚠️ **Service berjalan sebagai root.** Itu perlu agar panel bisa kelola
> service (systemctl), bind port 80/443, dan mengakses daemon Docker.
> Perlakukan kredensial panel setara kredensial admin server, dan lihat
> catatan keamanan di bawah soal halaman admin yang belum ber-HTTPS.

## Uninstall

Copot minipanel dari server:

```bash
curl -fsSL https://raw.githubusercontent.com/gugun024/minipanel/main/install.sh | \
  sudo bash -s -- --uninstall
```

Kalau file `install.sh` masih ada di server, cukup
`sudo bash install.sh --uninstall`. Yang dilakukan perintah di atas:

1. Stop service `minipanel` dan nonaktifkan dari auto-start
   (`systemctl disable --now minipanel`).
2. Hapus unit systemd `/etc/systemd/system/minipanel.service` dan binary
   `/usr/local/bin/minipanel`, lalu `systemctl daemon-reload`.

Data panel (`/var/lib/minipanel`) dan konfigurasi + kredensial
(`/etc/minipanel`) **dipertahankan** — install ulang nanti memakai data
yang sama. Untuk menghapus semuanya sekalian (bersih total):

```bash
curl -fsSL https://raw.githubusercontent.com/gugun024/minipanel/main/install.sh | \
  sudo bash -s -- --uninstall --purge
```

Yang **tidak pernah** disentuh uninstaller, dengan atau tanpa `--purge`:

- File website di `/var/www` (document root website-website kamu).
- Docker dan MariaDB yang kemarin dipasang lewat opsi installer — itu
  paket sistem yang berdiri sendiri; copot dengan `apt` bila sudah tidak
  dipakai.
- Database dan user MySQL/MariaDB yang dibuat lewat panel.

## Cara build

Butuh Go 1.24+ (cukup sekali, di mesin build saja):

```bash
cd minipanel
go build -o minipanel ./cmd/minipanel
```

Hasilnya satu file `minipanel` (~13,5 MB) — copy ke server mana pun (linux/amd64),
tidak perlu install apa-apa lagi.

## Cara run

```bash
MINIPANEL_USER=admin MINIPANEL_PASS=rahasia123 ./minipanel
```

Buka `http://<ip-server>:8080` di browser, login dengan kredensial di atas.

Jika `MINIPANEL_PASS` **tidak** di-set, minipanel membuat password acak yang
aman dan mencetaknya sekali di terminal saat startup — simpan password itu.

### Variabel environment

| Variabel | Default | Keterangan |
|---|---|---|
| `MINIPANEL_USER` | `admin` | Username login |
| `MINIPANEL_PASS` | *(acak, dicetak saat startup)* | Password login |
| `MINIPANEL_ADDR` | `:8080` | Alamat listen, mis. `127.0.0.1:8080` |
| `MINIPANEL_ROOT` | `/var/www` → fallback direktori data (`MINIPANEL_DATA`); yang belum ada dibuat otomatis | Direktori root file manager |
| `MINIPANEL_SERVICES` | `nginx,apache2,mysql,mariadb,php-fpm,redis-server,redis,docker,ssh,sshd,postgresql` | Daftar service (pisahkan koma) |
| `MINIPANEL_DATA` | `~/.minipanel` | Direktori data panel (daftar website + cache sertifikat) |
| `HTTP_PORT` | `80` | Port HTTP: serve ACME challenge + redirect ke HTTPS |
| `HTTPS_PORT` | `443` | Port HTTPS: serve website per-domain (TLS otomatis) |
| `MINIPANEL_ACME_STAGING` | `1` | `1` = Let's Encrypt staging (default, aman untuk dev); `0` = produksi |
| `MINIPANEL_DB_HOST` | `127.0.0.1` | Host server MySQL/MariaDB |
| `MINIPANEL_DB_PORT` | `3306` | Port server MySQL/MariaDB |
| `MINIPANEL_DB_USER` | *(kosong)* | User admin database (butuh hak CREATE DATABASE & CREATE USER) |
| `MINIPANEL_DB_PASS` | *(kosong)* | Password user admin database |

Contoh lengkap:

```bash
MINIPANEL_USER=admin \
MINIPANEL_PASS=rahasia123 \
MINIPANEL_ADDR=:8080 \
MINIPANEL_ROOT=/var/www \
MINIPANEL_SERVICES=nginx,mysql,redis-server,docker \
./minipanel
```

## Websites + SSL otomatis

Tambah domain lewat tab **Websites** di panel (atau API di bawah). minipanel
langsung melayani domain itu via HTTPS — **tanpa nginx / reverse proxy
eksternal**: Go me-terminate TLS sendiri dan memilih sertifikat berdasarkan
SNI, lalu me-routing request berdasarkan Host header ke target website itu:

- **Tipe statis** (`static`, default): serve file dari document root.
- **Tipe proxy** (`proxy`): semua request diteruskan (**reverse proxy**) ke
  aplikasi yang listen di `127.0.0.1:<port>` — untuk aplikasi Node.js,
  container Docker, dsb. Host asli + header `X-Forwarded-*` diteruskan,
  dan **WebSocket upgrade didukung**. Tipe ini dipakai otomatis oleh tab
  **Apps**; bisa juga dibuat manual untuk aplikasi yang sudah jalan sendiri.

Alurnya:

1. Tambah domain, mis. `tokosaya.com`. Document root boleh dikosongkan —
   otomatis dibuat di `<data-dir>/sites/tokosaya.com`.
2. Saat pertama kali ada yang akses `https://tokosaya.com`, minipanel
   mendaftarkan sertifikat ke Let's Encrypt (verifikasi HTTP-01 lewat port 80)
   dan menyimpannya di cache (`<data-dir>/certs`).
3. Sertifikat diperpanjang otomatis sebelum kedaluwarsa. Status sertifikat
   (sudah terbit / tanggal expired) terlihat di tabel Websites.

### Syarat

- DNS domain (A/AAAA record) **sudah mengarah ke IP server** ini.
- Port **80 dan 443 terbuka** dan bisa diakses publik (untuk verifikasi ACME).
- minipanel harus bisa bind ke port 80/443 — jalankan sebagai **root**,
  atau set kapabilitas: `setcap 'cap_net_bind_service=+ep' ./minipanel`.

### Staging vs produksi

Secara default minipanel memakai **Let's Encrypt staging**
(`MINIPANEL_ACME_STAGING=1`): sertifikat diterbitkan beneran tapi
**tidak dipercaya browser** (tampil peringatan). Ini disengaja agar aman
dari rate limit Let's Encrypt saat development/testing.

Untuk produksi (sertifikat asli yang dipercaya browser):

```bash
MINIPANEL_ACME_STAGING=0 ./minipanel
```

### Testing tanpa root

Port bisa diganti agar bisa dicoba tanpa hak akses root:

```bash
MINIPANEL_DATA=/tmp/mp-data HTTP_PORT=8081 HTTPS_PORT=8443 \
MINIPANEL_ADDR=127.0.0.1:8080 ./minipanel
```

Lalu (ganti `test` dengan password Anda):

```bash
# login
curl -s -c cj.txt -X POST 127.0.0.1:8080/api/login \
  -d '{"username":"admin","password":"test"}'

# tambah website
curl -s -b cj.txt -X POST 127.0.0.1:8080/api/websites \
  -d '{"domain":"contoh.com","root":""}'

# redirect HTTP -> HTTPS (perhatikan port 8443 dipertahankan)
curl -s -o /dev/null -w "%{http_code} -> %{redirect_url}\n" \
  -H "Host: contoh.com" http://127.0.0.1:8081/halo

# serve file via HTTPS (butuh SNI yang benar)
echo "<h1>halo</h1>" > /tmp/mp-data/sites/contoh.com/index.html
curl -sk --noproxy '*' --resolve contoh.com:8443:127.0.0.1 \
  https://contoh.com:8443/
```

> **Catatan:** penerbitan sertifikat asli tidak bisa dites tanpa domain
> publik yang mengarah ke server — Let's Encrypt harus bisa mencapai
> server Anda lewat internet. Untuk verifikasi end-to-end, lakukan di
> server dengan domain sungguhan (mulai dari mode staging).

### API websites (butuh login)

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/api/websites` | Daftar website + tipe target + status sertifikat |
| `POST` | `/api/websites` | Tambah statis: body `{"domain":"...","root":"..."}` (root opsional). Tambah proxy: body `{"domain":"...","target_type":"proxy","proxy_port":3000}` |
| `DELETE` | `/api/websites?domain=...` | Hapus dari daftar (file di disk **tidak** dihapus) |

Website yang dibuat versi lama (tanpa `target_type`) otomatis dianggap
`static` saat dimuat.

## Databases (MySQL/MariaDB)

Tab **Databases** mengelola database dan user. Fitur ini **opsional**:
kalau `MINIPANEL_DB_USER`/`MINIPANEL_DB_PASS` tidak di-set, tab menampilkan
panduan konfigurasi dan panel tetap berjalan normal.

```bash
MINIPANEL_DB_HOST=127.0.0.1 \
MINIPANEL_DB_PORT=3306 \
MINIPANEL_DB_USER=root \
MINIPANEL_DB_PASS=rahasia \
./minipanel
```

User admin butuh hak `CREATE`/`DROP` database dan `CREATE USER`
(biasanya user `root`). Yang bisa dilakukan dari UI/API:

- **Database**: lihat daftar (database sistem disembunyikan terpisah),
  buat database baru (otomatis `utf8mb4`), hapus database, lihat daftar tabel.
- **User**: lihat daftar user, buat user baru + otomatis diberi
  `ALL PRIVILEGES` ke **satu** database pilihan, hapus user, ganti password.

Nama database/user hanya boleh huruf, angka, dan underscore (maks 64 karakter)
— ini validasi anti SQL-injection, karena identifier tidak bisa di-parameterize
di SQL. Di balik layar nama tetap di-quote dengan backtick sebagai pertahanan
berlapis; password selalu dikirim sebagai parameter, tidak pernah
diinterpolasi ke query.

### API databases (butuh login)

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/api/db/status` | Status: `configured`, `reachable`, `addr`, `error` |
| `GET` | `/api/databases` | Daftar database user + database sistem (terpisah) |
| `POST` | `/api/databases` | Buat: body `{"name":"..."}` |
| `DELETE` | `/api/databases?name=...` | Hapus database |
| `GET` | `/api/databases/tables?name=...` | Daftar tabel dalam database |
| `GET` | `/api/dbusers` | Daftar user (`user`, `host`) |
| `POST` | `/api/dbusers` | Buat user + grant: body `{"user":"...","host":"localhost","password":"...","database":"..."}` |
| `DELETE` | `/api/dbusers?user=...&host=...` | Hapus user |
| `POST` | `/api/dbusers/password` | Ganti password: body `{"user":"...","host":"...","password":"..."}` |

Bila database belum dikonfigurasi, endpoint mengembalikan `503` dengan pesan
yang jelas; bila server DB tidak terjangkau, error koneksi ditampilkan apa adanya.

## Terminal web

Tab **Terminal** membuka shell interaktif langsung di browser
(xterm.js di frontend, PTY sungguhan di backend — bukan emulasi perintah).

- Klik tab **Terminal** → koneksi WebSocket (`GET /ws/terminal`) dibuka dan
  shell dijalankan: dari env `SHELL`, fallback `/bin/bash` lalu `/bin/sh`.
  Direktori awal adalah home user; `TERM=xterm-256color`.
- Ukuran terminal (cols/rows) mengikuti ukuran jendela browser secara
  otomatis (fit addon + pesan resize ke PTY).
- Tombol **Sambung ulang** menutup sesi lama dan membuka shell baru.
- Satu koneksi = satu proses shell. Menutup tab/koneksi membunuh shell-nya
  (proses selalu di-reap, tidak meninggalkan zombie). Membuka beberapa tab
  browser berarti beberapa shell terpisah.

> ⚠️ **Keamanan — baca ini.** Terminal = akses shell **penuh** dengan hak
> akses user yang menjalankan minipanel. Bila panel jalan sebagai **root**,
> siapa pun yang login ke panel mendapat shell **root**. Untuk pemakaian
> serius, jalankan minipanel sebagai user biasa (non-root) agar dampak
> terminal (dan panel secara umum) terbatas pada user itu.

Endpoint WebSocket hanya menerima koneksi dengan session login yang valid
(tanpa itu: `401`) dan header `Origin` (bila ada) harus satu host dengan panel.

## Backup terjadwal

Tab **Backups** mengelola job backup dengan scheduler cron yang berjalan
di dalam proses minipanel sendiri (tidak perlu crontab sistem).

Satu job berisi:

- **Nama** — bebas, hanya untuk tampilan.
- **Target** — `website` (arsip document root satu domain), `database`
  (dump SQL satu database), atau `both` (keduanya dalam satu job).
- **Jadwal** — cron expression 5 field (`menit jam tanggal bulan hari-minggu`),
  mis. `0 2 * * *` = tiap hari jam 02:00. Form menyediakan preset
  harian/mingguan/bulanan/tiap jam + mode custom. Descriptor seperti
  `@daily` juga diterima.
- **Aktif/nonaktif** — toggle di daftar job. Menonaktifkan job
  **menghentikan jadwalnya saat itu juga** (tanpa restart panel, tanpa
  menghapus job); mengaktifkan lagi langsung menjadwalkannya kembali.
  Job nonaktif tetap bisa dijalankan manual dengan tombol **▶ Jalan**.
- **Retensi** — jumlah backup terakhir **per tipe file** yang disimpan
  (default 7). Setiap backup sukses menghapus file yang lebih lama.

### Hasil backup

Definisi job disimpan di `<data-dir>/backups.json`; file hasil backup di
`<data-dir>/backups/<job-id>/` dengan nama berbasis waktu:

- Website: `<yyyymmdd-jjmmss>-site.tar.gz` — arsip document root domain.
  Symlink disimpan sebagai symlink (tidak diikuti).
- Database: `<yyyymmdd-jjmmss>-db.sql` — dump SQL.

Dari UI/API file bisa di-**download**, di-**hapus**, dan di-**restore**:

> ⚠️ **Restore menimpa data saat ini.** Restore website mengekstrak arsip
> menimpa isi document root (file yang tidak ada di arsip tidak dihapus);
> restore database mengeksekusi file `.sql` ke server (dump memuat
> `DROP TABLE IF EXISTS`, jadi tabel dikembalikan persis seperti saat
> backup). UI meminta konfirmasi ganda sebelum restore.

### Dump database: lewat koneksi Go, bukan mysqldump

Dump dibuat **lewat koneksi Go** (driver yang sama dengan fitur Databases),
bukan memanggil perintah `mysqldump` — minipanel tetap satu binary tanpa
dependensi eksternal. Isinya file `.sql` standar (header
`SET FOREIGN_KEY_CHECKS=0`, `CREATE DATABASE IF NOT EXISTS` + `USE`,
`DROP TABLE IF EXISTS` + `CREATE TABLE`, `INSERT` batch, lalu view) yang
bisa di-restore dengan cara biasa:

```bash
mysql -u root -p < 20251001-020000-db.sql
```

Nilai biner (BLOB/BINARY) ditulis sebagai literal hex, string di-escape
mengikuti aturan MySQL, dan klausa `DEFINER` pada view dibuang agar bisa
dibuat ulang oleh user admin mana pun. Konsekuensi pilihan ini:
**trigger, stored procedure/function, dan event tidak ikut ter-dump** —
tabel, view, dan data tercakup penuh.

Menghapus job **tidak** menghapus file backup-nya di disk (sama seperti
Websites); hapus folder `<data-dir>/backups/<job-id>/` secara manual bila
sudah tidak diperlukan.

### API backups (butuh login)

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/api/backups/jobs` | Daftar job (termasuk `enabled`, `last_run`, `last_status`) |
| `POST` | `/api/backups/jobs` | Buat job: body `{"name","enabled","schedule","target_type","domain","database","retention"}` |
| `PUT` | `/api/backups/jobs?id=...` | Ubah job (termasuk toggle `enabled`) — scheduler langsung menyesuaikan |
| `DELETE` | `/api/backups/jobs?id=...` | Hapus job (file backup di disk tidak ikut terhapus) |
| `POST` | `/api/backups/jobs/run?id=...` | Jalankan sekarang (async; pantau lewat `last_status`) |
| `GET` | `/api/backups/files?job=...` | Daftar file backup job (nama, tipe, ukuran, waktu) |
| `GET` | `/api/backups/files/download?job=...&file=...` | Download file backup |
| `DELETE` | `/api/backups/files?job=...&file=...` | Hapus file backup |
| `POST` | `/api/backups/files/restore` | Restore: body `{"job":"...","file":"..."}` — **menimpa data saat ini** |

Status jalan terakhir: `last_status` berisi `ok`, `running`, atau
`error: <pesan>`. Satu job tidak pernah berjalan bersamaan dengan dirinya
sendiri; run manual saat job sedang berjalan ditolak (`409`).

## Apps one-click (Docker)

Tab **Apps** memasang aplikasi populer dalam sekali klik.

**Konsepnya:** minipanel *tidak perlu tahu isi aplikasi*. Setiap project —
Next.js, WordPress, Node.js, apa pun — dibungkus jadi **container Docker**
yang listen di satu port. minipanel tinggal menjalankan container itu dan
mem-proxy domain ke port-nya (website tipe `proxy`). Docker adalah *opsi
deployment*, bukan syarat bagi project-nya: project yang sama juga bisa
dijalankan manual lalu dipasangkan website tipe proxy manual.

### Syarat

- **Docker ter-install** dan daemon-nya jalan di server
  (`curl -fsSL https://get.docker.com | sh`). minipanel memanggil docker
  lewat CLI (`docker run/pull/ps/...`) — user yang menjalankan minipanel
  harus punya akses ke socket docker (biasanya root, atau grup `docker`).
- Bila docker tidak ada, tab Apps menampilkan panduan install dan fitur
  nonaktif — **panel tetap jalan normal**.

### Cara install aplikasi

1. Buka tab **Apps**, klik **Install** pada kartu aplikasi.
2. Isi **domain** (harus sudah mengarah ke server ini, sama seperti Websites)
   dan env yang diminta (yang berlabel * wajib; password ber-ikon 🔑
   dibuatkan otomatis bila dikosongkan).
3. Panel bekerja di background: **pull image → cari port bebas**
   (rentang `10000-20000`) **→ jalankan container → daftarkan website proxy**
   untuk domain itu. Pantau statusnya di daftar *Aplikasi ter-install*
   (`installing` → `jalan`). HTTPS otomatis berlaku seperti website lain.
4. Dari daftar instance: **▶ jalankan**, **⏹ berhenti**, **🗑 uninstall**
   (container + network + volume + website proxy dihapus semua).

Semua container yang dikelola panel berlabel `minipanel.managed=1` dan
`minipanel.instance=<id>`; container docker lain milik Anda tidak disentuh.

### Aplikasi bawaan

| Aplikasi | Image | Keterangan |
|---|---|---|
| **WordPress** | `wordpress:latest` + `mysql:8.4` | **Dua container**: WordPress + MySQL, dihubungkan lewat *docker network privat* per instance (alias `db`). Password database dibuatkan otomatis dan disamakan di kedua container. Data persisten di named volume. |
| **Ghost** | `ghost:latest` | Platform blogging (Node.js). Pakai SQLite bawaan image — tanpa database terpisah. Isi env `url` dengan URL publik persis (`https://blog.contoh.com`). |
| **Node.js (contoh)** | `node:22-alpine` | Server HTTP kecil dari `node -e` — contoh paling sederhana cara membungkus aplikasi Node sendiri. Ganti definisinya untuk aplikasi sungguhan (mis. image hasil `docker build` project Next.js Anda). |

### Menambah definisi aplikasi sendiri

Definisi aplikasi adalah **data JSON**, bukan kode. Taruh file
`<data-dir>/apps/<nama>.json` — file user menimpa definisi bawaan bila
`id`-nya sama. Contoh minimal:

```json
{
  "id": "uptime-kuma",
  "name": "Uptime Kuma",
  "description": "Monitoring uptime mandiri.",
  "image": "louislam/uptime-kuma:1",
  "container_port": 3001,
  "volumes": [
    { "name": "data", "container_path": "/app/data" }
  ]
}
```

Field lengkap: `id`, `name`, `description`, `image`, `command` (array,
opsional), `container_port`, `env` (array `{key, label, default, required,
secret, generate}`), `volumes` (array `{name, container_path}` — nama asli
volume: `mp-<instance>-<name>`), dan `companions` (container pendamping:
`{id, image, env, volumes, network_alias}`; env companion mendukung
`from_app_env` untuk menyalin nilai dari env aplikasi utama, mis. password
database yang sama — lihat definisi WordPress di kode). Definisi yang tidak
valid membuat panel menolak start dengan pesan jelas — perbaiki file-nya.

### API apps (butuh login)

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/api/apps` | Katalog aplikasi + `docker_available` |
| `POST` | `/api/apps/install` | Install (async, `202`): body `{"app_id":"...","domain":"...","env":{"KEY":"nilai"}}` |
| `GET` | `/api/apps/instances` | Daftar instance + status container terkini |
| `POST` | `/api/apps/instances/start?id=...` | Jalankan container instance |
| `POST` | `/api/apps/instances/stop?id=...` | Hentikan container instance |
| `POST` | `/api/apps/instances/uninstall?id=...` | Hapus total: container, network, volume, website proxy |

## Catatan akses root

- **Kelola service butuh hak akses root/sudo.** Jalankan minipanel sebagai root,
  atau beri kemampuan via polkit/sudoers agar `systemctl start/stop/restart`
  bisa jalan. Tanpa itu, tombol aksi akan menampilkan pesan error yang jelas.
- Jika `systemctl` tidak ada di sistem, tab Layanan menampilkan pesan
  "systemctl tidak ditemukan" alih-alih error misterius.
- File manager hanya bisa mengakses file yang bisa dibaca/ditulis oleh user
  yang menjalankan minipanel — jalankan sebagai root bila perlu kelola
  `/var/www` milik `www-data`.

## Keamanan

- Password tidak pernah disimpan plaintext (bcrypt) dan tidak pernah di-log.
- Session: token acak 256-bit, cookie `httpOnly` + `SameSite=Lax`, kedaluwarsa 12 jam.
- File manager di-jail: request berisi `..` langsung ditolak (403), dan path
  selalu dinormalisasi + dipastikan tetap di dalam root.
- Nama service dibatasi whitelist — tidak bisa disuntik perintah shell
  (argumen diteruskan langsung ke `exec`, tanpa shell).
- Websites: nama domain divalidasi ketat (regex hostname); document root
  harus path absolut, tidak boleh `/`, dan traversal (`..`) ditolak.
  Sertifikat (autocert) hanya diterbitkan untuk domain yang terdaftar user
  (HostPolicy) — handshake TLS untuk host tak dikenal langsung gagal.
- Databases: nama database/user/host divalidasi ketat sebelum dipakai di query
  (identifier tidak bisa di-parameterize); identifier tetap di-quote backtick,
  string di-escape, dan password selalu via parameter. Kredensial admin DB
  hanya dari environment, tidak pernah disimpan di disk oleh panel.
- Terminal: endpoint WebSocket butuh session login valid dan memeriksa
  `Origin` secara ketat. Ingat: terminal adalah shell penuh setara user
  proses panel — lihat peringatan di bagian Terminal web.
- Backups: download file backup di-jail ketat ke direktori job-nya
  (nama file harus base name berakhiran backup yang dikenal; traversal
  ditolak 403). Ekstraksi arsip menolak entri `..`/absolut (anti zip-slip)
  dan melewati symlink absolut. Restore hanya untuk user login dan selalu
  diawali konfirmasi di UI karena menimpa data.
- Apps/Docker: semua perintah docker dipanggil lewat `exec` **tanpa shell**
  (argumen sebagai argv), dan semua input (nama image, env, volume, port)
  divalidasi regex ketat sebelum dipakai. Port container hanya di-publish
  ke `127.0.0.1` — akses publik selalu lewat reverse proxy + HTTPS panel.
  ⚠️ **Akses ke daemon docker setara root.** User yang bisa login panel
  pada dasarnya bisa menjalankan container apa pun — perlakukan kredensial
  panel setara kredensial admin server.
- ⚠️ **Panel admin (port 8080) belum pakai HTTPS.** Untuk pemakaian serius,
  pasang di belakang reverse proxy (Nginx/Caddy) dengan TLS, atau batasi
  listen ke `127.0.0.1` + SSH tunnel. (Website yang di-hosting sudah HTTPS
  otomatis — ini hanya soal halaman admin panel itu sendiri.)

## Batasan

- Metrik hanya untuk **Linux** (baca `/proc`).
- Session tersimpan di memori — hilang saat proses di-restart.
- Tidak ada manajemen user ganda, role, atau API token.
- File manager mengikuti symlink (tidak di-resolve) — jangan letakkan symlink
  ke luar root di dalam direktori yang dikelola.
- Batas ukuran: edit file via browser maks 2 MB, upload maks 50 MB.
- Serve website statis memakai `http.FileServer` standar (termasuk directory
  listing bila tidak ada `index.html`); belum ada PHP/CGI langsung — untuk
  aplikasi dinamis pakai tipe **proxy** atau tab **Apps** (Docker).
  Koneksi WebSocket/SSE yang di-proxy tunduk pada timeout server HTTPS
  panel (tulis 60 detik) — untuk aplikasi realtime yang sangat long-lived
  mungkin perlu penyesuaian.
- Backups: backup target website hanya untuk website **tipe statis**
  (punya document root). Website tipe proxy / aplikasi Docker tidak
  di-backup oleh fitur ini (data aplikasi Docker silakan backup volume-nya
  dengan cara lain).
- Cache sertifikat autocert menyimpan private key di `<data-dir>/certs`
  (mode 0700) — jaga direktori data panel.
- Fitur database butuh server MySQL/MariaDB yang berjalan dan kredensial
  admin via env; tanpa itu tab Databases nonaktif (panel tetap jalan).
  Grant user selalu `ALL PRIVILEGES` ke satu database — belum ada pilihan
  hak akses granular.
- Terminal: satu sesi shell per koneksi WebSocket; tidak ada riwayat sesi,
  attach ulang, atau multiplexing (tmux/screen) — sesi yang terputus hilang.
  Shell yang tersedia mengikuti env `SHELL` host.
- Backup database (dumper Go): tidak mencakup trigger, stored
  procedure/function, dan event. Restore file `.sql` dari tool lain yang
  memakai `DELIMITER` (mis. dump mysqldump berisi routine) tidak didukung.
  Restore website menimpa file se-path di document root; file lain yang
  tidak ada di arsip tidak dihapus. Satu run backup dibatasi 30 menit.
- Apps: install berjalan async dan bergantung pada kecepatan pull image
  (bila pull gagal tapi image sudah ada di lokal, install lanjut memakai
  image lokal itu); tidak ada log streaming install (status hanya
  installing/berjalan/error). Companion saat ini maksimal pola "app +
  pendamping" di satu network privat; env companion hanya dari
  `from_app_env`/default/generate — belum bisa diisi bebas dari form.
  Tidak ada batas resource (CPU/RAM) per container dan tidak ada update
  image otomatis. Container diberi `--restart unless-stopped` agar hidup
  lagi setelah server reboot.
- Belum ada: notifikasi.

## Struktur kode

```
minipanel/
├── cmd/minipanel/main.go      # entrypoint: config env, wiring, HTTP server
├── internal/
│   ├── auth/                  # login, bcrypt, session cookie
│   ├── metrics/               # baca /proc: CPU, RAM, disk, uptime, load
│   ├── services/              # wrapper systemctl (whitelist)
│   ├── files/                # operasi file ter-jail (anti path traversal)
│   ├── sites/                # CRUD website (JSON) + CertManager autocert
│   │                         # + routing statis / reverse proxy per-domain
│   ├── databases/            # kelola database & user MySQL/MariaDB (pure-Go driver)
│   ├── terminal/             # terminal web: WebSocket + PTY (shell interaktif)
│   ├── backup/               # backup terjadwal: job (JSON) + scheduler cron,
│   │                         # arsip tar.gz website, dump/restore SQL database
│   ├── apps/                 # one-click install Docker: wrapper CLI docker,
│   │                         # katalog aplikasi (JSON), lifecycle instance
│   └── web/                   # router API + frontend ter-embed (go:embed)
│       └── dist/              # index.html, login.html, styles.css, app.js,
│                              # xterm.js, xterm.css, xterm-addon-fit.js (vendored)
├── go.mod                     # dependensi: x/crypto, go-sql-driver/mysql,
│                              # gorilla/websocket, creack/pty, robfig/cron
└── README.md
```

**Keputusan teknis:** tidak pakai SQLite — kredensial cukup dari environment,
session di memori, dan daftar website, job backup, serta instance aplikasi
disimpan di file JSON (`<data-dir>/websites.json`, `<data-dir>/backups.json`,
dan `<data-dir>/instances.json`, ditulis atomik). Docker diakses lewat
**CLI** (`exec`, tanpa shell) — bukan Docker SDK — agar binary tetap kecil
dan bebas dependensi tambahan; definisi katalog aplikasi juga data JSON
(bawaan di kode + override di `<data-dir>/apps/*.json`). Dependensi eksternal:
`golang.org/x/crypto` (bcrypt + autocert/ACME),
`github.com/go-sql-driver/mysql` (driver database pure-Go, tanpa cgo),
`github.com/gorilla/websocket` (WebSocket — dipilih karena pola
satu-penulisnya sederhana dan cocok untuk pump PTY),
`github.com/creack/pty` (alokasi pseudo-terminal), dan
`github.com/robfig/cron/v3` (scheduler backup in-process); sisanya stdlib murni.
Frontend terminal memakai **xterm.js** (`@xterm/xterm` 5.5.0 + addon-fit)
yang file build resminya di-vendor ke `dist/` dan di-embed — tetap tanpa
npm/build step.
