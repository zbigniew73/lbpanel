#!/bin/bash
# =============================================================================
# lbpanel install script — AlmaLinux 9 / Rocky Linux 9
# Użycie:
#   ./install.sh panel                         — instalacja panelu na lb01
#   ./install.sh panel --domain lbpanel.20z.eu — z domeną (generuje Caddyfile)
#   ./install.sh agent <KEY> <PANEL_URL>        — agent na web01/02/03
#   ./install.sh caddy                          — instalacja Caddy na lb01
#   ./install.sh deps                           — tylko zależności (bez instalacji)
#   ./install.sh uninstall                      — usunięcie panelu
# =============================================================================
set -euo pipefail

# --- Kolory ---
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

info()    { echo -e "${GREEN}[✓]${NC} $*"; }
step()    { echo -e "${CYAN}[→]${NC} ${BOLD}$*${NC}"; }
warning() { echo -e "${YELLOW}[!]${NC} $*"; }
error()   { echo -e "${RED}[✗]${NC} $*"; exit 1; }
header()  { echo -e "\n${BOLD}${BLUE}=== $* ===${NC}\n"; }

# --- Stałe ---
INSTALL_DIR="/opt/lbpanel"
SERVICE_FILE="/etc/systemd/system/lbpanel.service"
DB_PATH="$INSTALL_DIR/lbpanel.db"
CERT_DIR="$INSTALL_DIR/certs"
BINARY="$INSTALL_DIR/lbpanel"
AGENT_BINARY="$INSTALL_DIR/lbagent"
PORT=4040
REPO="https://github.com/zbigniew73/lbpanel"

# Go — minimalna wymagana wersja
GO_MIN_MAJOR=1
GO_MIN_MINOR=21
GO_INSTALL_VER="1.22.4"
GO_ARCH="amd64"

# --- Sprawdzenie roota ---
[[ $EUID -ne 0 ]] && error "Uruchom jako root: sudo ./install.sh $*"

# --- Sprawdzenie OS ---
check_os() {
    if [ ! -f /etc/os-release ]; then
        error "Nie można wykryć systemu operacyjnego"
    fi
    source /etc/os-release
    case "$ID" in
        almalinux|rocky|rhel|centos)
            if [[ "${VERSION_ID%%.*}" -lt 9 ]]; then
                error "Wymagany AlmaLinux/Rocky 9+. Wykryto: $PRETTY_NAME"
            fi
            ;;
        *)
            warning "Nieprzetestowany OS: $PRETTY_NAME — kontynuuję mimo to"
            ;;
    esac
    info "OS: $PRETTY_NAME"
}

# =============================================================================
# DEPS — instalacja wszystkich zależności systemowych
# =============================================================================
install_deps() {
    header "Instalacja zależności systemowych"

    step "Aktualizacja repozytoriów..."
    dnf makecache --quiet

    step "Instalacja EPEL..."
    dnf install -y epel-release 2>/dev/null || true
    dnf makecache --quiet 2>/dev/null || true

    step "Instalacja pakietów bazowych..."
    dnf install -y \
        gcc \
        gcc-c++ \
        make \
        git \
        curl \
        wget \
        rsync \
        sqlite \
        sqlite-devel \
        openssl \
        openssl-devel \
        tar \
        gzip \
        which \
        procps-ng \
        net-tools \
        bind-utils \
        firewalld \
        2>/dev/null

    info "Pakiety systemowe zainstalowane"

    # Go — sprawdź czy jest i czy wystarczająco nowy
    install_go

    info "Wszystkie zależności gotowe"
}

