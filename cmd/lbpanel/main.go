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

// templateCache stores one parsed template set per page
var templateCache map[string]*template.Template

const AppVersion = "1.0.0"

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
	"upper":   strings.ToUpper,
	"version": func() string { return AppVersion },
	"not":     func(v bool) bool { return !v },
	"truncate": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n]
	},
}

func loadTemplates() {
	templateCache = make(map[string]*template.Template)

	pages := []string{
		"dashboard.html",
		"login.html",
		"nodes.html",
		"node_add.html",
		"node_key.html",
		"sites.html",
		"site_add.html",
		"caddy.html",
		"logs.html",
		"password.html",
		"setup.html",
	}

	// standalone pages (no base layout)
	standalone := map[string]bool{"login.html": true}

	for _, page := range pages {
		var t *template.Template
		var err error
		if standalone[page] {
			// login has its own full HTML layout
			t, err = template.New("").Funcs(funcMap).ParseFS(
				templateFS,
				"ui/templates/"+page,
			)
		} else {
			// all other pages use base.html layout
			t, err = template.New("").Funcs(funcMap).ParseFS(
				templateFS,
				"ui/templates/base.html",
				"ui/templates/"+page,
			)
		}
		if err != nil {
			log.Fatalf("template parse %s: %v", page, err)
		}
		templateCache[page] = t
		log.Printf("template loaded: %s", page)
	}
}

// standaloneTemplates don't use base layout — executed directly by their define name
var standaloneTemplates = map[string]string{
	"login.html": "login.html",
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	t, ok := templateCache[name]
	if !ok {
		log.Printf("template not found: %s", name)
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// standalone templates execute their own define block
	execName := "base"
	if tplName, isStandalone := standaloneTemplates[name]; isStandalone {
		execName = tplName
	}

	if err := t.ExecuteTemplate(w, execName, data); err != nil {
		log.Printf("template %s render error: %v", name, err)
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	dbPath  := flag.String("db",     "/opt/lbpanel/lbpanel.db", "SQLite database path")
	addr    := flag.String("addr",   "0.0.0.0:4040",            "Listen address (HTTPS)")
	certDir := flag.String("certs",  "/opt/lbpanel/certs",      "Directory for self-signed cert")
	domain  := flag.String("domain", "",                        "Panel domain")
	flag.Parse()

	if v := os.Getenv("LBPANEL_DB");     v != "" { *dbPath  = v }
	if v := os.Getenv("LBPANEL_ADDR");   v != "" { *addr    = v }
	if v := os.Getenv("LBPANEL_DOMAIN"); v != "" { *domain  = v }
	if v := os.Getenv("LBPANEL_CERTS");  v != "" { *certDir = v }

	initDB(*dbPath)
	ensureAdminExists()
	loadTemplates()

	if *domain != "" {
		dbSetSetting("panel_domain", *domain)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// ParseForm middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				r.ParseMultipartForm(32 << 20)
			}
			next.ServeHTTP(w, r)
		})
	})

	// Public
	r.Get("/login",  handleLogin)
	r.Post("/login", handleLogin)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "OK")
	})

	// Agent API
	r.Group(func(r chi.Router) {
		r.Use(agentAuthMiddleware)
		r.Get("/api/agent/ping",    handleAgentPing)
		r.Post("/api/agent/report", handleAgentReport)
	})

	// Protected
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard", handleDashboard)
		r.Post("/logout",   handleLogout)
		r.Get("/password",  handleChangePassword)
		r.Post("/password", handleChangePassword)

		r.Get("/nodes",                handleNodes)
		r.Get("/nodes/add",            handleNodeAdd)
		r.Post("/nodes/add",           handleNodeAdd)
		r.Post("/nodes/{id}/delete",   handleNodeDelete)
		r.Get("/nodes/{id}/regenkey",  handleNodeRegenKey)
		r.Post("/nodes/{id}/regenkey", handleNodeRegenKey)
		r.Get("/nodes/{id}/check",     handleNodeCheck)
		r.Post("/nodes/checkall",      handleCheckAllNodes)

		r.Get("/sites",              handleSites)
		r.Get("/sites/add",          handleSiteAdd)
		r.Post("/sites/add",         handleSiteAdd)
		r.Post("/sites/{id}/delete", handleSiteDelete)

		r.Get("/caddy",         handleCaddy)
		r.Post("/caddy/reload", handleCaddyReload)

		r.Get("/logs",           handleLogs)
		r.Get("/install-script", handleInstallScript)
		r.Get("/setup",          handleSetup)
	})

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

	log.Printf("lbpanel v%s → https://%s", AppVersion, *addr)
	log.Printf("login: lbadmin / lbadmin")

	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatal(err)
	}
}

func buildSelfSignedTLS(certDir, domain string) (*tls.Config, error) {
	os.MkdirAll(certDir, 0700)
	certFile := certDir + "/cert.pem"
	keyFile  := certDir + "/key.pem"

	if cert, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil && time.Until(leaf.NotAfter) > 7*24*time.Hour {
			log.Printf("TLS: loaded existing cert (expires %s)", leaf.NotAfter.Format("2006-01-02"))
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
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"lbpanel"}, CommonName: "lbpanel"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipnet.IP)
			}
		}
	}
	if domain != "" {
		tmpl.DNSNames = []string{domain, "www." + domain}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	cf, _ := os.OpenFile(certFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	privDER, _ := x509.MarshalECPrivateKey(priv)
	kf, _ := os.OpenFile(keyFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})
	kf.Close()

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
	}
}
