// Package qf is the read-only backend of the Qingflow ("轻流") dashboard.
//
// It proxies only three upstream reads:
//
//	GET  /tag/apps                        application packages / tree
//	POST /app/{key}/apply/filter/amount   record counts, optionally by date
//	GET  /app/{key}/publish               last publish time
//
// Nothing here creates, updates or deletes anything in Qingflow.
package qf

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"implboard/internal/diskstore"
)

// Options configures a Server.
type Options struct {
	// AuthPath is the credentials file (Python's QF_AUTH / qf_auth.json).
	// Empty means: QF_AUTH env, then DataDir, then the per-user data dir.
	AuthPath string
	// DataDir is the writable working directory. It is used as the default
	// location of the auth file; progress and caches stay in memory, exactly
	// like the Python server.
	DataDir string
}

// Server serves the dashboard API. Credentials are re-read on every request so
// an account switch shows up immediately.
type Server struct {
	authPath string
	dataDir  string

	authMu sync.Mutex

	prog  *progress
	cache *cache
	disk  *diskstore.Store

	workers int

	// Overridable in tests; production always talks to qingflow.com.
	baseURL    string
	httpClient *http.Client
}

// New loads the saved credentials. A missing or broken auth file is not an
// error: the server then behaves like the Python one before an account is
// configured and answers 401 until one is added.
func New(opts Options) (*Server, error) {
	dataDir := opts.DataDir
	if dataDir != "" {
		_ = os.MkdirAll(dataDir, 0o755)
	}

	authPath := opts.AuthPath
	if authPath == "" {
		authPath = defaultAuthPath(dataDir)
	}

	workers := 16
	if v := os.Getenv("QF_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}

	seedFromEnv(authPath)

	var disk *diskstore.Store
	if dir := dataDir; dir != "" {
		if d, err := diskstore.Open(dir+string(os.PathSeparator)+"cache", diskstore.DefaultMaxBytes); err == nil {
			disk = d
		}
	}

	return &Server{
		authPath:   authPath,
		dataDir:    dataDir,
		disk:       disk,
		prog:       newProgress(),
		cache:      newCache(),
		workers:    workers,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: requestTimeout},
	}, nil
}

// Handler returns the HTTP handler for everything under /api/. Static pages are
// the caller's business; unknown paths get the same JSON 404 the Python server
// returns for unmatched API paths.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.serveGet(w, r)
	case http.MethodPost:
		s.servePost(w, r)
	default:
		writeText(w, http.StatusNotImplemented, "Unsupported method")
	}
}

func (s *Server) serveGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if path == "/api/auth" {
		writeJSON(w, http.StatusOK, s.authView(s.loadAuth()))
		return
	}
	if path == "/api/progress" {
		writeJSON(w, http.StatusOK, s.prog.snapshot())
		return
	}
	if path == "/api/cache/days" {
		days := []string{}
		if s.disk != nil {
			if prof := s.activeProfile(); prof != nil {
				days = s.disk.Days("qf", prof.ID, "")
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"days": days})
		return
	}
	if path == "/api/cache/size" {
		var acct, total int64
		if s.disk != nil {
			total = s.disk.Total()
			if prof := s.activeProfile(); prof != nil {
				acct = s.disk.Size("qf", prof.ID)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": acct,
			"total":   total,
			"limit":   diskstore.DefaultMaxBytes,
		})
		return
	}

	prof := s.activeProfile()
	if prof == nil {
		writeJSON(w, http.StatusUnauthorized, errorBody("未配置账号"))
		return
	}
	api := s.apiFor(prof)

	switch path {
	case "/api/apps":
		tags, err := api.tree()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		s.cache.storeApps(profileKey(prof.Token), tags)
		groups := flatten(tags)
		total := 0
		for _, g := range groups {
			total += len(g.Apps)
		}
		writeJSON(w, http.StatusOK, appsResponse{Groups: groups, Apps: total})
		return

	case "/api/report":
		q := r.URL.Query()
		day := q.Get("day")
		if day == "" {
			// "date" is accepted as an alias; the Python server only knows "day".
			day = q.Get("date")
		}
		if day == "" {
			day = today()
		}
		report, err := s.buildReport(api, day, q.Get("fresh") == "1")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}

	writeJSON(w, http.StatusNotFound, errorBody("not found"))
}

