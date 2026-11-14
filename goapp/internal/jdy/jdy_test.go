package jdy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCurl(t *testing.T) {
	snippet := `curl 'https://www.jiandaoyun.com/_/admin/app/6ad4b72ea6fb06ac37fe79e8/form/abc/' \
  -H 'accept: application/json, text/plain, */*' \
  -H 'cookie: a=1; b=2' \
  -H 'x-csrf-token: tok123' \
  -H 'user-agent: curl/8' \
  -H 'x-jdy-ver: 9.9'`

	info := parseCurl(snippet)
	if info.Host != "www.jiandaoyun.com" {
		t.Errorf("host = %q", info.Host)
	}
	if info.Scheme != "https" {
		t.Errorf("scheme = %q", info.Scheme)
	}
	if info.Cookie != "a=1; b=2" {
		t.Errorf("cookie = %q", info.Cookie)
	}
	if info.CSRF != "tok123" {
		t.Errorf("csrf = %q", info.CSRF)
	}
	if info.AppID != "6ad4b72ea6fb06ac37fe79e8" {
		t.Errorf("appId = %q", info.AppID)
	}
	if info.UserAgent != "curl/8" || info.JdyVer != "9.9" {
		t.Errorf("headers = %q %q", info.UserAgent, info.JdyVer)
	}
}

func TestParseCurlFallsBackToBodyAppID(t *testing.T) {
	info := parseCurl(`curl 'https://www.jiandaoyun.com/api/x' -H 'cookie: c=1' --data-raw '{"appId":"6ab0c3281a10a54713c6abf5"}'`)
	if info.AppID != "6ab0c3281a10a54713c6abf5" {
		t.Fatalf("appId = %q", info.AppID)
	}
	if info.Cookie != "c=1" {
		t.Fatalf("cookie = %q", info.Cookie)
	}
}

func TestParseCurlDefaults(t *testing.T) {
	info := parseCurl("curl x")
	if info.Host != DefaultHost || info.Scheme != "https" {
		t.Fatalf("defaults = %q %q", info.Host, info.Scheme)
	}
	if info.AppID != "" {
		t.Fatalf("appId = %q", info.AppID)
	}
}

func TestLocalizeUpdateTime(t *testing.T) {
	at, ok := localize("2026-11-13T02:31:11.000Z")
	if !ok {
		t.Fatal("expected parse")
	}
	if got := at.Format("2006-01-02 15:04:05"); got != "2026-11-13 10:31:11" {
		t.Fatalf("local time = %s", got)
	}
	if _, ok := localize(nil); ok {
		t.Fatal("nil should not parse")
	}
	if _, ok := localize("not a date"); ok {
		t.Fatal("garbage should not parse")
	}
	if _, ok := localize(12345); ok {
		t.Fatal("numbers should not parse, matching the Python version")
	}
}

