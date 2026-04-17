// Glosse: kollaborativer Text mit Rand-Kommentaren.
//
// Ein editierbares Dokument steht im Zentrum. Am Rand schweben Kommentar-Karten,
// die per Selektion am markierten Text hängen, farbig nach Autor. Replies bilden
// flache Threads, Edit und Delete sind Author-beschränkt.
//
// Endpoints:
//   GET  /                    -> HTML-Shell (Login-Overlay + Layout)
//   GET  /health              -> ok
//   POST /auth                -> {"token": "..."} -> {"role": "read|write"} | 401
//   GET  /content             -> gespeichertes HTML-Fragment (Auth: read|write)
//   PUT  /content             -> HTML-Fragment speichern (Auth: write)
//   GET  /markdown            -> HTML -> Markdown Export (Auth: read|write)
//   GET  /comments            -> alle Kommentare (Auth: read|write)
//   POST /comments            -> neuen Kommentar anlegen (Auth: read|write)
//   DELETE /comments/:id      -> Kommentar loeschen (Auth: Autor oder write), kaskadiert
//   PUT    /comments/:id      -> Kommentar-Text aktualisieren (Auth: Autor oder write)
//
// Konfiguration via ENV:
//   GLOSSE_TOKEN_READ       (pflicht) Lese-Token fuer Leser
//   GLOSSE_TOKEN_WRITE      (pflicht) Schreib-Token fuer Autoren
//   GLOSSE_DIR              Datenverzeichnis, default /var/lib/glosse
//   GLOSSE_PORT             Port, default 3041
//   GLOSSE_TITLE            Angezeigter Titel ueber dem Dokument, default "Glosse"
//   GLOSSE_SUBTITLE         Untertitel (zweite Zeile), default leer
//   GLOSSE_ACCENT           Akzent-Schema: halo|aurora|ember|flux|moss, default halo
//   GLOSSE_MARLIN_NAME      Name, der automatisch Halo als Farbe bekommt (kann leer sein)
//
// Auth:
//   Header "Authorization: Bearer <token>" auf allen Endpoints ausser / und /health.
//   Fuer DELETE/PUT auf eigenen Kommentaren reicht ein Read-Token plus Header
//   "X-Author: <name>" der zum gespeicherten Autor passt.

package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed shell.html
var shellTemplate string

type Config struct {
	ReadToken   string
	WriteToken  string
	DirPath     string
	Port        string
	Title       string
	Subtitle    string
	Accent      string
	MarlinName  string
}

func loadConfig() Config {
	cfg := Config{
		ReadToken:  os.Getenv("GLOSSE_TOKEN_READ"),
		WriteToken: os.Getenv("GLOSSE_TOKEN_WRITE"),
		DirPath:    os.Getenv("GLOSSE_DIR"),
		Port:       os.Getenv("GLOSSE_PORT"),
		Title:      os.Getenv("GLOSSE_TITLE"),
		Subtitle:   os.Getenv("GLOSSE_SUBTITLE"),
		Accent:     strings.ToLower(os.Getenv("GLOSSE_ACCENT")),
		MarlinName: os.Getenv("GLOSSE_MARLIN_NAME"),
	}
	if cfg.DirPath == "" {
		cfg.DirPath = "/var/lib/glosse"
	}
	if cfg.Port == "" {
		cfg.Port = "3041"
	}
	if cfg.Title == "" {
		cfg.Title = "Glosse"
	}
	switch cfg.Accent {
	case "halo", "aurora", "ember", "flux", "moss":
	default:
		cfg.Accent = "halo"
	}
	if cfg.ReadToken == "" || cfg.WriteToken == "" {
		log.Fatal("GLOSSE_TOKEN_READ und GLOSSE_TOKEN_WRITE sind erforderlich")
	}
	return cfg
}

var accentRGB = map[string][2]string{
	"halo":   {"122, 162, 255", "168, 236, 255"},
	"aurora": {"0, 228, 255", "62, 255, 199"},
	"ember":  {"255, 122, 26", "255, 209, 102"},
	"flux":   {"255, 79, 163", "183, 107, 255"},
	"moss":   {"74, 222, 128", "227, 242, 107"},
}

