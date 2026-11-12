package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"st-go/tui/writing"
)

type aiProposal struct {
	ID, Document, Request, Manifest, Instruction, Status string
	Text, Reasoning, Draft, Session, Error               string
}
type aiStore struct {
	db   *sql.DB
	root string
}

func openAIStore(root string) (*aiStore, error) {
	dir := filepath.Join(root, ".govim")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err == nil {
		_, _ = f.WriteString("*\n")
		_ = f.Close()
	}
	path := filepath.Join(dir, "ai.sqlite3")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=3000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS ai_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT OR IGNORE INTO ai_meta VALUES('schema','1');
CREATE TABLE IF NOT EXISTS proposals(id TEXT PRIMARY KEY,document TEXT NOT NULL,request TEXT NOT NULL,manifest TEXT NOT NULL,instruction TEXT NOT NULL,status TEXT NOT NULL,text TEXT NOT NULL DEFAULT '',reasoning TEXT NOT NULL DEFAULT '',draft TEXT NOT NULL DEFAULT '',session TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',updated INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS requests(id TEXT PRIMARY KEY,placeholder TEXT NOT NULL,manifest TEXT NOT NULL,instruction TEXT NOT NULL,status TEXT NOT NULL,text TEXT NOT NULL DEFAULT '',reasoning TEXT NOT NULL DEFAULT '',prediction TEXT NOT NULL DEFAULT '',session TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS adaptations(id INTEGER PRIMARY KEY,placeholder TEXT NOT NULL,request TEXT NOT NULL,text TEXT NOT NULL,kind TEXT NOT NULL,created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS acceptances(id INTEGER PRIMARY KEY,placeholder TEXT NOT NULL,request TEXT NOT NULL,adaptation INTEGER NOT NULL,status TEXT NOT NULL,document_hash TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY,placeholder TEXT NOT NULL,kind TEXT NOT NULL,detail TEXT NOT NULL,created INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	var version string
	if err = db.QueryRow("SELECT value FROM ai_meta WHERE key='schema'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, fmt.Errorf("unsupported AI history schema")
	}
	return &aiStore{db: db, root: root}, nil
}

func (s *aiStore) placeholder(p *aiProposal) error {
	_, err := s.db.Exec("INSERT INTO proposals(id,document,request,manifest,instruction,status,updated) VALUES(?,?,'','','','created',?)", p.ID, p.Document, time.Now().UnixNano())
	return err
}

func (s *aiStore) insertIntent(p *aiProposal) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO adaptations(placeholder,request,text,kind,created) VALUES(?,?,?,'insert-intent',?)", p.ID, p.Request, p.Draft, time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	revision, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	result, err = tx.Exec("INSERT INTO acceptances(placeholder,request,adaptation,status,created) VALUES(?,?,?,'intent',?)", p.ID, p.Request, revision, time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (s *aiStore) inserted(p *aiProposal, id int64, hash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE acceptances SET status='inserted',document_hash=? WHERE id=?", hash, id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE proposals SET status='inserted',updated=? WHERE id=? AND request=?", time.Now().UnixNano(), p.ID, p.Request); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO events(placeholder,kind,detail,created) VALUES(?,'inserted',?,?)", p.ID, fmt.Sprintf("acceptance:%d sha256:%s", id, hash), time.Now().UnixNano()); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		p.Status = "inserted"
	}
	return err
}
func (s *aiStore) all() ([]*aiProposal, error) {
	rows, err := s.db.Query("SELECT id,document,request,manifest,instruction,status,text,reasoning,draft,session,error FROM proposals ORDER BY updated DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*aiProposal
	for rows.Next() {
		p := &aiProposal{}
		if err := rows.Scan(&p.ID, &p.Document, &p.Request, &p.Manifest, &p.Instruction, &p.Status, &p.Text, &p.Reasoning, &p.Draft, &p.Session, &p.Error); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *aiStore) request(p *aiProposal) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO requests(id,placeholder,manifest,instruction,status,created) VALUES(?,?,?,?,?,?)`, p.Request, p.ID, p.Manifest, p.Instruction, p.Status, time.Now().UnixNano())
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO proposals(id,document,request,manifest,instruction,status,updated) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET request=excluded.request,manifest=excluded.manifest,instruction=excluded.instruction,status=excluded.status,error='',updated=excluded.updated`, p.ID, p.Document, p.Request, p.Manifest, p.Instruction, p.Status, time.Now().UnixNano())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *aiStore) response(m writing.Message) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE requests SET status=?,text=?,reasoning=?,prediction=?,session=?,error=? WHERE id=? AND status NOT IN ('result','failed','cancelled')`, m.Type, m.Text, m.Reasoning, m.Prediction, m.Session, m.Error, m.RequestID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE proposals SET status=?,text=?,reasoning=?,draft=CASE WHEN ?='result' THEN ? ELSE draft END,session=?,error=?,updated=? WHERE request=? AND status NOT IN ('inserted','dismissed','result','failed','cancelled')`, m.Type, m.Text, m.Reasoning, m.Type, m.Prediction, m.Session, m.Error, time.Now().UnixNano(), m.RequestID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *aiStore) adapt(p *aiProposal, text, kind string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO adaptations(placeholder,request,text,kind,created) VALUES(?,?,?,?,?)", p.ID, p.Request, text, kind, time.Now().UnixNano())
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE proposals SET draft=?,updated=? WHERE id=?", text, time.Now().UnixNano(), p.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *aiStore) event(p *aiProposal, kind, detail string) error {
	_, err := s.db.Exec("INSERT INTO events(placeholder,kind,detail,created) VALUES(?,?,?,?)", p.ID, kind, detail, time.Now().UnixNano())
	return err
}
func (s *aiStore) state(p *aiProposal, status string) error {
	_, err := s.db.Exec("UPDATE proposals SET status=?,updated=? WHERE id=?", status, time.Now().UnixNano(), p.ID)
	if err == nil {
		p.Status = status
	}
	return err
}
