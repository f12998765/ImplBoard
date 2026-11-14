package jdy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"implboard/internal/diskstore"
)

const (
	reportTTL = 60 * time.Second
	trendTTL  = 900 * time.Second
	appsTTL   = 600 * time.Second

	maxRequestBody = 1 << 20 // 1 MiB is plenty for a pasted cURL
)

var errNotAuthenticated = errors.New("未认证")

// Options configures the board backend.
type Options struct {
	// AuthPath is the credential file. Empty means $JDY_AUTH, then
	// <DataDir>/jdy_auth.json — the same resolution the Python service uses.
	AuthPath string
	// DataDir is the writable directory for credentials, progress and caches.
	// Empty means the platform default (macOS: ~/Library/Application Support/ImplBoard).
	DataDir string
}

// Server owns the credential set, the read-only client and every cache.
type Server struct {
	mu           sync.Mutex
	authPath     string
	dataDir      string
	client       *Client
	defaultAppID string
	workers      int
	progress     *progressStore

	activeID string
	disk     *diskstore.Store

	reports map[cacheKey]*reportEntry
	trends  map[cacheKey]*trendEntry
	locks   map[string]*sync.Mutex

	appsCache []AppItem
	appsAt    float64
}

type cacheKey struct {
	appID string
	day   string
}

type reportEntry struct {
	report *Report
	at     float64
}

type trendEntry struct {
	trend *Trend
	at    float64
}

// New loads the credential store and verifies the active profile. A missing,
// expired or unusable credential is not an error: the server simply starts in
// the "not authenticated" state, exactly like the Python service.
func New(opts Options) (*Server, error) {
	dataDir, err := resolveDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	authPath := opts.AuthPath
	if authPath == "" {
		authPath = os.Getenv("JDY_AUTH")
	}
	if authPath == "" {
		authPath = filepath.Join(dataDir, "jdy_auth.json")
	}

	disk, derr := diskstore.Open(filepath.Join(dataDir, "cache"), diskstore.DefaultMaxBytes)
	if derr != nil {
		// A broken cache directory must not stop the board: fall back to memory.
		disk = nil
	}

	s := &Server{
		authPath:     expandHome(authPath),
		dataDir:      dataDir,
		disk:         disk,
		defaultAppID: DefaultAppID,
		workers:      defaultWorkers,
		progress:     newProgressStore(),
		reports:      make(map[cacheKey]*reportEntry),
		trends:       make(map[cacheKey]*trendEntry),
		locks:        make(map[string]*sync.Mutex),
	}
	s.activateSavedProfile()
	return s, nil
}

// Handler serves /api/... and /favicon.ico only. Everything else is a 404 so
// the surrounding board can own the static page.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.serveGet(w, r)
	case http.MethodPost:
		s.servePost(w, r)
	default:
		writeText(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- credential activation ----------

// activateSavedProfile mirrors the Python start-up path: verify the profile
// marked active and adopt it, silently giving up if it no longer works.
func (s *Server) activateSavedProfile() {
	st := loadStore(s.authPath)
	if st.Active == "" {
		return
	}
	if findProfile(st.Profiles, st.Active) == nil {
		return
	}
	_, _ = s.switchProfile(st.Active)
}

func (s *Server) activate(c *Client, appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = c
	s.defaultAppID = appID
	s.activeID = ""
	s.reports = make(map[cacheKey]*reportEntry)
	s.trends = make(map[cacheKey]*trendEntry)
	s.appsCache = nil
	s.appsAt = 0
}

func (s *Server) currentClient() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Server) defaultApp() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultAppID
}

func (s *Server) workerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workers
}

func (s *Server) ensure() (*Client, error) {
	if c := s.currentClient(); c != nil {
		return c, nil
	}
	return nil, errNotAuthenticated
}

// ---------- auth store operations ----------

func (s *Server) profiles() []Profile {
	st := loadStore(s.authPath)
	out := make([]Profile, 0, len(st.Profiles))
	for _, p := range st.Profiles {
		out = append(out, publicProfile(p, st.Active))
	}
	return out
}

