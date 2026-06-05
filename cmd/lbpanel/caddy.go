package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const caddyAPI = "http://127.0.0.1:2019"

var caddyClient = &http.Client{Timeout: 5 * time.Second}

// CaddyConfig is a minimal struct for load balancer config generation
type CaddyUpstream struct {
	Dial string `json:"dial"`
}

// caddyGetConfig fetches current Caddy config as raw JSON
func caddyGetConfig() (string, error) {
	resp, err := caddyClient.Get(caddyAPI + "/config/")
	if err != nil {
		return "", fmt.Errorf("caddy api unreachable: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	json.Indent(&out, body, "", "  ")
	return out.String(), nil
}

// caddyBuildConfig generates a full Caddy JSON config from sites + nodes
func caddyBuildConfig(sites []Site, nodes []Node) (map[string]interface{}, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes configured")
	}

	// Build upstreams list from active nodes
	var upstreams []map[string]interface{}
	for _, n := range nodes {
		if n.Status == "offline" {
			continue
		}
		upstreams = append(upstreams, map[string]interface{}{
			"dial": fmt.Sprintf("%s:%d", n.IP, 80),
		})
	}
	if len(upstreams) == 0 {
		// fallback — use all nodes even if status unknown
		for _, n := range nodes {
			upstreams = append(upstreams, map[string]interface{}{
				"dial": fmt.Sprintf("%s:%d", n.IP, 80),
			})
		}
	}

	// Build routes for each active site
	var routes []map[string]interface{}
	for _, s := range sites {
		if !s.Active {
			continue
		}

		hosts := []string{s.Domain}
		if s.CDNSub != "" {
			hosts = append(hosts, s.CDNSub)
		}

		lbPolicy := s.LBPolicy
		if lbPolicy == "" {
			lbPolicy = "ip_hash"
		}

		route := map[string]interface{}{
			"match": []map[string]interface{}{
				{"host": hosts},
			},
			"handle": []map[string]interface{}{
				{
					"handler": "reverse_proxy",
					"upstreams": upstreams,
					"load_balancing": map[string]interface{}{
						"selection_policy": map[string]interface{}{
							"policy": lbPolicy,
						},
					},
					"health_checks": map[string]interface{}{
						"active": map[string]interface{}{
							"uri":      "/health",
							"interval": "10s",
							"timeout":  "3s",
						},
					},
				},
			},
		}
		routes = append(routes, route)
	}

	config := map[string]interface{}{
		"apps": map[string]interface{}{
			"http": map[string]interface{}{
				"servers": map[string]interface{}{
					"lbpanel": map[string]interface{}{
						"listen": []string{":80", ":443"},
						"routes": routes,
					},
				},
			},
			"tls": map[string]interface{}{}, // Caddy auto HTTPS
		},
	}
	return config, nil
}

// caddyApplyConfig sends new config to Caddy via /load endpoint
func caddyApplyConfig(config map[string]interface{}) error {
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}

	resp, err := caddyClient.Post(
		caddyAPI+"/load",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("caddy apply: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy load failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

// caddyReloadFromDB rebuilds config from DB and applies it
func caddyReloadFromDB() error {
	sites, err := dbGetSites()
	if err != nil {
		return err
	}
	nodes, err := dbGetNodes()
	if err != nil {
		return err
	}

	config, err := caddyBuildConfig(sites, nodes)
	if err != nil {
		return err
	}

	if err := caddyApplyConfig(config); err != nil {
		dbAddLog(nil, "caddy_reload", "error: "+err.Error())
		return err
	}

	dbAddLog(nil, "caddy_reload", "ok")
	return nil
}

// caddyStatus checks if Caddy API is reachable
func caddyStatus() bool {
	resp, err := caddyClient.Get(caddyAPI + "/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// caddyGetCaddyfile returns a human-readable Caddyfile-style summary
// (not actual Caddyfile — Caddy JSON API doesn't expose Caddyfile)
func caddyGetCaddyfile(sites []Site, nodes []Node) string {
	var sb strings.Builder

	sb.WriteString("# lbpanel generated config summary\n\n")

	var nodeList []string
	for _, n := range nodes {
		nodeList = append(nodeList, fmt.Sprintf("%s:%d", n.IP, 80))
	}

	for _, s := range sites {
		if !s.Active {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s", s.Domain))
		if s.CDNSub != "" {
			sb.WriteString(fmt.Sprintf(", %s", s.CDNSub))
		}
		sb.WriteString(" {\n")
		sb.WriteString(fmt.Sprintf("    reverse_proxy %s {\n", strings.Join(nodeList, " ")))
		sb.WriteString(fmt.Sprintf("        lb_policy %s\n", s.LBPolicy))
		sb.WriteString("        health_uri /health\n")
		sb.WriteString("    }\n")
		sb.WriteString("}\n\n")
	}
	return sb.String()
}