func (cfg Config) renderShell() string {
	pair := accentRGB[cfg.Accent]
	replacements := map[string]string{
		"{{TITLE}}":        htmlEscape(cfg.Title),
		"{{SUBTITLE}}":     htmlEscape(cfg.Subtitle),
		"{{ACCENT_KEY}}":   cfg.Accent,
		"{{ACCENT_RGB}}":   pair[0],
		"{{ACCENT_2_RGB}}": pair[1],
		"{{MARLIN_NAME}}":  cfg.MarlinName,
	}
	out := shellTemplate
	for k, v := range replacements {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

type Comment struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	ColorScheme string `json:"colorScheme"`
	ParagraphID string `json:"paragraphId"`
	AnchorText  string `json:"anchorText"`
	Text        string `json:"text"`
	CreatedAt   string `json:"createdAt"`
	ParentID    string `json:"parentId,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

type server struct {
	cfg    Config
	shell  string
	mu     sync.Mutex
	docFP  string
	histFP string
}

func newServer(cfg Config) *server {
	if err := os.MkdirAll(cfg.DirPath, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", cfg.DirPath, err)
	}
	histDir := filepath.Join(cfg.DirPath, "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		log.Fatalf("mkdir history: %v", err)
	}
	return &server{
		cfg:    cfg,
		shell:  cfg.renderShell(),
		docFP:  filepath.Join(cfg.DirPath, "document.html"),
		histFP: histDir,
	}
}

func (s *server) tokenRole(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	got := []byte(h[len(prefix):])
	if subtle.ConstantTimeCompare(got, []byte(s.cfg.WriteToken)) == 1 {
		return "write"
	}
	if subtle.ConstantTimeCompare(got, []byte(s.cfg.ReadToken)) == 1 {
		return "read"
	}
	return ""
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, s.shell)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "ok")
}

func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read fail", http.StatusBadRequest)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	got := []byte(req.Token)
	role := ""
	if subtle.ConstantTimeCompare(got, []byte(s.cfg.WriteToken)) == 1 {
		role = "write"
	} else if subtle.ConstantTimeCompare(got, []byte(s.cfg.ReadToken)) == 1 {
		role = "read"
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"role": role})
}

func (s *server) handleContent(w http.ResponseWriter, r *http.Request) {
	role := s.tokenRole(r)
	if role == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		data, err := os.ReadFile(s.docFP)
		s.mu.Unlock()
		if err != nil {
			if os.IsNotExist(err) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, "<p><em>Leer. Schreib etwas rein.</em></p>")
				return
			}
			http.Error(w, "read fail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	case http.MethodPut:
		if role != "write" {
			http.Error(w, "write forbidden", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read fail", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		existing, _ := os.ReadFile(s.docFP)
		if len(existing) > 0 {
			stamp := time.Now().UTC().Format("20060102T150405")
			backup := filepath.Join(s.histFP, stamp+".html")
			_ = os.WriteFile(backup, existing, 0o644)
		}
		tmp := s.docFP + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			http.Error(w, "write fail", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, s.docFP); err != nil {
			_ = os.Remove(tmp)
			http.Error(w, "rename fail", http.StatusInternalServerError)
			return
		}
		log.Printf("content updated (%d bytes)", len(body))
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleMarkdown(w http.ResponseWriter, r *http.Request) {
	role := s.tokenRole(r)
	if role == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	data, err := os.ReadFile(s.docFP)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "read fail", http.StatusInternalServerError)
		return
	}
	md := htmlFragmentToMarkdown(string(data))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = io.WriteString(w, md)
}

var (
	reH1       = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	reH2       = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`)
	reH3       = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`)
	reP        = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	reStrong   = regexp.MustCompile(`(?is)<(strong|b)[^>]*>(.*?)</(strong|b)>`)
	reEm       = regexp.MustCompile(`(?is)<(em|i)[^>]*>(.*?)</(em|i)>`)
	reBr       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reMeta     = regexp.MustCompile(`(?is)<div class="meta"[^>]*>.*?</div>`)
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reWSpace   = regexp.MustCompile(`\n{3,}`)
	reEntities = map[string]string{
		"&amp;":  "&",
		"&lt;":   "<",
		"&gt;":   ">",
		"&quot;": "\"",
		"&#39;":  "'",
		"&nbsp;": " ",
	}
)

func htmlFragmentToMarkdown(h string) string {
	h = reMeta.ReplaceAllString(h, "")
	h = reStrong.ReplaceAllString(h, "**$2**")
	h = reEm.ReplaceAllString(h, "*$2*")
	h = reBr.ReplaceAllString(h, "\n")
	h = reH1.ReplaceAllString(h, "\n# $1\n\n")
	h = reH2.ReplaceAllString(h, "\n## $1\n\n")
	h = reH3.ReplaceAllString(h, "\n### $1\n\n")
	h = reP.ReplaceAllString(h, "$1\n\n")
	h = reTag.ReplaceAllString(h, "")
	for k, v := range reEntities {
		h = strings.ReplaceAll(h, k, v)
	}
	h = reWSpace.ReplaceAllString(h, "\n\n")
	return strings.TrimSpace(h) + "\n"
}

func (s *server) commentsPath() string {
	return filepath.Join(s.cfg.DirPath, "comments.json")
}

func (s *server) loadComments() []Comment {
	out := []Comment{}
	data, err := os.ReadFile(s.commentsPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func (s *server) saveComments(list []Comment) error {
	tmp := s.commentsPath() + ".tmp"
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.commentsPath())
}

func genID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) handleComments(w http.ResponseWriter, r *http.Request) {
	role := s.tokenRole(r)
	if role == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		list := s.loadComments()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "read fail", http.StatusBadRequest)
			return
		}
		var c Comment
		if err := json.Unmarshal(body, &c); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		c.Author = strings.TrimSpace(c.Author)
		c.Text = strings.TrimSpace(c.Text)
		c.AnchorText = strings.TrimSpace(c.AnchorText)
		c.ParagraphID = strings.TrimSpace(c.ParagraphID)
		c.ParentID = strings.TrimSpace(c.ParentID)
		if c.Author == "" || c.Text == "" {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}
		if c.ParagraphID == "" && c.ParentID == "" {
			http.Error(w, "missing paragraphId", http.StatusBadRequest)
			return
		}
		if len(c.Author) > 64 || len(c.Text) > 4000 || len(c.AnchorText) > 1000 {
			http.Error(w, "too long", http.StatusBadRequest)
			return
		}
		switch c.ColorScheme {
		case "aurora", "ember", "flux", "moss", "halo":
		default:
			c.ColorScheme = "halo"
		}
		c.ID = genID()
		c.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		s.mu.Lock()
		list := s.loadComments()
		list = append(list, c)
		err = s.saveComments(list)
		s.mu.Unlock()
		if err != nil {
			http.Error(w, "save fail", http.StatusInternalServerError)
			return
		}
		log.Printf("comment added id=%s author=%s", c.ID, c.Author)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleCommentByID(w http.ResponseWriter, r *http.Request, id string) {
	role := s.tokenRole(r)
	if role == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	requestingAuthor := strings.TrimSpace(r.Header.Get("X-Author"))

	switch r.Method {
	case http.MethodDelete:
		s.mu.Lock()
		defer s.mu.Unlock()
		list := s.loadComments()
		var target *Comment
		for i := range list {
			if list[i].ID == id {
				target = &list[i]
				break
			}
		}
		if target == nil {
			http.NotFound(w, r)
			return
		}
		if role != "write" && (requestingAuthor == "" || target.Author != requestingAuthor) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		toDelete := map[string]bool{id: true}
		changed := true
		for changed {
			changed = false
			for _, c := range list {
				if !toDelete[c.ID] && c.ParentID != "" && toDelete[c.ParentID] {
					toDelete[c.ID] = true
					changed = true
				}
			}
		}
		out := make([]Comment, 0, len(list))
		for _, c := range list {
			if !toDelete[c.ID] {
				out = append(out, c)
			}
		}
		if err := s.saveComments(out); err != nil {
			http.Error(w, "save fail", http.StatusInternalServerError)
			return
		}
		log.Printf("comment deleted id=%s (+%d descendants) by=%s role=%s", id, len(toDelete)-1, requestingAuthor, role)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
		if err != nil {
			http.Error(w, "read fail", http.StatusBadRequest)
			return
		}
		var update struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &update); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(update.Text)
		if text == "" || len(text) > 4000 {
			http.Error(w, "invalid text", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		list := s.loadComments()
		idx := -1
		for i := range list {
			if list[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		if role != "write" && (requestingAuthor == "" || list[idx].Author != requestingAuthor) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		list[idx].Text = text
		list[idx].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveComments(list); err != nil {
			http.Error(w, "save fail", http.StatusInternalServerError)
			return
		}
		log.Printf("comment edited id=%s by=%s role=%s", id, requestingAuthor, role)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list[idx])
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func main() {
	cfg := loadConfig()
	srv := newServer(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleRoot)
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc("/auth", srv.handleAuth)
	mux.HandleFunc("/content", srv.handleContent)
	mux.HandleFunc("/markdown", srv.handleMarkdown)
	mux.HandleFunc("/comments", srv.handleComments)
	mux.HandleFunc("/comments/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/comments/")
		id = strings.Trim(id, "/")
		if id == "" {
			srv.handleComments(w, r)
			return
		}
		srv.handleCommentByID(w, r, id)
	})

	addr := ":" + cfg.Port
	log.Printf("glosse listening on %s, dir=%s, title=%q, accent=%s", addr, cfg.DirPath, cfg.Title, cfg.Accent)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