# =============================================================================
# GO — instalacja / weryfikacja wersji
# =============================================================================
install_go() {
    step "Sprawdzanie Go..."

    # Funkcja porównująca wersje
    go_version_ok() {
        local ver
        ver=$(go version 2>/dev/null | grep -oP '\d+\.\d+' | head -1)
        [ -z "$ver" ] && return 1
        local major minor
        major=$(echo "$ver" | cut -d. -f1)
        minor=$(echo "$ver" | cut -d. -f2)
        [[ "$major" -gt "$GO_MIN_MAJOR" ]] && return 0
        [[ "$major" -eq "$GO_MIN_MAJOR" && "$minor" -ge "$GO_MIN_MINOR" ]] && return 0
        return 1
    }

    if go_version_ok; then
        info "Go $(go version | grep -oP '\d+\.\d+\.\d+' | head -1) — OK"
        return
    fi

    # Sprawdź czy dnf ma wystarczająco nowe Go
    DNF_GO_VER=$(dnf info golang 2>/dev/null | grep -i version | head -1 | awk '{print $3}' || echo "0")
    DNF_GO_MINOR=$(echo "$DNF_GO_VER" | cut -d. -f2)

    if [[ "${DNF_GO_MINOR:-0}" -ge "$GO_MIN_MINOR" ]]; then
        step "Instalacja Go $DNF_GO_VER przez dnf..."
        dnf install -y golang
        info "Go $(go version) zainstalowane"
        return
    fi

    # Pobierz oficjalny tarball
    step "Instalacja Go $GO_INSTALL_VER z golang.org..."

    local ARCHIVE="go${GO_INSTALL_VER}.linux-${GO_ARCH}.tar.gz"
    local URL="https://go.dev/dl/${ARCHIVE}"
    local TMP="/tmp/${ARCHIVE}"

    curl -fsSL "$URL" -o "$TMP" || \
        wget -q "$URL" -O "$TMP" || \
        error "Nie można pobrać Go z $URL"

    # Usuń poprzednią instalację jeśli istnieje
    rm -rf /usr/local/go

    tar -C /usr/local -xzf "$TMP"
    rm -f "$TMP"

    # PATH
    if ! grep -q '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null; then
        cat > /etc/profile.d/go.sh << 'EOF'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/root/go
export GOBIN=/usr/local/go/bin
EOF
    fi
    export PATH=$PATH:/usr/local/go/bin

    info "Go $(go version) zainstalowane w /usr/local/go"
}

# =============================================================================
# CADDY — instalacja przez oficjalne repo
# =============================================================================
install_caddy() {
    header "Instalacja Caddy"

    if command -v caddy &>/dev/null; then
        info "Caddy już zainstalowany: $(caddy version)"
        return
    fi

    step "Dodawanie repozytorium Caddy..."
    dnf install -y 'dnf-command(copr)' 2>/dev/null || true

    # Oficjalny sposób instalacji Caddy na RHEL/AlmaLinux
    cat > /etc/yum.repos.d/caddy.repo << 'EOF'
[caddy]
name=Caddy
baseurl=https://copr-be.cloud.fedoraproject.org/results/@caddy/caddy/epel-9-$basearch/
gpgcheck=1
gpgkey=https://copr-be.cloud.fedoraproject.org/results/@caddy/caddy/pubkey.gpg
enabled=1
EOF

    step "Instalacja Caddy..."
    dnf install -y caddy || {
        # Fallback — pobierz binary bezpośrednio
        warning "Repo Caddy niedostępne — pobieram binary..."
        CADDY_VER=$(curl -s https://api.github.com/repos/caddyserver/caddy/releases/latest \
            | grep '"tag_name"' | cut -d'"' -f4 | tr -d 'v')
        CADDY_URL="https://github.com/caddyserver/caddy/releases/download/v${CADDY_VER}/caddy_${CADDY_VER}_linux_amd64.tar.gz"
        curl -fsSL "$CADDY_URL" | tar -xz -C /tmp caddy
        mv /tmp/caddy /usr/bin/caddy
        chmod +x /usr/bin/caddy
    }

    # Katalogi Caddy
    mkdir -p /etc/caddy /var/log/caddy /var/lib/caddy
    chown caddy:caddy /var/log/caddy /var/lib/caddy 2>/dev/null || true

    # Podstawowy Caddyfile jeśli nie istnieje
    if [ ! -f /etc/caddy/Caddyfile ]; then
        cat > /etc/caddy/Caddyfile << 'EOF'
# /etc/caddy/Caddyfile
# Wygenerowany przez lbpanel install.sh
# Dodaj konfigurację domen poniżej

# Caddy Admin API — tylko lokalnie
{
    admin 127.0.0.1:2019
    email admin@example.com
}
EOF
    fi

    # Systemd
    if ! systemctl is-enabled --quiet caddy 2>/dev/null; then
        systemctl enable caddy
    fi
    systemctl start caddy || true

    # Firewall
    if command -v firewall-cmd &>/dev/null; then
        systemctl enable --now firewalld
        firewall-cmd --permanent --add-service=http  &>/dev/null || true
        firewall-cmd --permanent --add-service=https &>/dev/null || true
        firewall-cmd --reload &>/dev/null || true
        info "Firewall: porty 80/443 otwarte"
    fi

    info "Caddy $(caddy version) zainstalowany"
    warning "Caddy Admin API nasłuchuje na 127.0.0.1:2019 — wymagane przez lbpanel"
}