// setAuth validates a credential against the live API, then stores it as a
// profile keyed by host+appId (an existing one is overwritten).
func (s *Server) setAuth(cookie, csrf, host, scheme, appID string, extra map[string]string) (*Client, error) {
	c := newClient(host, scheme, cookie, csrf, extra)
	c.appID = appID
	if _, err := c.EntryMap(); err != nil {
		return nil, err
	}

	st := loadStore(s.authPath)
	now := time.Now().Unix()
	var prof map[string]any
	for _, p := range st.Profiles {
		if asString(p["host"]) == host && asString(p["app_id"]) == appID {
			prof = p
			break
		}
	}
	if prof == nil {
		prof = map[string]any{
			"id":        "p" + randomHex(4),
			"createdAt": now,
		}
		st.Profiles = append(st.Profiles, prof)
	}
	defaultName := host
	if strings.HasPrefix(host, "lark.") {
		defaultName = "飞书 " + host
	}
	prof["name"] = orDefault(asString(prof["name"]), defaultName)
	prof["host"] = host
	prof["scheme"] = scheme
	prof["app_id"] = appID
	prof["cookie"] = cookie
	prof["csrf"] = csrf
	prof["user_agent"] = extra["user-agent"]
	prof["jdy_ver"] = extra["x-jdy-ver"]
	prof["updatedAt"] = now
	st.Active = asString(prof["id"])
	_ = saveStore(s.authPath, st)

	s.activate(c, appID)
	s.setActiveID(asString(prof["id"]))
	return c, nil
}

// switchProfile verifies and activates a stored profile.
func (s *Server) switchProfile(id string) (Profile, error) {
	st := loadStore(s.authPath)
	prof := findProfile(st.Profiles, id)
	if prof == nil {
		return Profile{}, errors.New("没有这个凭据")
	}
	c := newClient(
		asString(prof["host"]),
		orDefault(asString(prof["scheme"]), "https"),
		asString(prof["cookie"]),
		asString(prof["csrf"]),
		map[string]string{
			"user-agent": asString(prof["user_agent"]),
			"x-jdy-ver":  asString(prof["jdy_ver"]),
		},
	)
	c.appID = orDefault(asString(prof["app_id"]), DefaultAppID)
	if _, err := c.EntryMap(); err != nil {
		return Profile{}, err
	}

	st.Active = id
	_ = saveStore(s.authPath, st)
	s.activate(c, c.appID)
	s.setActiveID(id)
	return publicProfile(prof, id), nil
}

func (s *Server) deleteProfile(id string) error {
	st := loadStore(s.authPath)
	before := len(st.Profiles)
	kept := make([]map[string]any, 0, len(st.Profiles))
	for _, p := range st.Profiles {
		if asString(p["id"]) != id {
			kept = append(kept, p)
		}
	}
	if len(kept) == before {
		return errors.New("没有这个凭据")
	}
	st.Profiles = kept
	if st.Active == id {
		st.Active = ""
		s.mu.Lock()
		s.client = nil
		s.reports = make(map[cacheKey]*reportEntry)
		s.trends = make(map[cacheKey]*trendEntry)
		s.appsCache = nil
		s.appsAt = 0
		s.mu.Unlock()
	}
	_ = saveStore(s.authPath, st)
	return nil
}

// ---------- 磁盘缓存 ----------

// account is the credential id the cache is namespaced by. Editing a cURL
// keeps the same id, so its reports stay valid; deleting the account drops them.
func (s *Server) account() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID != "" {
		return s.activeID
	}
	st := loadStore(s.authPath)
	if st.Active != "" {
		return st.Active
	}
	return "_"
}

func (s *Server) setActiveID(id string) {
	s.mu.Lock()
	s.activeID = id
	s.mu.Unlock()
}

func (s *Server) loadDiskReport(acct, appID, day string) *Report {
	if s.disk == nil {
		return nil
	}
	e, ok := s.disk.Get("jdy", acct, appID, day)
	if !ok {
		return nil
	}
	var rep Report
	if err := json.Unmarshal(e.Data, &rep); err != nil {
		return nil
	}
	return &rep
}

