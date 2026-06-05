package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// --- Auth handlers ---

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderTemplate(w, "login.html", map[string]interface{}{
			"Error": "",
		})
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	adminUser := dbGetSetting("admin_username")
	adminHash := dbGetSetting("admin_password_hash")

	if username != adminUser || !checkPassword(adminHash, password) {
		dbAddLog(nil, "login_failed", "user: "+username)
		renderTemplate(w, "login.html", map[string]interface{}{
			"Error": "Nieprawidłowy login lub hasło",
		})
		return
	}

	if err := setSessionCookie(w, username); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	dbAddLog(nil, "login_ok", "user: "+username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderTemplate(w, "password.html", map[string]interface{}{
			"Success": false,
			"Error":   "",
		})
		return
	}

	current := r.FormValue("current")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm")

	adminHash := dbGetSetting("admin_password_hash")
	if !checkPassword(adminHash, current) {
		renderTemplate(w, "password.html", map[string]interface{}{
			"Error": "Aktualne hasło jest nieprawidłowe",
		})
		return
	}
	if newPass != confirm {
		renderTemplate(w, "password.html", map[string]interface{}{
			"Error": "Nowe hasła nie są zgodne",
		})
		return
	}
	if len(newPass) < 8 {
		renderTemplate(w, "password.html", map[string]interface{}{
			"Error": "Hasło musi mieć co najmniej 8 znaków",
		})
		return
	}

	hash, err := hashPassword(newPass)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	dbSetSetting("admin_password_hash", hash)
	dbAddLog(nil, "password_changed", "ok")
	renderTemplate(w, "password.html", map[string]interface{}{
		"Success": true,
		"Error":   "",
	})
}

// --- Dashboard ---

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	nodes, _ := dbGetNodes()
	sites, _ := dbGetSites()
	logs, _ := dbGetLogs(10)

	online := 0
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}

	renderTemplate(w, "dashboard.html", map[string]interface{}{
		"Nodes":        nodes,
		"Sites":        sites,
		"Logs":         logs,
		"OnlineCount":  online,
		"TotalNodes":   len(nodes),
		"TotalSites":   len(sites),
		"CaddyOnline":  caddyStatus(),
	})
}

// --- Nodes ---

func handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := dbGetNodes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "nodes.html", map[string]interface{}{
		"Nodes": nodes,
	})
}

func handleNodeAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderTemplate(w, "node_add.html", map[string]interface{}{
			"Error":      "",
			"NewKey":     "",
			"NodeName":   "",
		})
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	ip := strings.TrimSpace(r.FormValue("ip"))
	portStr := strings.TrimSpace(r.FormValue("port"))

	if name == "" || ip == "" {
		renderTemplate(w, "node_add.html", map[string]interface{}{
			"Error": "Nazwa i IP są wymagane",
		})
		return
	}

	port := 7313
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	apiKey, err := generateAPIKey()
	if err != nil {
		http.Error(w, "key gen error", http.StatusInternalServerError)
		return
	}

	id, err := dbAddNode(name, ip, port, apiKey)
	if err != nil {
		renderTemplate(w, "node_add.html", map[string]interface{}{
			"Error": "Błąd zapisu: " + err.Error(),
		})
		return
	}

	dbAddLog(nil, "node_added", fmt.Sprintf("id=%d name=%s ip=%s", id, name, ip))

	// Show key ONCE after creation
	renderTemplate(w, "node_add.html", map[string]interface{}{
		"Error":    "",
		"NewKey":   apiKey,
		"NodeName": name,
		"NodeID":   id,
	})
}

func handleNodeRegenKey(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	node, err := dbGetNode(id)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	newKey, err := generateAPIKey()
	if err != nil {
		http.Error(w, "key gen error", http.StatusInternalServerError)
		return
	}

	if err := dbUpdateNodeKey(id, newKey); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dbAddLog(nil, "node_key_regen", fmt.Sprintf("node=%s", node.Name))

	renderTemplate(w, "node_key.html", map[string]interface{}{
		"Node":   node,
		"NewKey": newKey,
	})
}

func handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	node, _ := dbGetNode(id)
	if err := dbDeleteNode(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if node != nil {
		dbAddLog(nil, "node_deleted", fmt.Sprintf("name=%s ip=%s", node.Name, node.IP))
	}
	http.Redirect(w, r, "/nodes", http.StatusSeeOther)
}

func handleNodeCheck(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	node, err := dbGetNode(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	status := checkNodeStatus(node)
	dbUpdateNodeStatus(id, status)
	dbAddLog(&node.ID, "status_check", status)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": status,
		"node":   node.Name,
	})
}

// checkNodeStatus pings lbagent on node
func checkNodeStatus(node *Node) string {
	url := fmt.Sprintf("http://%s:%d/status", node.IP, node.Port)
	client := &http.Client{Timeout: 3 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "offline"
	}
	req.Header.Set("X-LBPanel-Key", node.APIKey)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "offline"
	}
	resp.Body.Close()
	return "online"
}

// handleCheckAllNodes checks all nodes sequentially
func handleCheckAllNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := dbGetNodes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	results := make(map[string]string)
	for _, n := range nodes {
		node := n
		status := checkNodeStatus(&node)
		dbUpdateNodeStatus(node.ID, status)
		dbAddLog(&node.ID, "status_check", status)
		results[node.Name] = status
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// --- Sites ---

func handleSites(w http.ResponseWriter, r *http.Request) {
	sites, err := dbGetSites()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "sites.html", map[string]interface{}{
		"Sites": sites,
	})
}

func handleSiteAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderTemplate(w, "site_add.html", map[string]interface{}{
			"Error":    "",
			"LBPolicies": []string{"ip_hash", "round_robin", "least_conn", "random"},
		})
		return
	}

	domain := strings.TrimSpace(r.FormValue("domain"))
	cdnSub := strings.TrimSpace(r.FormValue("cdn_sub"))
	wpSource := strings.TrimSpace(r.FormValue("wp_source"))
	lbPolicy := r.FormValue("lb_policy")

	if domain == "" {
		renderTemplate(w, "site_add.html", map[string]interface{}{
			"Error": "Domena jest wymagana",
			"LBPolicies": []string{"ip_hash", "round_robin", "least_conn", "random"},
		})
		return
	}

	if lbPolicy == "" {
		lbPolicy = "ip_hash"
	}

	id, err := dbAddSite(domain, cdnSub, wpSource, lbPolicy)
	if err != nil {
		renderTemplate(w, "site_add.html", map[string]interface{}{
			"Error": "Błąd zapisu: " + err.Error(),
			"LBPolicies": []string{"ip_hash", "round_robin", "least_conn", "random"},
		})
		return
	}

	dbAddLog(nil, "site_added", fmt.Sprintf("id=%d domain=%s", id, domain))
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

func handleSiteDelete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	site, _ := dbGetSite(id)
	if err := dbDeleteSite(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if site != nil {
		dbAddLog(nil, "site_deleted", "domain="+site.Domain)
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

// --- Caddy ---

func handleCaddy(w http.ResponseWriter, r *http.Request) {
	config, err := caddyGetConfig()
	if err != nil {
		config = "Caddy API niedostępne: " + err.Error()
	}

	sites, _ := dbGetSites()
	nodes, _ := dbGetNodes()
	preview := caddyGetCaddyfile(sites, nodes)

	renderTemplate(w, "caddy.html", map[string]interface{}{
		"Config":  config,
		"Preview": preview,
		"Online":  caddyStatus(),
	})
}

func handleCaddyReload(w http.ResponseWriter, r *http.Request) {
	err := caddyReloadFromDB()
	result := "ok"
	if err != nil {
		result = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"result": result})
}

// --- Logs ---

func handleLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := dbGetLogs(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "logs.html", map[string]interface{}{
		"Logs": logs,
	})
}

// --- Agent API (called by lbagent) ---
// These routes use agentAuthMiddleware

func handleAgentPing(w http.ResponseWriter, r *http.Request) {
	node := r.Context().Value(contextKey("node")).(*Node)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"node": node.Name,
		"time": time.Now().Unix(),
	})
}

func handleAgentReport(w http.ResponseWriter, r *http.Request) {
	node := r.Context().Value(contextKey("node")).(*Node)

	var payload struct {
		Status string `json:"status"`
		Info   string `json:"info"`
	}
	json.NewDecoder(r.Body).Decode(&payload)

	dbUpdateNodeStatus(node.ID, "online")
	if payload.Info != "" {
		dbAddLog(&node.ID, "agent_report", payload.Info)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
}

// handleSetup shows Caddy config snippet for domain mode (Let's Encrypt)
func handleSetup(w http.ResponseWriter, r *http.Request) {
	domain := dbGetSetting("panel_domain")
	renderTemplate(w, "setup.html", map[string]interface{}{
		"Domain":    domain,
		"HasDomain": domain != "",
	})
}

// handleInstallScript serves a dynamic install script for lbagent
func handleInstallScript(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		http.Error(w, "missing node param", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(nodeID)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}

	node, err := dbGetNode(id)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	// Get panel host from request
	panelHost := r.Host

	script := fmt.Sprintf(`#!/bin/bash
# lbagent install script for node: %s
# Generated by lbpanel at %s
set -e

AGENT_URL="http://%s/agent/binary"
INSTALL_DIR="/opt/lbagent"
SERVICE_FILE="/etc/systemd/system/lbagent.service"
API_KEY="%s"
PANEL_URL="http://%s"
NODE_NAME="%s"

echo "[lbagent] Installing for node: $NODE_NAME"

mkdir -p $INSTALL_DIR

# Download binary
# curl -fsSL "$AGENT_URL" -o "$INSTALL_DIR/lbagent"
# chmod +x "$INSTALL_DIR/lbagent"

# Write config
cat > "$INSTALL_DIR/lbagent.env" << EOF
LBAGENT_KEY=%s
LBAGENT_PANEL=%s
LBAGENT_NODE=%s
LBAGENT_PORT=7313
EOF

# Create systemd service
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=LBPanel Agent
After=network.target

[Service]
EnvironmentFile=$INSTALL_DIR/lbagent.env
ExecStart=$INSTALL_DIR/lbagent
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable lbagent
systemctl restart lbagent

echo "[lbagent] Done! Agent running on port 7313"
`, node.Name, time.Now().Format("2006-01-02 15:04:05"),
		panelHost, node.APIKey, panelHost,
		node.Name, node.APIKey, panelHost, node.Name)

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=install-lbagent-%s.sh", strings.ToLower(node.Name)))
	fmt.Fprint(w, script)
}