# =============================================================================
# PANEL — kompilacja i instalacja lbpanel
# =============================================================================
install_panel() {
    header "Instalacja lbpanel"

    # Parsuj argumenty
    DOMAIN=""
    SOURCE="auto"   # auto | local | github
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --domain)        DOMAIN="$2";  shift 2 ;;
            --source)        SOURCE="$2";  shift 2 ;;
            --local)         SOURCE="local";  shift ;;
            --github)        SOURCE="github"; shift ;;
            *) shift ;;
        esac
    done

    check_os
    install_deps
    install_caddy

    step "Tworzenie katalogów..."
    mkdir -p "$INSTALL_DIR" "$CERT_DIR"
    chmod 700 "$CERT_DIR"

    # Ustal źródło kodu
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    SRC_DIR=""

    case "$SOURCE" in
        local)
            # Jawnie lokalnie — źródła muszą być obok install.sh
            if [ ! -f "$SCRIPT_DIR/go.mod" ] || [ ! -d "$SCRIPT_DIR/cmd/lbpanel" ]; then
                error "--local: nie znaleziono go.mod i cmd/ obok install.sh ($SCRIPT_DIR)"
            fi
            SRC_DIR="$SCRIPT_DIR"
            info "Tryb: lokalny — $SRC_DIR"
            ;;
        github)
            # Jawnie z GitHuba — zawsze świeży clone
            step "Tryb: GitHub — $REPO"
            rm -rf /tmp/lbpanel-src
            git clone --depth=1 "$REPO" /tmp/lbpanel-src
            SRC_DIR="/tmp/lbpanel-src"
            ;;
        auto|*)
            # Auto-wykrycie: lokalne > istniejący /tmp clone > świeży GitHub
            if [ -f "$SCRIPT_DIR/go.mod" ] && [ -d "$SCRIPT_DIR/cmd/lbpanel" ]; then
                SRC_DIR="$SCRIPT_DIR"
                info "Tryb: auto → lokalny ($SRC_DIR)"
            elif [ -d /tmp/lbpanel-src/.git ]; then
                step "Tryb: auto → istniejący clone, aktualizacja..."
                cd /tmp/lbpanel-src && git pull --quiet
                SRC_DIR="/tmp/lbpanel-src"
            else
                step "Tryb: auto → GitHub ($REPO)"
                rm -rf /tmp/lbpanel-src
                git clone --depth=1 "$REPO" /tmp/lbpanel-src
                SRC_DIR="/tmp/lbpanel-src"
            fi
            ;;
    esac

    # Kompilacja
    step "Kompilacja lbpanel (CGO_ENABLED=1)..."
    cd "$SRC_DIR"
    export PATH=$PATH:/usr/local/go/bin
    CGO_ENABLED=1 go build \
        -ldflags="-s -w" \
        -o "$BINARY" \
        ./cmd/lbpanel/

    step "Kompilacja lbagent (CGO_ENABLED=0)..."
    CGO_ENABLED=0 go build \
        -ldflags="-s -w" \
        -o "$AGENT_BINARY" \
        ./cmd/lbagent/

    chmod 755 "$BINARY" "$AGENT_BINARY"
    info "Binaries: lbpanel $(du -sh $BINARY | cut -f1), lbagent $(du -sh $AGENT_BINARY | cut -f1)"

    # Plik środowiskowy — nie nadpisuj jeśli istnieje
    if [ ! -f "$INSTALL_DIR/lbpanel.env" ]; then
        step "Tworzenie pliku konfiguracyjnego..."
        cat > "$INSTALL_DIR/lbpanel.env" << EOF
LBPANEL_DB=$DB_PATH
LBPANEL_ADDR=0.0.0.0:$PORT
LBPANEL_CERTS=$CERT_DIR
EOF
        if [ -n "$DOMAIN" ]; then
            echo "LBPANEL_DOMAIN=$DOMAIN" >> "$INSTALL_DIR/lbpanel.env"
        else
            echo "# LBPANEL_DOMAIN=lbpanel.twojadomena.pl" >> "$INSTALL_DIR/lbpanel.env"
        fi
        chmod 600 "$INSTALL_DIR/lbpanel.env"
        info "Konfiguracja: $INSTALL_DIR/lbpanel.env"
    else
        warning "Plik $INSTALL_DIR/lbpanel.env już istnieje — pomijam (backup: ${INSTALL_DIR}/lbpanel.env.bak)"
        cp "$INSTALL_DIR/lbpanel.env" "$INSTALL_DIR/lbpanel.env.bak"
    fi

    # Systemd service
    step "Tworzenie serwisu systemd..."
    cat > "$SERVICE_FILE" << EOF
