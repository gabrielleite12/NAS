package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	remoteDBName = ".nas.db"
	legacyJSON   = ".nas_database.json"
)

var (
	sqlDB     *sql.DB
	dbMutex   sync.Mutex
	localDB   string
	saveTimer *time.Timer
)

func isHiddenMeta(name string) bool {
	return name == remoteDBName || name == legacyJSON || strings.HasPrefix(name, ".nas")
}

func normalizeVirt(p string) string {
	p = path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(p), "/"))
	if p == "." || p == "" {
		return "/"
	}
	return p
}

func initDB() {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "naslocal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("Falha ao criar cache do banco: %v", err)
	}
	localDB = filepath.Join(dir, "nas.db")

	if err := openSQL(); err != nil {
		log.Fatalf("Falha ao iniciar banco local: %v", err)
	}
	if err := ensureSchema(); err != nil {
		log.Fatalf("Falha no schema do banco: %v", err)
	}
}

func openSQL() error {
	var err error
	sqlDB, err = sql.Open("sqlite", localDB)
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	_, _ = sqlDB.Exec(`PRAGMA journal_mode=DELETE`)
	return nil
}

func ensureSchema() error {
	_, err := sqlDB.Exec(`
CREATE TABLE IF NOT EXISTS file_owners (
	path TEXT PRIMARY KEY,
	owner TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'file',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS activity_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	time TEXT NOT NULL,
	user TEXT NOT NULL,
	action TEXT NOT NULL,
	item TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_time ON activity_logs(id DESC);
`)
	return err
}

func loadDB() {
	dbMutex.Lock()
	defer dbMutex.Unlock()

	remote := path.Join(config.FtpRoot, remoteDBName)
	buf := &bytes.Buffer{}
	err := ftpStorage.Download(remote, buf)
	if err == nil && buf.Len() > 0 {
		_ = sqlDB.Close()
		if werr := os.WriteFile(localDB, buf.Bytes(), 0644); werr != nil {
			log.Printf("[DB] Erro ao gravar cache local: %v", werr)
		}
		if oerr := openSQL(); oerr != nil {
			log.Printf("[DB] Erro ao reabrir sqlite: %v", oerr)
			return
		}
		_ = ensureSchema()
		log.Printf("[DB] Carregado %s do NAS (%d bytes)", remoteDBName, buf.Len())
		return
	}

	migrateLegacyJSONLocked()
	scheduleSaveLocked()
	log.Printf("[DB] Usando banco local; será publicado como %s no NAS", remoteDBName)
}

func migrateLegacyJSONLocked() {
	legacyPath := path.Join(config.FtpRoot, legacyJSON)
	buf := &bytes.Buffer{}
	if err := ftpStorage.Download(legacyPath, buf); err != nil || buf.Len() == 0 {
		return
	}
	var legacy struct {
		Logs       []LogEntry        `json:"logs"`
		FileOwners map[string]string `json:"file_owners"`
	}
	if err := json.Unmarshal(buf.Bytes(), &legacy); err != nil {
		return
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		return
	}
	for p, owner := range legacy.FileOwners {
		np := normalizeVirt(p)
		_, _ = tx.Exec(
			`INSERT OR REPLACE INTO file_owners(path, owner, kind, created_at) VALUES(?,?,?,?)`,
			np, owner, "file", time.Now().Format(time.RFC3339Nano),
		)
	}
	for i := len(legacy.Logs) - 1; i >= 0; i-- {
		e := legacy.Logs[i]
		t := e.Time.Format(time.RFC3339Nano)
		if e.Time.IsZero() {
			t = time.Now().Format(time.RFC3339Nano)
		}
		_, _ = tx.Exec(
			`INSERT INTO activity_logs(time, user, action, item) VALUES(?,?,?,?)`,
			t, e.User, e.Action, e.Item,
		)
	}
	_ = tx.Commit()
	log.Printf("[DB] Migrado %s -> %s (%d owners, %d logs)", legacyJSON, remoteDBName, len(legacy.FileOwners), len(legacy.Logs))
}

func scheduleSaveLocked() {
	if saveTimer != nil {
		saveTimer.Stop()
	}
	saveTimer = time.AfterFunc(800*time.Millisecond, func() {
		if err := persistDBToNAS(); err != nil {
			log.Printf("[DB] Erro ao salvar no NAS: %v", err)
		}
	})
}

func persistDBToNAS() error {
	dbMutex.Lock()
	defer dbMutex.Unlock()
	if sqlDB == nil {
		return fmt.Errorf("db nil")
	}

	_, _ = sqlDB.Exec(`PRAGMA wal_checkpoint(FULL)`)
	_ = sqlDB.Close()

	data, err := os.ReadFile(localDB)
	if err != nil {
		_ = openSQL()
		return err
	}

	remote := path.Join(config.FtpRoot, remoteDBName)
	upErr := ftpStorage.Upload(remote, bytes.NewReader(data))

	if oerr := openSQL(); oerr != nil {
		return oerr
	}
	if upErr == nil {
		log.Printf("[DB] Sincronizado %s (%d bytes)", remoteDBName, len(data))
	}
	return upErr
}

