package main

import (
	"database/sql"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

// Node represents a web server (web01/02/03)
type Node struct {
	ID       int
	Name     string
	IP       string
	Port     int
	APIKey   string
	Status   string
	LastSeen *time.Time
	Created  time.Time
}

// Site represents a WordPress site with CDN config
type Site struct {
	ID          int
	Domain      string
	CDNSub      string
	WPSource    string
	LBPolicy    string
	Active      bool
	Created     time.Time
}

// LogEntry represents an operation log
type LogEntry struct {
	ID      int
	NodeID  *int
	Node    string // joined
	Action  string
	Result  string
	Created time.Time
}

func initDB(path string) {
	var err error
	db, err = sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	db.SetMaxOpenConns(1) // sqlite single writer

	schema := `
	CREATE TABLE IF NOT EXISTS nodes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		ip         TEXT NOT NULL,
		port       INTEGER DEFAULT 7313,
		api_key    TEXT UNIQUE NOT NULL,
		status     TEXT DEFAULT 'unknown',
		last_seen  DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sites (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		domain       TEXT NOT NULL UNIQUE,
		cdn_sub      TEXT,
		wp_source    TEXT,
		lb_policy    TEXT DEFAULT 'ip_hash',
		active       INTEGER DEFAULT 1,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS logs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id    INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
		action     TEXT NOT NULL,
		result     TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`

	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("db schema: %v", err)
	}
	log.Println("db: initialized")
}

// --- Nodes ---

func dbGetNodes() ([]Node, error) {
	rows, err := db.Query(`
		SELECT id, name, ip, port, api_key, status, last_seen, created_at
		FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		var lastSeen sql.NullTime
		err := rows.Scan(&n.ID, &n.Name, &n.IP, &n.Port, &n.APIKey,
			&n.Status, &lastSeen, &n.Created)
		if err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			n.LastSeen = &lastSeen.Time
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func dbGetNode(id int) (*Node, error) {
	var n Node
	var lastSeen sql.NullTime
	err := db.QueryRow(`
		SELECT id, name, ip, port, api_key, status, last_seen, created_at
		FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Name, &n.IP, &n.Port, &n.APIKey,
			&n.Status, &lastSeen, &n.Created)
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		n.LastSeen = &lastSeen.Time
	}
	return &n, nil
}

func dbGetNodeByKey(key string) (*Node, error) {
	var n Node
	var lastSeen sql.NullTime
	err := db.QueryRow(`
		SELECT id, name, ip, port, api_key, status, last_seen, created_at
		FROM nodes WHERE api_key = ?`, key).
		Scan(&n.ID, &n.Name, &n.IP, &n.Port, &n.APIKey,
			&n.Status, &lastSeen, &n.Created)
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		n.LastSeen = &lastSeen.Time
	}
	return &n, nil
}

func dbAddNode(name, ip string, port int, apiKey string) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO nodes (name, ip, port, api_key) VALUES (?, ?, ?, ?)`,
		name, ip, port, apiKey)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbUpdateNodeStatus(id int, status string) error {
	_, err := db.Exec(`
		UPDATE nodes SET status = ?, last_seen = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id)
	return err
}

func dbUpdateNodeKey(id int, newKey string) error {
	_, err := db.Exec(`UPDATE nodes SET api_key = ? WHERE id = ?`, newKey, id)
	return err
}

func dbDeleteNode(id int) error {
	_, err := db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

// --- Sites ---

func dbGetSites() ([]Site, error) {
	rows, err := db.Query(`
		SELECT id, domain, COALESCE(cdn_sub,''), COALESCE(wp_source,''),
		       lb_policy, active, created_at
		FROM sites ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		var active int
		err := rows.Scan(&s.ID, &s.Domain, &s.CDNSub, &s.WPSource,
			&s.LBPolicy, &active, &s.Created)
		if err != nil {
			return nil, err
		}
		s.Active = active == 1
		sites = append(sites, s)
	}
	return sites, nil
}

func dbGetSite(id int) (*Site, error) {
	var s Site
	var active int
	err := db.QueryRow(`
		SELECT id, domain, COALESCE(cdn_sub,''), COALESCE(wp_source,''),
		       lb_policy, active, created_at
		FROM sites WHERE id = ?`, id).
		Scan(&s.ID, &s.Domain, &s.CDNSub, &s.WPSource,
			&s.LBPolicy, &active, &s.Created)
	if err != nil {
		return nil, err
	}
	s.Active = active == 1
	return &s, nil
}

func dbAddSite(domain, cdnSub, wpSource, lbPolicy string) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO sites (domain, cdn_sub, wp_source, lb_policy)
		VALUES (?, ?, ?, ?)`,
		domain, cdnSub, wpSource, lbPolicy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbDeleteSite(id int) error {
	_, err := db.Exec(`DELETE FROM sites WHERE id = ?`, id)
	return err
}

// --- Logs ---

func dbAddLog(nodeID *int, action, result string) {
	_, err := db.Exec(`
		INSERT INTO logs (node_id, action, result) VALUES (?, ?, ?)`,
		nodeID, action, result)
	if err != nil {
		log.Printf("log write error: %v", err)
	}
}

func dbGetLogs(limit int) ([]LogEntry, error) {
	rows, err := db.Query(`
		SELECT l.id, l.node_id, COALESCE(n.name, 'system'), l.action, l.result, l.created_at
		FROM logs l
		LEFT JOIN nodes n ON n.id = l.node_id
		ORDER BY l.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []LogEntry
	for rows.Next() {
		var e LogEntry
		var nodeID sql.NullInt64
		err := rows.Scan(&e.ID, &nodeID, &e.Node, &e.Action, &e.Result, &e.Created)
		if err != nil {
			return nil, err
		}
		if nodeID.Valid {
			id := int(nodeID.Int64)
			e.NodeID = &id
		}
		logs = append(logs, e)
	}
	return logs, nil
}

// --- Settings ---

func dbGetSetting(key string) string {
	var val string
	db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	return val
}

func dbSetSetting(key, value string) error {
	_, err := db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