[Unit]
Description=LBPanel - Load Balancer Panel
Documentation=https://github.com/zbigniew73/lbpanel
After=network.target caddy.service
Wants=caddy.service

[Service]
Type=simple
EnvironmentFile=$INSTALL_DIR/lbpanel.env
ExecStart=$BINARY
WorkingDirectory=$INSTALL_DIR
Restart=on-failure
RestartSec=5
User=root
# Uprawnienia
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=$INSTALL_DIR
# Logi
StandardOutput=journal
StandardError=journal
SyslogIdentifier=lbpanel

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable lbpanel

    # Firewall — port panelu
    if command -v firewall-cmd &>/dev/null; then
        systemctl enable --now firewalld
        firewall-cmd --permanent --add-port=${PORT}/tcp &>/dev/null || true
        firewall-cmd --reload &>/dev/null || true
        info "Firewall: port $PORT/tcp otwarty"
    fi

    # Start
    step "Uruchamianie lbpanel..."
    systemctl restart lbpanel
    sleep 2

    if systemctl is-active --quiet lbpanel; then
        SERVER_IP=$(hostname -I | awk '{print $1}')
        header "Instalacja zakończona pomyślnie!"
        echo -e "  ${GREEN}URL (IP):${NC}   https://${SERVER_IP}:${PORT}"
        if [ -n "$DOMAIN" ]; then
            echo -e "  ${GREEN}URL (domena):${NC} https://${DOMAIN}:${PORT}  ← po konfiguracji Caddy"
        fi
        echo -e "  ${GREEN}Login:${NC}      lbadmin"
        echo -e "  ${YELLOW}Hasło:${NC}      lbadmin  ${YELLOW}← ZMIEŃ PO ZALOGOWANIU!${NC}"
        echo -e "  ${CYAN}Setup Caddy:${NC} https://${SERVER_IP}:${PORT}/setup"
        echo ""
        warning "Przeglądarka pokaże ostrzeżenie o certyfikacie (self-signed) — kliknij 'Zaawansowane → Przejdź mimo to'"
        if [ -n "$DOMAIN" ]; then
            echo ""
            step "Aby aktywować Let's Encrypt dla $DOMAIN:"
            echo "  1. Otwórz: https://${SERVER_IP}:${PORT}/setup"
            echo "  2. Skopiuj wygenerowany Caddyfile"
            echo "  3. Wklej do: /etc/caddy/Caddyfile"
            echo "  4. Uruchom: systemctl reload caddy"
        fi
    else
        error "lbpanel nie wystartował!"$'\n'"Diagnostyka: journalctl -u lbpanel -n 50 --no-pager"
    fi
}

