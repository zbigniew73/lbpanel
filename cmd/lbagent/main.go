package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const AgentVersion = "1.0.0"

var (
	agentKey   string
	panelURL   string
	nodeName   string
	listenPort string
)

func main() {
	agentKey = os.Getenv("LBAGENT_KEY")
	panelURL = os.Getenv("LBAGENT_PANEL")
	nodeName = os.Getenv("LBAGENT_NODE")
	listenPort = os.Getenv("LBAGENT_PORT")

	if agentKey == "" {
		log.Fatal("LBAGENT_KEY is required")
	}
	if listenPort == "" {
		listenPort = "7313"
	}
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}

	mux := http.NewServeMux()

	// All routes require key auth
	mux.HandleFunc("/status", authMiddleware(handleStatus))
	mux.HandleFunc("/sync", authMiddleware(handleSync))
	mux.HandleFunc("/info", authMiddleware(handleInfo))
	mux.HandleFunc("/health", handleHealth) // no auth — used by Caddy health check

	addr := ":" + listenPort
	log.Printf("lbagent v%s node=%s listening on %s", AgentVersion, nodeName, addr)

	// Start background reporter
	if panelURL != "" {
		go reportLoop()
	}

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// authMiddleware checks X-LBPanel-Key header
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-LBPanel-Key")
		if key == "" || key != agentKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleHealth — no auth, used by Caddy health checks
func handleHealth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "OK")
}

// handleStatus returns node status info
func handleStatus(w http.ResponseWriter, r *http.Request) {
	info := gatherInfo()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleInfo returns detailed node info
func handleInfo(w http.ResponseWriter, r *http.Request) {
	info := gatherInfo()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleSync triggers rsync from WordPress source
func handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Source string `json:"source"` // user@host:/path/
		Dest   string `json:"dest"`   // /var/www/cdn/domain/
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.Source == "" || req.Dest == "" {
		http.Error(w, "source and dest required", http.StatusBadRequest)
		return
	}

	// Run rsync in background
	go runSync(req.Source, req.Dest)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "sync started",
		"source": req.Source,
		"dest":   req.Dest,
	})
}

// runSync executes rsync
func runSync(source, dest string) {
	log.Printf("sync: %s -> %s", source, dest)

	// Create dest dir if needed
	os.MkdirAll(dest, 0755)

	cmd := exec.Command("rsync",
		"-az",
		"--no-perms",
		"--include=*.jpg", "--include=*.jpeg",
		"--include=*.png", "--include=*.gif",
		"--include=*.webp", "--include=*.svg",
		"--include=*.ico", "--include=*.woff",
		"--include=*.woff2", "--include=*/",
		"--exclude=*",
		source, dest,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("sync error: %v — %s", err, string(out))
		return
	}
	log.Printf("sync ok: %s", strings.TrimSpace(string(out)))
}

type NodeInfo struct {
	Node    string  `json:"node"`
	Version string  `json:"version"`
	OS      string  `json:"os"`
	Arch    string  `json:"arch"`
	Uptime  float64 `json:"uptime_seconds"`
	Load    string  `json:"load"`
	Time    int64   `json:"time"`
}

func gatherInfo() NodeInfo {
	info := NodeInfo{
		Node:    nodeName,
		Version: AgentVersion,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Time:    time.Now().Unix(),
	}

	// Get load average (Linux only)
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			info.Load = strings.Join(parts[:3], " ")
		}
	}

	// Get uptime (Linux only)
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		var up float64
		fmt.Sscanf(string(data), "%f", &up)
		info.Uptime = up
	}

	return info
}

// reportLoop sends periodic heartbeat to lbpanel
func reportLoop() {
	ticker := time.NewTicker(30 * time.Second)
	client := &http.Client{Timeout: 5 * time.Second}

	for range ticker.C {
		info := gatherInfo()
		body, _ := json.Marshal(map[string]interface{}{
			"status": "online",
			"info":   fmt.Sprintf("load:%s uptime:%.0fs", info.Load, info.Uptime),
		})

		req, err := http.NewRequest("POST", panelURL+"/api/agent/report",
			strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		req.Header.Set("X-LBPanel-Key", agentKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("report error: %v", err)
			continue
		}
		resp.Body.Close()
	}
}
