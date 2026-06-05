package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed ui/templates/*.html
var templateFS embed.FS

var templates *template.Template

const AppVersion = "1.0.0"

// Template helper functions
var funcMap = template.FuncMap{
	"formatTime": func(t time.Time) string {
		return t.Format("2006-01-02 15:04:05")
	},
	"formatTimePtr": func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"statusClass": func(status string) string {
		switch status {
		case "online":
			return "status-online"
		case "offline":
			return "status-offline"
		default:
			return "status-unknown"
		}
	},
	"upper": strings.ToUpper,
	"version": func() string { return AppVersion },
}

func loadTemplates() {
	var err error
	templates, err = template.New("").Funcs(funcMap).ParseFS(
		templateFS, "ui/templates/*.html",
	)
	if err != nil {
		log.Fatalf("template parse: %v", err)
	}
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s error: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func main() {
	dbPath  := flag.String("db",     "/opt/lbpanel/lbpanel.db", "SQLite database path")
	addr    := flag.String("addr",   "0.0.0.0:4040",            "Listen address (HTTPS)")
	certDir := flag.String("certs",  "/opt/lbpanel/certs",      "Directory for self-signed cert")
	domain  := flag.String("domain", "",                         "Panel domain (e.g. lbpanel.20z.eu) — only for Caddy config hint")
	flag.Parse()

	// Env overrides
	if v := os.Getenv("LBPANEL_DB");     v != "" { *dbPath  = v }
	if v := os.Getenv("LBPANEL_ADDR");   v != "" { *addr    = v }
	if v := os.Getenv("LBPANEL_DOMAIN"); v != "" { *domain  = v }
	if v := os.Getenv("LBPANEL_CERTS");  v != "" { *certDir = v }

	// Init
	initDB(*dbPath)
	ensureAdminExists()
	loadTemplates()

	// Store domain in DB so handlers can use it
	if *domain != "" {
		dbSetSetting("panel_domain", *domain)
	}

	// Build router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Public
	r.Get("/login",  handleLogin)
	r.Post("/login", handleLogin)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "OK")
	})

	// Agent API (key auth)
	r.Group(func(r chi.Router) {
		r.Use(agentAuthMiddleware)
		r.Get("/api/agent/ping",    handleAgentPing)
		r.Post("/api/agent/report", handleAgentReport)
	})

	// Protected (JWT auth)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard",  handleDashboard)
		r.Post("/logout",    handleLogout)
		r.Get("/password",   handleChangePassword)
		r.Post("/password",  handleChangePassword)

		r.Get("/nodes",                 handleNodes)
		r.Get("/nodes/add",             handleNodeAdd)
		r.Post("/nodes/add",            handleNodeAdd)
		r.Post("/nodes/{id}/delete",    handleNodeDelete)
		r.Get("/nodes/{id}/regenkey",   handleNodeRegenKey)
		r.Post("/nodes/{id}/regenkey",  handleNodeRegenKey)
		r.Get("/nodes/{id}/check",      handleNodeCheck)
		r.Post("/nodes/checkall",       handleCheckAllNodes)

		r.Get("/sites",              handleSites)
		r.Get("/sites/add",          handleSiteAdd)
		r.Post("/sites/add",         handleSiteAdd)
		r.Post("/sites/{id}/delete", handleSiteDelete)

		r.Get("/caddy",         handleCaddy)
		r.Post("/caddy/reload", handleCaddyReload)

		r.Get("/logs",           handleLogs)
		r.Get("/install-script", handleInstallScript)

		// Caddy config snippet for domain mode
		r.Get("/setup", handleSetup)
	})

	// TLS — self-signed cert (works for IP and domain alike)
	tlsCfg, err := buildSelfSignedTLS(*certDir, *domain)
	if err != nil {
		log.Fatalf("TLS setup: %v", err)
	}

	srv := &http.Server{
		Addr:         *addr,
		Handler:      r,
		TLSConfig:    tlsCfg,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("lbpanel v%s  →  https://%s", AppVersion, *addr)
	if *domain != "" {
		log.Printf("domain mode: https://%s:4040  (skonfiguruj Caddy — patrz /setup)", *domain)
	}
	log.Printf("login: lbadmin / lbadmin  ← zmień hasło!")

	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatal(err)
	}
}

// buildSelfSignedTLS generates (or loads cached) self-signed ECDSA cert.
// Cert covers: 127.0.0.1, ::1, wszystkie lokalne IP + opcjonalnie domenę.
func buildSelfSignedTLS(certDir, domain string) (*tls.Config, error) {
	os.MkdirAll(certDir, 0700)
	certFile := certDir + "/cert.pem"
	keyFile  := certDir + "/key.pem"

	// If cert already exists and is still valid (>7 days), reuse it
	if cert, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil && time.Until(leaf.NotAfter) > 7*24*time.Hour {
			log.Printf("TLS: loaded existing self-signed cert (expires %s)",
				leaf.NotAfter.Format("2006-01-02"))
			return tlsConfig(cert), nil
		}
	}

	log.Println("TLS: generating new self-signed ECDSA cert...")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"lbpanel"},
			CommonName:   "lbpanel",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// SANs: loopback + all local IPs
	tmpl.IPAddresses = []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipnet.IP)
			}
		}
	}

	// Optional domain SAN
	if domain != "" {
		tmpl.DNSNames = []string{domain, "www." + domain}
		log.Printf("TLS: SAN domain = %s, www.%s", domain, domain)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	// Write cert.pem
	cf, err := os.OpenFile(certFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	// Write key.pem
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	kf, err := os.OpenFile(keyFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})
	kf.Close()

	log.Printf("TLS: cert saved to %s (valid 1 year)", certDir)

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return tlsConfig(cert), nil
}

func tlsConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		},
	}
}