# =============================================================================
# AGENT — instalacja lbagent na web01/02/03
# =============================================================================
install_agent() {
    # Argumenty: agent <KEY> <PANEL_URL> [NODE_NAME]
    local KEY="${2:-}"
    local PANEL_URL="${3:-}"
    local NODE_NAME="${4:-$(hostname -s)}"

    [ -z "$KEY" ]       && error "Brakuje API key.\nUżycie: $0 agent <KEY> <PANEL_URL> [NODE_NAME]"
    [ -z "$PANEL_URL" ] && error "Brakuje URL panelu.\nUżycie: $0 agent <KEY> <PANEL_URL> [NODE_NAME]"

    header "Instalacja lbagent na $NODE_NAME"

    check_os

    # Minimalne zależności dla agenta
    step "Instalacja zależności agenta..."
    dnf install -y rsync openssh-clients curl 2>/dev/null || true

    local AGENT_DIR="/opt/lbagent"
    local AGENT_SERVICE="/etc/systemd/system/lbagent.service"

    mkdir -p "$AGENT_DIR"

    # Pobierz binary agenta z panelu (jeśli dostępne) lub skompiluj
    step "Pobieranie lbagent binary..."
    if curl -fsSk "${PANEL_URL}/agent/binary" -o "${AGENT_DIR}/lbagent.tmp" 2>/dev/null; then
        # Weryfikacja że to binary ELF
        if file "${AGENT_DIR}/lbagent.tmp" | grep -q ELF; then
            mv "${AGENT_DIR}/lbagent.tmp" "${AGENT_DIR}/lbagent"
            chmod 755 "${AGENT_DIR}/lbagent"
            info "lbagent pobrany z panelu"
        else
            rm -f "${AGENT_DIR}/lbagent.tmp"
            warning "Binary z panelu nieprawidłowe — kompilacja lokalna"
            build_agent_local "$AGENT_DIR"
        fi
    else
        warning "Panel niedostępny lub brak endpointu — kompilacja lokalna"
        build_agent_local "$AGENT_DIR"
    fi

    # Konfiguracja
    step "Tworzenie konfiguracji..."
    cat > "$AGENT_DIR/lbagent.env" << EOF
LBAGENT_KEY=$KEY
LBAGENT_PANEL=$PANEL_URL
LBAGENT_NODE=$NODE_NAME
LBAGENT_PORT=7313
EOF
    chmod 600 "$AGENT_DIR/lbagent.env"

    # Systemd
    cat > "$AGENT_SERVICE" << EOF
[Unit]
Description=LBPanel Agent ($NODE_NAME)
After=network.target

[Service]
Type=simple
EnvironmentFile=$AGENT_DIR/lbagent.env
ExecStart=$AGENT_DIR/lbagent
WorkingDirectory=$AGENT_DIR
Restart=on-failure
RestartSec=5
User=root
NoNewPrivileges=yes
PrivateTmp=yes
StandardOutput=journal
StandardError=journal
SyslogIdentifier=lbagent

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable lbagent

    # Firewall — port agenta (tylko LAN)
    if command -v firewall-cmd &>/dev/null; then
        systemctl enable --now firewalld
        firewall-cmd --permanent --add-port=7313/tcp &>/dev/null || true
        # Port 80 — dla Caddy health check
        firewall-cmd --permanent --add-service=http &>/dev/null || true
        firewall-cmd --reload &>/dev/null || true
        info "Firewall: port 7313/tcp i 80/tcp otwarte"
    fi

    step "Uruchamianie lbagent..."
    systemctl restart lbagent
    sleep 2

    if systemctl is-active --quiet lbagent; then
        header "lbagent zainstalowany!"
        echo -e "  ${GREEN}Node:${NC}   $NODE_NAME ($(hostname -I | awk '{print $1}'))"
        echo -e "  ${GREEN}Panel:${NC}  $PANEL_URL"
        echo -e "  ${GREEN}Port:${NC}   7313"
        echo -e "  ${GREEN}Status:${NC} $(systemctl is-active lbagent)"
    else
        error "lbagent nie wystartował!"$'\n'"Diagnostyka: journalctl -u lbagent -n 30 --no-pager"
    fi
}

# Kompilacja agenta lokalnie (gdy brak binary z panelu)
build_agent_local() {
    local DEST="$1"
    install_go
    export PATH=$PATH:/usr/local/go/bin

    local AGENT_SRC_DIR=""
    local AGENT_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    local AGENT_SOURCE="${2:-auto}"  # auto | local | github

    case "$AGENT_SOURCE" in
        local)
            [ ! -f "$AGENT_SCRIPT_DIR/go.mod" ] && \
                error "--local: brak go.mod obok install.sh ($AGENT_SCRIPT_DIR)"
            AGENT_SRC_DIR="$AGENT_SCRIPT_DIR"
            info "Tryb: lokalny — $AGENT_SRC_DIR"
            ;;
        github)
            step "Tryb: GitHub — $REPO"
            rm -rf /tmp/lbpanel-src
            git clone --depth=1 "$REPO" /tmp/lbpanel-src
            AGENT_SRC_DIR="/tmp/lbpanel-src"
            ;;
        auto|*)
            if [ -f "$AGENT_SCRIPT_DIR/go.mod" ] && [ -d "$AGENT_SCRIPT_DIR/cmd/lbagent" ]; then
                AGENT_SRC_DIR="$AGENT_SCRIPT_DIR"
                info "Tryb: auto → lokalny"
            elif [ -d /tmp/lbpanel-src/.git ]; then
                AGENT_SRC_DIR="/tmp/lbpanel-src"
            else
                step "Tryb: auto → GitHub"
                git clone --depth=1 "$REPO" /tmp/lbpanel-src
                AGENT_SRC_DIR="/tmp/lbpanel-src"
            fi
            ;;
    esac

    step "Kompilacja lbagent..."
    cd "$AGENT_SRC_DIR"
    CGO_ENABLED=0 go build -ldflags="-s -w" -o "$DEST/lbagent" ./cmd/lbagent/
    chmod 755 "$DEST/lbagent"
    info "lbagent skompilowany: $(du -sh $DEST/lbagent | cut -f1)"
}

