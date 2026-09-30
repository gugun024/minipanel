#!/usr/bin/env bash
# minipanel — installer satu perintah
#
#   curl -fsSL https://raw.githubusercontent.com/gugun024/minipanel/main/install.sh | sudo bash
#
# Opsi:
#   --yes         Non-interaktif (pakai env MP_* / default)
#   --dry-run     Hanya cetak langkah, tidak mengubah sistem sama sekali
#   --uninstall   Hapus service + binary (data di /var/lib/minipanel dipertahankan)
#   --purge       Dengan --uninstall: ikut hapus /var/lib/minipanel dan /etc/minipanel
#   --help        Tampilkan bantuan ini
#
# Env (mode non-interaktif):
#   MP_ADMIN_USER=admin MP_ADMIN_PASS=rahasia MP_INSTALL_DOCKER=1 MP_INSTALL_MARIADB=1
set -euo pipefail

REPO="gugun024/minipanel"
# NB: jangan pakai nama VERSION — tertimpa oleh /etc/os-release saat di-source.
PANEL_VERSION="latest"

BIN_PATH="/usr/local/bin/minipanel"
DATA_DIR="/var/lib/minipanel"
CONF_DIR="/etc/minipanel"
ENV_FILE="$CONF_DIR/minipanel.env"
UNIT_FILE="/etc/systemd/system/minipanel.service"

ASSUME_YES=0
DRY_RUN=0
UNINSTALL=0
PURGE=0

for arg in "$@"; do
    case "$arg" in
        --yes) ASSUME_YES=1 ;;
        --dry-run) DRY_RUN=1 ;;
        --uninstall) UNINSTALL=1 ;;
        --purge) PURGE=1 ;;
        --help|-h)
            sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Opsi tidak dikenal: $arg (lihat --help)" >&2; exit 1 ;;
    esac
done

# run: jalankan perintah, atau cetak saja saat --dry-run
run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "[dry-run] $*"
    else
        echo "+ $*"
        "$@"
    fi
}

info() { echo "==> $*"; }

die() { echo "ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------- uninstall
if [ "$UNINSTALL" -eq 1 ]; then
    [ "${EUID:-$(id -u)}" -eq 0 ] || die "Jalankan sebagai root (sudo)."
    info "Menghapus minipanel..."
    if [ "$DRY_RUN" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
        systemctl disable --now minipanel 2>/dev/null || true
    else
        run systemctl disable --now minipanel
    fi
    run rm -f "$UNIT_FILE"
    run rm -f "$BIN_PATH"
    if [ "$DRY_RUN" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload || true
    else
        run systemctl daemon-reload
    fi
    if [ "$PURGE" -eq 1 ]; then
        run rm -rf "$DATA_DIR" "$CONF_DIR"
        info "Data ($DATA_DIR) dan konfigurasi ($CONF_DIR) ikut dihapus (--purge)."
    else
        info "Data tetap disimpan di $DATA_DIR dan konfigurasi di $CONF_DIR."
        info "Hapus total dengan: install.sh --uninstall --purge"
    fi
    info "Selesai. minipanel sudah di-uninstall."
    exit 0
fi

# ---------------------------------------------------------------- prasyarat
if [ "$DRY_RUN" -eq 0 ]; then
    [ "${EUID:-$(id -u)}" -eq 0 ] || die "Jalankan sebagai root: sudo bash install.sh"
fi

OS_ID=""
if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-}"
    case "$OS_ID" in
        debian|ubuntu) ;;
        *) die "OS tidak didukung: '${OS_ID:-tidak diketahui}'. Installer ini hanya untuk Debian/Ubuntu." ;;
    esac
else
    die "Tidak bisa mendeteksi OS (/etc/os-release tidak ada). Hanya Debian/Ubuntu yang didukung."
fi
info "OS terdeteksi: $OS_ID"

case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "Arsitektur tidak didukung: $(uname -m) (hanya x86_64/aarch64)." ;;
esac
info "Arsitektur: $ARCH"

command -v curl >/dev/null 2>&1 || die "curl tidak ditemukan. Install dulu: apt-get install -y curl"

# ---------------------------------------------------------------- kredensial
# Jalankan ulang installer TIDAK menimpa kredensial yang sudah ada (idempotent).
EXIST_USER=""
EXIST_PASS=""
if [ -f "$ENV_FILE" ]; then
    EXIST_USER="$(grep -E '^MINIPANEL_USER=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
    EXIST_PASS="$(grep -E '^MINIPANEL_PASS=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi

ADMIN_USER="${MP_ADMIN_USER:-${EXIST_USER:-admin}}"
ADMIN_PASS="${MP_ADMIN_PASS:-$EXIST_PASS}"
PASS_GENERATED=0
INSTALL_DOCKER="${MP_INSTALL_DOCKER:-0}"
INSTALL_MARIADB="${MP_INSTALL_MARIADB:-0}"

ask_yn() {
    # $1 = pertanyaan, $2 = default (y/n). Jawaban disimpan di REPLY_YN (0/1).
    local ans=""
    read -r -p "$1 [$2] " ans || true
    ans="${ans:-$2}"
    case "$ans" in
        y|Y|ya|Ya|YA) REPLY_YN=1 ;;
        *) REPLY_YN=0 ;;
    esac
}

if [ "$ASSUME_YES" -eq 0 ] && [ "$DRY_RUN" -eq 0 ] && [ -t 0 ]; then
    read -r -p "Username admin panel [$ADMIN_USER]: " _u || true
    [ -n "${_u:-}" ] && ADMIN_USER="$_u"
    read -r -s -p "Password admin panel (kosongkan = buat acak): " _p || true
    echo
    [ -n "${_p:-}" ] && ADMIN_PASS="$_p"
    ask_yn "Install Docker juga?" "n"; INSTALL_DOCKER="$REPLY_YN"
    ask_yn "Install MariaDB juga?" "n"; INSTALL_MARIADB="$REPLY_YN"