func saveDB() {
	_ = persistDBToNAS()
}

func setOwner(virtPath, owner, kind string) {
	virtPath = normalizeVirt(virtPath)
	if kind == "" {
		kind = "file"
	}
	dbMutex.Lock()
	defer dbMutex.Unlock()
	_, err := sqlDB.Exec(
		`INSERT OR REPLACE INTO file_owners(path, owner, kind, created_at) VALUES(?,?,?,?)`,
		virtPath, owner, kind, time.Now().Format(time.RFC3339Nano),
	)
	if err != nil {
		log.Printf("[DB] setOwner: %v", err)
		return
	}
	scheduleSaveLocked()
}

func getOwner(virtPath string) string {
	virtPath = normalizeVirt(virtPath)
	dbMutex.Lock()
	defer dbMutex.Unlock()
	var owner string
	err := sqlDB.QueryRow(`SELECT owner FROM file_owners WHERE path = ?`, virtPath).Scan(&owner)
	if err != nil {
		return ""
	}
	return owner
}

func deleteOwnerTree(virtPath string) {
	virtPath = normalizeVirt(virtPath)
	dbMutex.Lock()
	defer dbMutex.Unlock()
	if virtPath == "/" {
		_, _ = sqlDB.Exec(`DELETE FROM file_owners`)
	} else {
		_, _ = sqlDB.Exec(
			`DELETE FROM file_owners WHERE path = ? OR path LIKE ?`,
			virtPath, virtPath+"/%",
		)
	}
	scheduleSaveLocked()
}

func renameOwner(oldPath, newPath string) {
	oldPath = normalizeVirt(oldPath)
	newPath = normalizeVirt(newPath)
	dbMutex.Lock()
	defer dbMutex.Unlock()

	_, _ = sqlDB.Exec(`UPDATE file_owners SET path = ? WHERE path = ?`, newPath, oldPath)

	rows, err := sqlDB.Query(`SELECT path FROM file_owners WHERE path LIKE ?`, oldPath+"/%")
	if err != nil {
		scheduleSaveLocked()
		return
	}
	var kids []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			kids = append(kids, p)
		}
	}
	rows.Close()
	for _, p := range kids {
		np := newPath + strings.TrimPrefix(p, oldPath)
		_, _ = sqlDB.Exec(`UPDATE file_owners SET path = ? WHERE path = ?`, np, p)
	}
	scheduleSaveLocked()
}

// pruneOwnersInDir remove metadados de itens que não existem mais nesta pasta.
func pruneOwnersInDir(dirPath string, existingNames map[string]bool) {
	dirPath = normalizeVirt(dirPath)
	dbMutex.Lock()
	defer dbMutex.Unlock()

	rows, err := sqlDB.Query(`SELECT path FROM file_owners`)
	if err != nil {
		return
	}
	var stale []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		p = normalizeVirt(p)
		parent := path.Dir(p)
		if parent == "." {
			parent = "/"
		}
		if parent != dirPath {
			continue
		}
		name := path.Base(p)
		if !existingNames[name] {
			stale = append(stale, p)
		}
	}
	rows.Close()

	for _, p := range stale {
		_, _ = sqlDB.Exec(`DELETE FROM file_owners WHERE path = ? OR path LIKE ?`, p, p+"/%")
	}
	if len(stale) > 0 {
		log.Printf("[DB] Removidos %d metadados órfãos em %s", len(stale), dirPath)
		scheduleSaveLocked()
	}
}

func clearAllMeta() {
	dbMutex.Lock()
	defer dbMutex.Unlock()
	_, _ = sqlDB.Exec(`DELETE FROM file_owners`)
	_, _ = sqlDB.Exec(`DELETE FROM activity_logs`)
	scheduleSaveLocked()
}

func addLog(user, action, item string) {
	dbMutex.Lock()
	defer dbMutex.Unlock()
	_, err := sqlDB.Exec(
		`INSERT INTO activity_logs(time, user, action, item) VALUES(?,?,?,?)`,
		time.Now().Format(time.RFC3339Nano), user, action, item,
	)
	if err != nil {
		log.Printf("[DB] addLog: %v", err)
		return
	}
	_, _ = sqlDB.Exec(`
		DELETE FROM activity_logs WHERE id NOT IN (
			SELECT id FROM activity_logs ORDER BY id DESC LIMIT 500
		)`)
	scheduleSaveLocked()
}

func getLogs() []LogEntry {
	dbMutex.Lock()
	defer dbMutex.Unlock()
	rows, err := sqlDB.Query(`SELECT time, user, action, item FROM activity_logs ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return []LogEntry{}
	}
	defer rows.Close()
	out := make([]LogEntry, 0)
	for rows.Next() {
		var e LogEntry
		var t string
		if rows.Scan(&t, &e.User, &e.Action, &e.Item) != nil {
			continue
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, t)
		out = append(out, e)
	}
	return out
}