func (s *Server) saveDiskReport(acct, appID string, rep *Report) {
	if s.disk == nil || rep == nil {
		return
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return
	}
	ts := time.Now().UnixMilli()
	_ = s.disk.Put("jdy", acct, appID, rep.Date, ts, body)
}

func (s *Server) diskTotal() int64 {
	if s.disk == nil {
		return 0
	}
	return s.disk.Total()
}

func (s *Server) dropDiskCache(acct string) {
	if s.disk == nil || acct == "" {
		return
	}
	_ = s.disk.Drop("jdy", acct)
}

// ---------- caches ----------

func (s *Server) keylock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lk := s.locks[key]
	if lk == nil {
		lk = &sync.Mutex{}
		s.locks[key] = lk
	}
	return lk
}

// ---------- read operations ----------

func (s *Server) apps() ([]AppItem, error) {
	base, err := s.ensure()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.appsCache) > 0 && unixNow()-s.appsAt < appsTTL.Seconds() {
		cached := s.appsCache
		s.mu.Unlock()
		return cached, nil
	}
	appID := s.defaultAppID
	s.mu.Unlock()

	apps := fetchApps(base, appID)
	resolveNames(base, apps)
	active := s.account()

	// 标上「这个包是从哪个账号列出来的」：切包时按它去命中那个账号的缓存。
	// 每个账号只额外拉一次应用列表（受 appsTTL 约束），拿不到就只在已缓存
	// 的结果里认领，绝不会给别的账号乱标。
	s.mu.Lock()
	owned := make(map[string]string, len(apps))
	for _, a := range apps {
		owned[a.AppID] = active
	}
	s.mu.Unlock()

	for _, p := range s.otherProfiles(active) {
		c, err := s.clientFromProfile(p)
		if err != nil {
			continue
		}
		for _, a := range fetchAppIDsQuiet(c) {
			s.mu.Lock()
			if _, taken := owned[a]; !taken {
				owned[a] = asString(p["id"])
			}
			s.mu.Unlock()
		}
	}

	for i := range apps {
		s.mu.Lock()
		apps[i].Account = owned[apps[i].AppID]
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.appsCache, s.appsAt = apps, unixNow()
	s.mu.Unlock()
	return apps, nil
}

func (s *Server) findProfile(id string) map[string]any {
	st := loadStore(s.authPath)
	for _, p := range st.Profiles {
		if asString(p["id"]) == id {
			return p
		}
	}
	return nil
}