fi

if [ -z "$ADMIN_PASS" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then
        ADMIN_PASS="(akan digenerate acak)"
    else
        ADMIN_PASS="$(head -c 48 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)"
    fi
    PASS_GENERATED=1
fi

# ---------------------------------------------------------------- download binary
if [ "$PANEL_VERSION" = "latest" ]; then
    DL_BASE="https://github.com/$REPO/releases/latest/download"
else
    DL_BASE="https://github.com/$REPO/releases/download/$PANEL_VERSION"
fi

download_binary() {
    local name="$1" tmp="$2"
    curl -fsSL -o "$tmp" "$DL_BASE/$name"
}

if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] curl -fsSL -o /tmp/minipanel.bin $DL_BASE/minipanel-linux-$ARCH (fallback: $DL_BASE/minipanel)"
    echo "[dry-run] install -m 0755 /tmp/minipanel.bin $BIN_PATH"
else
    TMP_BIN="$(mktemp)"
    trap 'rm -f "$TMP_BIN"' EXIT
    info "Mengunduh minipanel ($ARCH) dari GitHub Releases..."
    if ! download_binary "minipanel-linux-$ARCH" "$TMP_BIN"; then
        info "Asset minipanel-linux-$ARCH tidak ditemukan, mencoba nama 'minipanel'..."
        download_binary "minipanel" "$TMP_BIN" || die "Gagal mengunduh binary dari $DL_BASE"
    fi
    run install -m 0755 "$TMP_BIN" "$BIN_PATH"
fi

# ---------------------------------------------------------------- direktori + env
run install -d -m 0755 "$DATA_DIR"
run install -d -m 0755 "$CONF_DIR"
if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] tulis $ENV_FILE (chmod 600): MINIPANEL_USER=$ADMIN_USER, MINIPANEL_PASS=***, MINIPANEL_DATA=$DATA_DIR"
else
    umask 077
    cat > "$ENV_FILE" <<EOF
MINIPANEL_USER=$ADMIN_USER
MINIPANEL_PASS=$ADMIN_PASS
MINIPANEL_DATA=$DATA_DIR
EOF
    chmod 600 "$ENV_FILE"
    info "Konfigurasi ditulis ke $ENV_FILE (chmod 600)"
fi

# ---------------------------------------------------------------- opsional: Docker
if [ "$INSTALL_DOCKER" = "1" ]; then
    if [ "$DRY_RUN" -eq 0 ] && command -v docker >/dev/null 2>&1; then
        info "Docker sudah ter-install, dilewati."
    else
        info "Menginstall Docker (script resmi get.docker.com)..."
        if [ "$DRY_RUN" -eq 1 ]; then
            echo "[dry-run] curl -fsSL https://get.docker.com | sh"
            echo "[dry-run] systemctl enable --now docker"
        else
            curl -fsSL https://get.docker.com | sh
            systemctl enable --now docker || true
        fi
    fi
fi

# ---------------------------------------------------------------- opsional: MariaDB
if [ "$INSTALL_MARIADB" = "1" ]; then
    if [ "$DRY_RUN" -eq 0 ] && command -v mariadbd >/dev/null 2>&1; then
        info "MariaDB sudah ter-install, dilewati."
    else
        info "Menginstall MariaDB..."
        run apt-get update
        run apt-get install -y mariadb-server
        if [ "$DRY_RUN" -eq 1 ]; then
            echo "[dry-run] systemctl enable --now mariadb"
        else
            systemctl enable --now mariadb || true
        fi
    fi
fi

# ---------------------------------------------------------------- systemd unit
if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] tulis $UNIT_FILE (EnvironmentFile=$ENV_FILE, ExecStart=$BIN_PATH, Restart=on-failure, WorkingDirectory=$DATA_DIR)"
    echo "[dry-run] systemctl daemon-reload"
    echo "[dry-run] systemctl enable --now minipanel"
else
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=minipanel - panel kontrol server minimalis
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH
WorkingDirectory=$DATA_DIR
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
    info "Unit systemd ditulis ke $UNIT_FILE"
    run systemctl daemon-reload
    run systemctl enable --now minipanel
fi

# ---------------------------------------------------------------- ringkasan
IP_ADDR="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$IP_ADDR" ] || IP_ADDR="127.0.0.1"
SVC_STATUS="tidak diperiksa (dry-run)"
if [ "$DRY_RUN" -eq 0 ]; then
    SVC_STATUS="$(systemctl is-active minipanel 2>/dev/null || echo 'tidak aktif')"
fi

if [ "$DRY_RUN" -eq 1 ]; then
    echo
    echo "=== DRY-RUN selesai — tidak ada perubahan apa pun pada sistem ==="
fi
cat <<EOF

================================================================
  minipanel berhasil di-install
================================================================
  URL panel   : http://$IP_ADDR:8080
  Username    : $ADMIN_USER
EOF
if [ "$PASS_GENERATED" -eq 1 ]; then
    echo "  Password    : $ADMIN_PASS   <-- SIMPAN, hanya tampil sekali ini"
else
    echo "  Password    : (sesuai yang diatur / sudah ada sebelumnya)"
fi
cat <<EOF
  Status      : $SVC_STATUS
  Binary      : $BIN_PATH
  Data        : $DATA_DIR
  Konfigurasi : $ENV_FILE

  Catatan: service berjalan sebagai root (perlu untuk kelola
  service, bind port 80/443, dan akses Docker). Jaga kredensial
  panel seperti kredensial admin server.
================================================================
EOF