func TestHumanize(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second:   "刚刚",
		5 * time.Minute:    "5 分钟前",
		3 * time.Hour:      "3 小时前",
		49 * time.Hour:     "2 天前",
	}
	for d, want := range cases {
		if got := humanize(d); got != want {
			t.Errorf("humanize(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestValidDay(t *testing.T) {
	good := []string{"2026-11-13", "0000-00-00"}
	for _, d := range good {
		if !validDay(d) {
			t.Errorf("%q should be valid", d)
		}
	}
	bad := []string{"2026-1-3", "2026/11/13", "", "2026-11-13T00", "abcd-ef-gh"}
	for _, d := range bad {
		if validDay(d) {
			t.Errorf("%q should be invalid", d)
		}
	}
}

func TestFlattenTree(t *testing.T) {
	tree := map[string]any{
		"entryMap": map[string]any{
			"g1":    map[string]any{"type": "group", "name": "人事"},
			"g2":    map[string]any{"type": "group", "name": "财务"},
			"f1":    map[string]any{"type": "form", "name": "入职", "hasFlow": true},
			"f2":    map[string]any{"type": "form", "name": "报销"},
			"other": map[string]any{"type": "form", "name": "游离"},
		},
		"menu": []any{
			map[string]any{"id": "g1", "child": []any{
				map[string]any{"id": "f1"},
			}},
			map[string]any{"id": "g2", "child": []any{
				map[string]any{"id": "f2"},
			}},
			map[string]any{"id": "other"},
		},
	}
	forms, groups := flattenTree(tree)
	if groups != 2 {
		t.Fatalf("groups = %d", groups)
	}
	if len(forms) != 3 {
		t.Fatalf("forms = %v", forms)
	}
	if forms[0].Group != "人事" || !forms[0].HasFlow || forms[0].Name != "入职" {
		t.Errorf("first form = %+v", forms[0])
	}
	if forms[2].Group != "" {
		t.Errorf("top-level form group = %q", forms[2].Group)
	}
}

func TestFlattenTreeFallsBackToParents(t *testing.T) {
	tree := map[string]any{
		"entryMap": map[string]any{
			"g1": map[string]any{"type": "group", "entryId": "g1", "name": "人事"},
			"f1": map[string]any{"type": "form", "entryId": "f1", "name": "入职", "parent": "g1"},
		},
	}
	forms, groups := flattenTree(tree)
	if groups != 1 {
		t.Fatalf("groups = %d", groups)
	}
	if len(forms) != 1 || forms[0].Group != "人事" || forms[0].EntryID != "f1" {
		t.Fatalf("forms = %+v", forms)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jdy_auth.json")

	st := store{Active: "p1", Profiles: []map[string]any{
		{"id": "p1", "name": "主账号", "host": "www.jiandaoyun.com", "app_id": "6ab0c3281a10a54713c6abf5",
			"cookie": "secret", "csrf": "csrf", "createdAt": json.Number("1794587814")},
	}}
	if err := saveStore(path, st); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}

	loaded := loadStore(path)
	if loaded.Active != "p1" || len(loaded.Profiles) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if asString(loaded.Profiles[0]["cookie"]) != "secret" {
		t.Error("cookie lost")
	}

	pub := publicProfile(loaded.Profiles[0], "p1")
	if pub.Name != "主账号" || !pub.Active || pub.Env != "简道云" || pub.UpdatedAt != 1794587814 {
		t.Errorf("public profile = %+v", pub)
	}
}

func TestLoadStoreMigratesLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	legacy := `{"host":"www.jiandaoyun.com","cookie":"c","csrf":"x","app_id":"6ab0c3281a10a54713c6abf5"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st := loadStore(path)
	if st.Active != "p1" || len(st.Profiles) != 1 {
		t.Fatalf("store = %+v", st)
	}
	if asString(st.Profiles[0]["cookie"]) != "c" {
		t.Fatal("cookie lost during migration")
	}
}

func TestLoadStoreMissingFile(t *testing.T) {
	st := loadStore(filepath.Join(t.TempDir(), "nothing.json"))
	if len(st.Profiles) != 0 || st.Active != "" {
		t.Fatalf("store = %+v", st)
	}
}

// newTestServer builds a server without any usable credential; no request
// touches the network.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		AuthPath: filepath.Join(t.TempDir(), "missing.json"),
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHandlerUnauthenticated(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	cases := []struct {
		method, target string
		code           int
		body           string
	}{
		{"GET", "/api/status", 200, `"auth":false`},
		{"GET", "/api/auth", 200, `"profiles":[]`},
		{"GET", "/api/apps", 401, `"needAuth":true`},
		{"GET", "/api/report", 401, `"needAuth":true`},
		{"GET", "/api/trend", 401, `"needAuth":true`},
		{"GET", "/api/progress", 401, `"needAuth":true`},
		{"GET", "/not-api", 404, "not found"},
		{"GET", "/api/unknown", 404, "not found"},
		{"POST", "/api/auth/switch", 400, `"缺少 id"`},
		{"POST", "/api/auth/delete", 400, `"缺少 id"`},
		{"POST", "/api/unknown", 404, `"not found"`},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("%s %s: code = %d, want %d (body %s)", c.method, c.target, rec.Code, c.code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: body = %s, want to contain %s", c.method, c.target, rec.Body, c.body)
		}
		if got := rec.Header().Get("cache-control"); got != "no-store" {
			t.Errorf("%s %s: cache-control = %q", c.method, c.target, got)
		}
	}
}

func TestFaviconIs204(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("content-type"); ct != "image/x-icon" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestProgressIdleShape(t *testing.T) {
	p := newProgressStore()
	got := p.snapshot("a|2026-11-13")
	if got["phase"] != "idle" || got["done"] != 0 || got["total"] != 0 {
		t.Fatalf("idle = %+v", got)
	}

	p.log("a|2026-11-13", "hello")
	for i := 0; i < 60; i++ {
		p.log("a|2026-11-13", "line")
	}
	logs, _ := p.snapshot("a|2026-11-13")["logs"].([]any)
	if len(logs) != 40 {
		t.Fatalf("log ring = %d", len(logs))
	}
}

func TestReportJSONShape(t *testing.T) {
	// A hand-built report verifies that nullable fields keep their JSON shape
	// without needing the network.
	rep := Report{
		Date:     "2026-11-13",
		Updated:  []FormRow{},
		WithData: []FormRow{},
		Forms:    []FormRow{},
		Groups:   []groupRow{},
		Errors:   []rowError{},
	}
	raw, err := encodeJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	want := `"errors":[]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("body = %s", raw)
	}
	if strings.Contains(string(raw), `"trend"`) {
		t.Fatalf("trend key should be absent: %s", raw)
	}
}

// TestLiveReport talks to Jiandaoyun and is skipped with -short or when no
// credential file is configured.
func TestLiveReport(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	authPath := os.Getenv("JDY_AUTH")
	if authPath == "" {
		t.Skip("set JDY_AUTH to run the live test")
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Skip("credential file not found")
	}
	s, err := New(Options{AuthPath: authPath, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if s.currentClient() == nil {
		t.Skip("no active credential")
	}
	rep, err := s.report(s.defaultApp(), "", false, "2026-11-13")
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalForms == 0 {
		t.Fatal("no forms returned")
	}
	if rep.AppID != s.defaultApp() {
		t.Fatalf("appId = %q", rep.AppID)
	}
}