// otherProfiles lists stored credentials except the active one.
func (s *Server) otherProfiles(skipID string) []map[string]any {
	st := loadStore(s.authPath)
	out := make([]map[string]any, 0, len(st.Profiles))
	for _, p := range st.Profiles {
		if asString(p["id"]) == skipID {
			continue
		}
		if asString(p["cookie"]) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// clientFromProfile builds a client from a stored credential without
// disturbing the active profile.
func (s *Server) clientFromProfile(p map[string]any) (*Client, error) {
	host := asString(p["host"])
	if host == "" {
		return nil, errors.New("凭据缺少 host")
	}
	c := newClient(
		host,
		orDefault(asString(p["scheme"]), "https"),
		asString(p["cookie"]),
		asString(p["csrf"]),
		map[string]string{
			"user-agent": asString(p["user_agent"]),
			"x-jdy-ver":  asString(p["jdy_ver"]),
		},
	)
	c.appID = orDefault(asString(p["app_id"]), DefaultAppID)
	return c, nil
}

// fetchAppIDsQuiet returns just the ids a credential can see; a credential
// that no longer works simply yields nothing.
func fetchAppIDsQuiet(c *Client) []string {
	items := fetchApps(c, c.appID)
	out := make([]string, 0, len(items))
	for _, a := range items {
		out = append(out, a.AppID)
	}
	return out
}

func (s *Server) clientFor(appID, acct string) (*Client, error) {
	base, err := s.ensure()
	if err != nil {
		return nil, err
	}
	// 指定了应用包所属账号且不是当前账号时，直接用那个账号的凭据，
	// 这样「切包」命中的是包真正属于的缓存条目，而不是发一次注定被拒的请求。
	if acct != "" && acct != s.account() {
		if p := s.findProfile(acct); p != nil {
			if c, cerr := s.clientFromProfile(p); cerr == nil {
				c.appID = appID
				return c, nil
			}
		}
	}
	if appID == base.appID {
		return base, nil
	}
	c := newClient(base.host, base.scheme, base.cookie, base.csrf, base.extra)
	c.appID = appID
	return c, nil
}

// report returns the board for one day. acct is the credential the
// application package belongs to (empty means the active one): the disk cache
// is keyed by account+app+day, so passing it makes a package switch hit the
// entry that package actually wrote instead of refetching.
func (s *Server) report(appID, acct string, fresh bool, day string) (*Report, error) {
	if _, err := s.ensure(); err != nil {
		return nil, err
	}
	if day == "" {
		day = nowInShanghai().Format("2006-01-02")
	}
	if acct == "" {
		acct = s.account()
	}
	key := cacheKey{appID: appID, day: day}

	// 磁盘里有这个「账号+应用包+日期」就直接用，一次外部请求都不发；
	// 只有 fresh=1（点刷新）才重新采集并覆盖当天那份。
	if !fresh {
		if rep := s.loadDiskReport(acct, appID, day); rep != nil {
			s.mu.Lock()
			s.reports[key] = &reportEntry{report: rep, at: unixNow()}
			s.mu.Unlock()
			return rep, nil
		}
	}

	s.mu.Lock()
	if hit, ok := s.reports[key]; ok && !fresh && unixNow()-hit.at < reportTTL.Seconds() {
		rep := hit.report
		s.mu.Unlock()
		return rep, nil
	}
	s.mu.Unlock()

	// One build per app+day, so concurrent refreshes do not trample progress.
	lk := s.keylock("report|" + appID + "|" + day)
	lk.Lock()
	defer lk.Unlock()

	s.mu.Lock()
	if hit, ok := s.reports[key]; ok && !fresh && unixNow()-hit.at < reportTTL.Seconds() {
		rep := hit.report
		s.mu.Unlock()
		return rep, nil
	}
	s.mu.Unlock()

	name := s.appNameFromList(appID)
	c, err := s.clientFor(appID, acct)
	if err != nil {
		return nil, err
	}
	rep, err := buildReport(c, appID, name, s.workerCount(), day, s.progress)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.reports[key] = &reportEntry{report: rep, at: unixNow()}
	s.mu.Unlock()
	s.saveDiskReport(acct, appID, rep)
	return rep, nil
}

func (s *Server) trend(appID string, acct string, fresh bool, only []string, endDay string) (*Trend, error) {
	if _, err := s.ensure(); err != nil {
		return nil, err
	}
	if endDay == "" {
		endDay = nowInShanghai().Format("2006-01-02")
	}
	key := cacheKey{appID: appID, day: endDay}

	s.mu.Lock()
	if hit, ok := s.trends[key]; ok && !fresh && unixNow()-hit.at < trendTTL.Seconds() {
		t := hit.trend
		s.mu.Unlock()
		return t, nil
	}
	s.mu.Unlock()

	lk := s.keylock("trend|" + appID + "|" + endDay)
	lk.Lock()
	defer lk.Unlock()

	s.mu.Lock()
	if hit, ok := s.trends[key]; ok && !fresh && unixNow()-hit.at < trendTTL.Seconds() {
		t := hit.trend
		s.mu.Unlock()
		return t, nil
	}
	s.mu.Unlock()

	c, err := s.clientFor(appID, acct)
	if err != nil {
		return nil, err
	}
	t, err := buildTrend(c, 7, only, endDay, appID+"|"+endDay, s.progress)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.trends[key] = &trendEntry{trend: t, at: unixNow()}
	s.mu.Unlock()
	return t, nil
}

// ---------- GET routes ----------

type statusResponse struct {
	Auth     bool     `json:"auth"`
	AppID    string   `json:"appId"`
	Active   *Profile `json:"active"`
	Profiles int      `json:"profiles"`
}

type authResponse struct {
	Auth     bool      `json:"auth"`
	Profiles []Profile `json:"profiles"`
	Active   *Profile  `json:"active"`
}

func activeProfile(profiles []Profile) *Profile {
	for i := range profiles {
		if profiles[i].Active {
			p := profiles[i]
			return &p
		}
	}
	return nil
}

func (s *Server) serveGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch path {
	case "/api/status":
		profs := s.profiles()
		writeJSON(w, http.StatusOK, statusResponse{
			Auth:     s.currentClient() != nil,
			AppID:    s.defaultApp(),
			Active:   activeProfile(profs),
			Profiles: len(profs),
		})
		return
	case "/api/auth":
		profs := s.profiles()
		writeJSON(w, http.StatusOK, authResponse{
			Auth:     s.currentClient() != nil,
			Profiles: profs,
			Active:   activeProfile(profs),
		})
		return
	case "/favicon.ico":
		w.Header().Set("content-type", "image/x-icon")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if path == "/api/cache/days" {
		days := []string{}
		if s.disk != nil {
			acct := r.URL.Query().Get("acct")
			if acct == "" {
				acct = s.account()
			}
			days = s.disk.Days("jdy", acct, r.URL.Query().Get("appId"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"days": days})
		return
	}
	if path == "/api/cache/size" {
		var acct int64
		if s.disk != nil {
			acct = s.disk.Size("jdy", s.account())
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": acct,
			"total":   s.diskTotal(),
			"limit":   diskstore.DefaultMaxBytes,
		})
		return
	}

	if !strings.HasPrefix(path, "/api/") {
		writeText(w, http.StatusNotFound, "not found")
		return
	}

	q := r.URL.Query()
	appID := s.defaultApp()
	if q.Has("appId") {
		appID = q.Get("appId")
	}
	fresh := q.Get("fresh") == "1"

	if s.currentClient() == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "未认证", "needAuth": true})
		return
	}
	if !validAppID(appID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "appId 不合法"})
		return
	}

	day := q.Get("date")
	if day != "" && !validDay(day) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "date 需为 YYYY-MM-DD"})
		return
	}

	if path == "/api/progress" {
		key := appID + "|" + orDefault(day, nowInShanghai().Format("2006-01-02"))
		if acct := q.Get("acct"); acct != "" && acct != s.account() {
			key += "|" + acct
		}
		writeJSON(w, http.StatusOK, s.progress.snapshot(key))
		return
	}

	switch path {
	case "/api/apps":
		apps, err := s.apps()
		if err != nil {
			s.writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"apps": apps, "current": appID})
	case "/api/report":
		s.serveReport(w, appID, day, fresh, q)
	case "/api/trend":
		only := make([]string, 0, 4)
		for _, part := range strings.Split(q.Get("only"), ",") {
			if part != "" {
				only = append(only, part)
			}
		}
		t, err := s.trend(appID, q.Get("acct"), fresh, only, day)
		if err != nil {
			s.writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	default:
		writeText(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) serveReport(w http.ResponseWriter, appID, day string, fresh bool, q url.Values) {
	acct := q.Get("acct")
	rep, err := s.report(appID, acct, fresh, day)
	if err != nil {
		s.writeAPIError(w, err)
		return
	}
	// Copy before attaching the trend: the cache keeps the bare report.
	out := *rep
	key := appID + "|" + out.Date

	if q.Get("trend") != "0" {
		ids := make([]string, 0, len(out.Forms))
		for _, f := range out.Forms {
			if f.Total > 0 {
				ids = append(ids, f.EntryID)
			}
		}
		switch {
		case len(ids) == 0:
			out.Trend = &Trend{Days: []trendDay{}, End: out.Date, GeneratedAt: ""}
		default:
			t, terr := s.trend(appID, acct, fresh, ids, out.Date)
			if terr != nil {
				out.Trend = json.RawMessage("null")
				s.progress.log(key, "趋势统计失败："+terr.Error())
			} else {
				out.Trend = t
			}
		}
	}

	s.progress.set(key, map[string]any{
		"phase": "done", "current": []any{}, "running": 0, "finished": unixNow(),
	})
	s.progress.log(key, fmt.Sprintf("完成：用时 %.1fs", float64(out.DurationMs)/1000))
	writeJSON(w, http.StatusOK, &out)
}

func (s *Server) writeAPIError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotAuthenticated) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "未认证", "needAuth": true})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
}