type appsResponse struct {
	Groups []AppGroup `json:"groups"`
	Apps   int        `json:"apps"`
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/auth" {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = unmarshalJSON(raw, &body)
	}
	if body == nil {
		body = map[string]any{}
	}

	op := asString(body["op"])
	if op == "" {
		op = "add"
	}

	s.authMu.Lock()
	defer s.authMu.Unlock()

	cfg := loadAuth(s.authPath)
	switch op {
	case "add":
		info := parseCurl(asString(body["curl"]))
		if info.Token == "" {
			writeJSON(w, http.StatusBadRequest, errorBody(
				"没有从 cURL 里解析到 token，请确认复制了 -H 'token: ...' 或 prodtoken cookie"))
			return
		}
		if info.WSID == "" {
			writeJSON(w, http.StatusBadRequest, errorBody(
				"没有解析到 wsid（-H 'wsid: ...' 或 prodwsId cookie）"))
			return
		}
		name := asString(body["name"])
		if name == "" {
			name = "wsid " + info.WSID
		}
		cfg.Profiles = append(cfg.Profiles, Profile{
			ID:          "q" + strconv.FormatInt(time.Now().Unix(), 10),
			Name:        name,
			Token:       info.Token,
			WSID:        info.WSID,
			QFVersion:   info.QFVersion,
			CookieExtra: info.CookieExtra,
		})
		cfg.setActive(cfg.Profiles[len(cfg.Profiles)-1].ID)
		s.cache.clearReports()

	case "switch":
		id := asString(body["id"])
		if id == "" {
			cfg.Active = nil
		} else {
			cfg.setActive(id)
		}

	case "delete":
		id := asString(body["id"])
		kept := make([]Profile, 0, len(cfg.Profiles))
		for _, p := range cfg.Profiles {
			if p.ID != id {
				kept = append(kept, p)
			}
		}
		cfg.Profiles = kept
		s.dropDiskCache(id)
		if cfg.Active != nil && *cfg.Active == id {
			if len(kept) > 0 {
				cfg.setActive(kept[0].ID)
			} else {
				cfg.Active = nil
			}
		}
		s.cache.clearReports()
	}

	if err := saveAuth(s.authPath, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, authSaveResponse{OK: true, Active: cfg.Active})
}

type authSaveResponse struct {
	OK     bool    `json:"ok"`
	Active *string `json:"active"`
}

type authProfileView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	WSID any    `json:"wsid"`
}

type authView struct {
	Active   *string           `json:"active"`
	Profiles []authProfileView `json:"profiles"`
}

func (s *Server) authView(cfg *AuthConfig) authView {
	view := authView{Active: cfg.Active, Profiles: make([]authProfileView, 0, len(cfg.Profiles))}
	for _, p := range cfg.Profiles {
		view.Profiles = append(view.Profiles, authProfileView{ID: p.ID, Name: p.Name, WSID: p.WSID})
	}
	return view
}

func (s *Server) loadAuth() *AuthConfig {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return loadAuth(s.authPath)
}

func (s *Server) activeProfile() *Profile {
	return s.loadAuth().ActiveProfile()
}

func (s *Server) apiFor(p *Profile) *qfAPI {
	return newQfAPI(p, s.baseURL, s.httpClient)
}

// errorBody is the {"error": "..."} envelope every non-2xx response uses.
func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := marshalNoEscape(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"encoding failed"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

// DataDir reports the writable directory the server was configured with.
func (s *Server) DataDir() string {
	if s.dataDir != "" {
		return s.dataDir
	}
	return DefaultDataDir()
}