# =============================================================================
# UNINSTALL
# =============================================================================
uninstall_panel() {
    header "Usuwanie lbpanel"

    read -rp "Czy na pewno chcesz usunąć lbpanel? [tak/nie]: " CONFIRM
    [[ "$CONFIRM" != "tak" ]] && { info "Anulowano"; exit 0; }

    step "Zatrzymywanie serwisu..."
    systemctl stop lbpanel  2>/dev/null || true
    systemctl disable lbpanel 2>/dev/null || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload

    step "Backup bazy danych..."
    if [ -f "$DB_PATH" ]; then
        cp "$DB_PATH" "/root/lbpanel-db-backup-$(date +%Y%m%d-%H%M%S).db"
        info "Backup DB: /root/lbpanel-db-backup-*.db"
    fi

    step "Usuwanie plików..."
    rm -rf "$INSTALL_DIR"

    # Firewall
    firewall-cmd --permanent --remove-port=${PORT}/tcp &>/dev/null || true
    firewall-cmd --reload &>/dev/null || true

    info "lbpanel usunięty (Caddy i lbagent pozostają)"
}

# =============================================================================
# GŁÓWNY DISPATCH
# =============================================================================
MODE="${1:-help}"

echo -e "${BOLD}${CYAN}"
echo "  ██╗██████╗ ██████╗  █████╗ ███╗   ██╗███████╗██╗     "
echo "  ██║██╔══██╗██╔══██╗██╔══██╗████╗  ██║██╔════╝██║     "
echo "  ██║██████╔╝██████╔╝███████║██╔██╗ ██║█████╗  ██║     "
echo "  ██║██╔══██╗██╔═══╝ ██╔══██║██║╚██╗██║██╔══╝  ██║     "
echo "  ██║██████╔╝██║     ██║  ██║██║ ╚████║███████╗███████╗"
echo "  ╚═╝╚═════╝ ╚═╝     ╚═╝  ╚═╝╚═╝  ╚═══╝╚══════╝╚══════╝"
echo -e "${NC}${BOLD}  Load Balancer Panel — installer v1.0${NC}"
echo ""

case "$MODE" in
    panel)    install_panel "$@" ;;
    caddy)    check_os; install_caddy ;;
    deps)     check_os; install_deps ;;
    agent)    install_agent "$@" ;;
    uninstall) uninstall_panel ;;
    help|--help|-h)
        echo "Użycie:"
        echo "  $0 panel [OPCJE]                     — instalacja panelu na lb01"
        echo "  $0 caddy                             — tylko instalacja Caddy"
        echo "  $0 deps                              — tylko zależności systemowe"
        echo "  $0 agent <KEY> <URL> [NODE]          — agent na web01/02/03"
        echo "  $0 uninstall                         — usuń panel"
        echo ""
        echo "Opcje dla \"panel\":"
        echo "  --domain <domena>    domena panelu (np. lbpanel.20z.eu)"
        echo "  --local              użyj źródeł lokalnych (obok install.sh)"
        echo "  --github             pobierz źródła z GitHub (wymaga git + internet)"
        echo "  --source auto        auto-wykrycie: lokalne > /tmp clone > GitHub (domyślne)"
        echo ""
        echo "Przykłady:"
        echo "  # Instalacja z paczki ZIP (źródła obok install.sh — domyślne)"
        echo "  ./install.sh panel --domain lbpanel.20z.eu"
        echo ""
        echo "  # Jawnie z lokalnej paczki"
        echo "  ./install.sh panel --local --domain lbpanel.20z.eu"
        echo ""
        echo "  # Zawsze świeży clone z GitHub"
        echo "  ./install.sh panel --github --domain lbpanel.20z.eu"
        echo ""
        echo "  # Agent na web01"
        echo "  ./install.sh agent <KEY> https://lbpanel.20z.eu:4040 web01"
        ;;
    *)
        error "Nieznana opcja: $MODE — użyj: $0 help"
        ;;
esac