// ---------- POST routes ----------

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))

	switch r.URL.Path {
	case "/api/auth":
		s.serveAuth(w, raw)
	case "/api/auth/switch":
		s.serveSwitch(w, raw)
	case "/api/auth/delete":
		s.serveDelete(w, raw)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func bodyID(raw []byte) string {
	var v any
	if err := decodeJSON(raw, &v); err != nil {
		return ""
	}
	if m := asMap(v); m != nil {
		return asString(m["id"])
	}
	return ""
}

func (s *Server) serveSwitch(w http.ResponseWriter, raw []byte) {
	id := bodyID(raw)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 id"})
		return
	}
	prof, err := s.switchProfile(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "切换失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": prof})
}

func (s *Server) serveDelete(w http.ResponseWriter, raw []byte) {
	id := bodyID(raw)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 id"})
		return
	}
	defer s.dropDiskCache(id) // 删账号才连缓存一起删
	if err := s.deleteProfile(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"auth":     s.currentClient() != nil,
		"profiles": s.profiles(),
	})
}

// serveAuth accepts either a pasted cURL snippet or explicit cookie/csrf
// fields, verifies them with a read-only call, and stores the result.
func (s *Server) serveAuth(w http.ResponseWriter, raw []byte) {
	text := strings.TrimSpace(string(raw))

	var payload map[string]any
	if strings.HasPrefix(text, "{") {
		var decoded any
		if err := decodeJSON([]byte(text), &decoded); err == nil {
			payload = asMap(decoded)
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	cookie := asString(payload["cookie"])
	csrf := asString(payload["csrf"])
	appID := asString(payload["appId"])
	host := orDefault(asString(payload["host"]), DefaultHost)
	scheme := orDefault(asString(payload["scheme"]), "https")
	var extra map[string]string

	curl := asString(payload["curl"])
	if curl == "" && len(payload) == 0 {
		curl = text
	}
	if curl != "" {
		info := parseCurl(curl)
		cookie = orDefault(cookie, info.Cookie)
		csrf = orDefault(csrf, info.CSRF)
		appID = orDefault(appID, info.AppID)
		host = orDefault(info.Host, host)
		scheme = orDefault(info.Scheme, scheme)
		extra = map[string]string{"user-agent": info.UserAgent, "x-jdy-ver": info.JdyVer}
	}

	if cookie == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "没找到 Cookie：请粘贴完整的 cURL（含 -H 'cookie: ...'）",
		})
		return
	}
	appID = orDefault(appID, s.defaultApp())
	if !validAppID(appID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "appId 不合法，请在 cURL 里带上 /_/admin/app/{appId}/ 路径或手动指定",
		})
		return
	}

	if _, err := s.setAuth(cookie, csrf, host, scheme, appID, extra); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "验证失败：" + err.Error()})
		return
	}

	if name := strings.TrimSpace(asString(payload["name"])); name != "" {
		st := loadStore(s.authPath)
		for _, p := range st.Profiles {
			if asString(p["host"]) == host && asString(p["app_id"]) == appID {
				p["name"] = name
			}
		}
		_ = saveStore(s.authPath, st)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "appId": appID})
}

// ---------- response helpers ----------

func validDay(day string) bool {
	if len(day) != 10 {
		return false
	}
	for i, c := range day {
		if i == 4 || i == 7 {
			if c != '-' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := encodeJSON(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeBody(w, code, body, "application/json; charset=utf-8")
}

func writeText(w http.ResponseWriter, code int, text string) {
	writeBody(w, code, []byte(text), "text/plain; charset=utf-8")
}

func writeBody(w http.ResponseWriter, code int, body []byte, ctype string) {
	w.Header().Set("content-type", ctype)
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}
