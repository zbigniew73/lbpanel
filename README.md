# lbpanel

Load Balancer Panel dla AlmaLinux 9 / Rocky Linux 9.

Panel zarządzania klastrem serwerów webowych opartym na **Caddy v2** jako load balancerze. Przeznaczony do budowy prostego CDN dla stron WordPress.

## Architektura

```
                  klienci
                     │
              lb01.domena.eu  (lbpanel + Caddy)
             ┌─────┼─────┐
          web01  web02  web03  (lbagent)
```

## Stack

- **Go** + [chi](https://github.com/go-chi/chi) — HTTP router
- **SQLite** ([mattn/go-sqlite3](https://github.com/mattn/go-sqlite3), CGO_ENABLED=1) — baza danych
- **JWT** + bcrypt — autentykacja
- **Caddy Admin API** (:2019) — zarządzanie load balancerem
- HTML/template — UI (zero JS frameworków, dark theme)

## Wymagania

- AlmaLinux 9 / Rocky Linux 9
- Go 1.21+ (instalowany automatycznie przez `install.sh`)
- gcc + sqlite-devel (instalowane automatycznie)
- Caddy v2 (instalowany automatycznie)

## Instalacja

### Z GitHub (czysta maszyna)

```bash
dnf install -y git
git clone https://github.com/zbigniew73/lbpanel /root/lbpanel
cd /root/lbpanel
chmod +x install.sh
./install.sh panel --github --domain lbpanel.20z.eu
```

### Z paczki ZIP (lokalnie)

```bash
unzip lbpanel.zip
cd lbpanel
./install.sh panel --domain lbpanel.20z.eu
```

### Agenty na web01/02/03

Po zalogowaniu do panelu: **Nodes → Dodaj node** — panel wygeneruje klucz API i gotową komendę instalacyjną.

Lub ręcznie:

```bash
./install.sh agent <API_KEY> https://lbpanel.domena.eu:4040 web01
```

## Dostęp do panelu

| Tryb | URL |
|------|-----|
| Przez IP (self-signed cert) | `https://<IP>:4040` |
| Przez domenę (Let's Encrypt) | `https://lbpanel.domena.eu` |

Domyślne dane logowania: `lbadmin` / `lbadmin` — **zmień hasło po pierwszym logowaniu**.

Konfiguracja Caddy z Let's Encrypt dostępna pod: `https://<IP>:4040/setup`

## Opcje install.sh

```
./install.sh panel [OPCJE]        — instalacja panelu
./install.sh agent <KEY> <URL>    — agent na node
./install.sh caddy                — tylko Caddy
./install.sh deps                 — tylko zależności
./install.sh uninstall            — usuń panel

Opcje panel:
  --domain <domena>   domena panelu
  --local             źródła lokalne (obok install.sh)
  --github            pobierz z GitHub
```

## Struktura projektu

```
lbpanel/
├── cmd/
│   ├── lbpanel/          — główny panel (port 4040 HTTPS)
│   │   ├── main.go       — router, TLS self-signed, startup
│   │   ├── db.go         — SQLite: nodes, sites, logs
│   │   ├── auth.go       — JWT + bcrypt + API key middleware
│   │   ├── caddy.go      — Caddy Admin API client
│   │   ├── handlers.go   — HTTP handlery
│   │   └── ui/templates/ — HTML templates
│   └── lbagent/          — agent na web01/02/03 (port 7313)
│       └── main.go
├── go.mod
├── install.sh
└── README.md
```

## Licencja

MIT
