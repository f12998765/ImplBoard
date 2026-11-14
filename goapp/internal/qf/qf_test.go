package qf

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseCurlFromHeadersAndCookie(t *testing.T) {
	curl := "curl 'https://qingflow.com/api/app/x/apply/filter/amount' \\\n" +
		"  -H 'token: tok-header' \\\n" +
		"  -H 'wsid: 690763' \\\n" +
		"  -H 'content-type: application/json' \\\n" +
		"  -b 'prodUid=u1; qfVersion=mongo3; prodtoken=tok-cookie; prodwsId=42; JSESSIONID=sess'"

	got := parseCurl(curl)
	if got.Token != "tok-header" {
		t.Fatalf("token = %q, want header value", got.Token)
	}
	if got.WSID != "690763" {
		t.Fatalf("wsid = %q, want header value", got.WSID)
	}
	if got.QFVersion != "mongo3" {
		t.Fatalf("qfVersion = %q", got.QFVersion)
	}
	if got.CookieExtra != "prodUid=u1; JSESSIONID=sess; prodwsId=42" {
		t.Fatalf("cookieExtra = %q", got.CookieExtra)
	}
}

func TestParseCurlFallsBackToCookie(t *testing.T) {
	got := parseCurl("curl --cookie 'prodtoken=tok-cookie; prodwsId=42' https://qingflow.com/api/tag/apps")
	if got.Token != "tok-cookie" || got.WSID != "42" {
		t.Fatalf("token/wsid = %q/%q, want cookie fallback", got.Token, got.WSID)
	}
	if got.QFVersion != "mongo1" {
		t.Fatalf("qfVersion = %q, want default", got.QFVersion)
	}
	if got.CookieExtra != "prodwsId=42" {
		t.Fatalf("cookieExtra = %q", got.CookieExtra)
	}
}

func TestParseCurlEmpty(t *testing.T) {
	got := parseCurl("")
	if got.Token != "" || got.WSID != "" || got.QFVersion != "mongo1" {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestFlattenNestedAndDeduped(t *testing.T) {
	tags := []any{
		map[string]any{
			"tagId":   "t1",
			"tagName": "基础信息",
			"tagItems": []any{
				map[string]any{"itemType": float64(1), "appKey": "k1", "title": "A"},
				map[string]any{"itemType": float64(1), "appKey": "k1", "title": "A dup"},
				map[string]any{"itemType": float64(2), "title": "folder", "subItems": []any{
					map[string]any{"itemType": float64(1), "appKey": "k2", "relationKey": "B", "beingTrial": true},
					map[string]any{"itemType": float64(1), "appKey": "k3"},
				}},
			},
		},
		map[string]any{"tagId": nil, "tagItems": []any{}},
	}

	groups := flatten(tags)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].TagID != "t1" || groups[0].TagName != "基础信息" {
		t.Fatalf("group[0] = %+v", groups[0])
	}
	got := groups[0].Apps
	if len(got) != 3 {
		t.Fatalf("apps = %+v, want 3 unique", got)
	}
	if got[1].Name != "B" || !got[1].Trial {
		t.Fatalf("nested app = %+v", got[1])
	}
	if got[2].Name != "k3" {
		t.Fatalf("fallback name = %q, want key", got[2].Name)
	}
	if groups[1].TagName != "未分组" {
		t.Fatalf("unnamed tag = %q", groups[1].TagName)
	}
}

func TestAuthFileRoundTripKeepsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qf_auth.json")
	original := `{
  "profiles": [
    {"id": "q1", "name": "锋芒", "token": "tok-123456", "wsid": 690763,
     "qfVersion": "mongo1", "cookieExtra": "", "note": "keep me"}
  ],
  "active": "q1",
  "window": {"w": 1200}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := loadAuth(path)
	prof := cfg.ActiveProfile()
	if prof == nil || prof.WSIDText() != "690763" {
		t.Fatalf("active profile = %+v", prof)
	}

	view := (&Server{}).authView(cfg)
	if view.Active == nil || *view.Active != "q1" {
		t.Fatalf("view active = %v", view.Active)
	}
	if len(view.Profiles) != 1 || view.Profiles[0].WSID != json.Number("690763") {
		t.Fatalf("view profiles = %+v", view.Profiles)
	}

	if err := saveAuth(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keep me", "window", "690763", "锋芒"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("saved auth lost %q:\n%s", want, raw)
		}
	}
	var reread AuthConfig
	if err := unmarshalJSON(raw, &reread); err != nil {
		t.Fatal(err)
	}
	if len(reread.Profiles) != 1 || reread.Profiles[0].WSIDText() != "690763" {
		t.Fatalf("reread = %+v", reread.Profiles)
	}
}

func writeAuth(t *testing.T, dir string, active *string, profiles ...Profile) string {
	t.Helper()
	path := filepath.Join(dir, "qf_auth.json")
	cfg := &AuthConfig{Profiles: profiles, Active: active}
	if err := saveAuth(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func strPtr(s string) *string { return &s }

func TestHandlerWithoutActiveProfile(t *testing.T) {
	path := writeAuth(t, t.TempDir(), nil, Profile{ID: "q1", Name: "one", Token: "tok-123456", WSID: "1"})
	s, err := New(Options{AuthPath: path, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	cases := []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/api/auth", 200, `{"active":null,"profiles":[{"id":"q1","name":"one","wsid":"1"}]}`},
		{"GET", "/api/progress", 200, `{"phase":"idle","done":0,"total":0,"current":"","logs":[],"at":0}`},
		{"GET", "/api/apps", 401, `{"error":"未配置账号"}`},
		{"GET", "/api/report", 401, `{"error":"未配置账号"}`},
		{"GET", "/api/nope", 401, `{"error":"未配置账号"}`},
		{"POST", "/api/nope", 404, `{"error":"not found"}`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status {
			t.Fatalf("%s %s status = %d, want %d", c.method, c.path, rec.Code, c.status)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != c.body {
			t.Fatalf("%s %s body = %s, want %s", c.method, c.path, got, c.body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s %s content-type = %q", c.method, c.path, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("%s %s cache-control = %q", c.method, c.path, cc)
		}
	}
}

func TestAuthPostAddSwitchDelete(t *testing.T) {
	dir := t.TempDir()
	path := writeAuth(t, dir, nil)
	s, err := New(Options{AuthPath: path, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	post := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth", strings.NewReader(body)))
		var out map[string]any
		_ = unmarshalJSON(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := post(`{"op":"add"}`)
	if code != 400 {
		t.Fatalf("add without curl status = %d (%v)", code, out)
	}
	if !strings.Contains(asString(out["error"]), "token") {
		t.Fatalf("error = %v", out["error"])
	}

	code, out = post(`{"op":"add","curl":"curl -H 'token: tok-abcdef' -H 'wsid: 42' https://qingflow.com/api/tag/apps","name":"账号A"}`)
	if code != 200 || out["ok"] != true {
		t.Fatalf("add status = %d, body = %v", code, out)
	}
	activeID := asString(out["active"])
	if !strings.HasPrefix(activeID, "q") {
		t.Fatalf("active id = %q", activeID)
	}

	cfg := loadAuth(path)
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "账号A" ||
		cfg.Profiles[0].QFVersion != "mongo1" || cfg.Profiles[0].WSID != "42" {
		t.Fatalf("stored profile = %+v", cfg.Profiles)
	}

	// Profile ids are "q" + unix seconds: wait so the second add gets its own id.
	time.Sleep(1100 * time.Millisecond)
	code, out = post(`{"op":"add","curl":"curl -b 'prodtoken=t2; prodwsId=7; qfVersion=mongo2' x"}`)
	if code != 200 {
		t.Fatalf("second add status = %d", code)
	}
	second := asString(out["active"])
	cfg = loadAuth(path)
	if len(cfg.Profiles) != 2 || cfg.Profiles[1].Name != "wsid 7" {
		t.Fatalf("second profile = %+v", cfg.Profiles)
	}

	code, out = post(`{"op":"switch","id":"` + activeID + `"}`)
	if code != 200 || asString(out["active"]) != activeID {
		t.Fatalf("switch = %d %v", code, out)
	}

	code, out = post(`{"op":"delete","id":"` + second + `"}`)
	if code != 200 || asString(out["active"]) != activeID {
		t.Fatalf("delete = %d %v", code, out)
	}
	code, out = post(`{"op":"delete","id":"` + activeID + `"}`)
	if code != 200 || out["active"] != nil {
		t.Fatalf("delete last = %d %v", code, out)
	}
	if len(loadAuth(path).Profiles) != 0 {
		t.Fatal("profiles not removed")
	}
}

// fakeUpstream mimics the three read-only Qingflow endpoints.
type fakeUpstream struct {
	mu       sync.Mutex
	hits     map[string]int
	amounts  map[string]map[string]int64 // app key -> day -> count
	sinceVal map[string]int64            // app key -> "records after publish"
	pub      map[string]int64            // app key -> ms
	failTree bool
	server   *httptest.Server
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{
		hits:     map[string]int{},
		amounts:  map[string]map[string]int64{},
		sinceVal: map[string]int64{},
		pub:      map[string]int64{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tag/apps", func(w http.ResponseWriter, r *http.Request) {
		f.record("tree")
		if f.failTree {
			fmt.Fprint(w, `{"code":403,"message":"token 已失效"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"tags":[
			{"tagId":"t1","tagName":"基础信息","tagItems":[
				{"itemType":1,"appKey":"k1","title":"表单A"},
				{"itemType":2,"title":"组","subItems":[
					{"itemType":1,"appKey":"k2","relationKey":"表单B","beingTrial":true}
				]}
			]},
			{"tagId":"t2","tagName":"业务","tagItems":[{"itemType":1,"appKey":"k3","title":"表单C"}]}
		]}}`)
	})
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/app/"), "/")
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		key := parts[0]
		if parts[1] == "publish" {
			f.record("publish " + key)
			ms, ok := f.pub[key]
			if !ok {
				fmt.Fprint(w, `{"code":0,"data":{}}`)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"lastPublishTime":%d,"lastPublishUser":{"userName":"张三"}}}`, ms)
			return
		}
		if parts[1] != "apply" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Queries []struct {
				MinValue string `json:"minValue"`
				MaxValue string `json:"maxValue"`
			} `json:"queries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Queries) == 0 {
			f.record("count total " + key)
			fmt.Fprintf(w, `{"code":0,"data":{"resultAmount":%d}}`, f.dayTotal(key))
			return
		}
		q := body.Queries[0]
		if strings.Contains(q.MinValue, ":") {
			f.record("count since " + key)
			fmt.Fprintf(w, `{"code":0,"data":{"resultAmount":%d}}`, f.sinceVal[key])
			return
		}
		f.record("count day " + key)
		fmt.Fprintf(w, `{"code":0,"data":{"resultAmount":%d}}`, f.amounts[key][q.MinValue])
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeUpstream) record(hit string) {
	f.mu.Lock()
	f.hits[hit]++
	f.mu.Unlock()
}

func (f *fakeUpstream) dayTotal(key string) int64 {
	var sum int64
	for _, v := range f.amounts[key] {
		sum += v
	}
	return sum + 100
}

func (f *fakeUpstream) count(hit string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[hit]
}

func (f *fakeUpstream) totalHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.hits {
		n += v
	}
	return n
}

func newTestServer(t *testing.T, upstream *fakeUpstream) *Server {
	t.Helper()
	dir := t.TempDir()
	path := writeAuth(t, dir, strPtr("q1"), Profile{
		ID: "q1", Name: "测试", Token: "tok-abcdef", WSID: "690763", QFVersion: "mongo1",
	})
	s, err := New(Options{AuthPath: path, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s.baseURL = upstream.server.URL
	s.httpClient = upstream.server.Client()
	return s
}

func TestAppsEndpoint(t *testing.T) {
	f := newFakeUpstream(t)
	s := newTestServer(t, f)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Groups []AppGroup `json:"groups"`
		Apps   int        `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Apps != 3 || len(got.Groups) != 2 {
		t.Fatalf("apps=%d groups=%d", got.Apps, len(got.Groups))
	}
	if got.Groups[0].Apps[0].Name != "表单A" || !got.Groups[0].Apps[1].Trial {
		t.Fatalf("group apps = %+v", got.Groups[0].Apps)
	}
	if f.count("tree") != 1 {
		t.Fatalf("tree hits = %d", f.count("tree"))
	}
}

func TestAppsEndpointUpstreamError(t *testing.T) {
	f := newFakeUpstream(t)
	f.failTree = true
	s := newTestServer(t, f)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] != "token 已失效" {
		t.Fatalf("error = %q", got["error"])
	}
}

func TestReportEndpoint(t *testing.T) {
	f := newFakeUpstream(t)
	const day = "2026-11-13"
	pubAt := time.Date(2026, 11, 13, 10, 30, 0, 0, shanghai)
	f.pub["k1"] = pubAt.UnixMilli()
	f.pub["k2"] = time.Date(2026, 11, 10, 9, 0, 0, 0, shanghai).UnixMilli()
	f.amounts["k1"] = map[string]int64{"2026-11-13": 5, "2026-11-12": 2}
	f.amounts["k2"] = map[string]int64{"2026-11-13": 0}
	f.amounts["k3"] = map[string]int64{"2026-11-13": 7, "2026-11-07": 1}
	f.sinceVal["k1"] = 3

	s := newTestServer(t, f)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/report?day="+day, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var rep Report
	if err := unmarshalJSON(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}

	if rep.Day != day {
		t.Fatalf("day = %q", rep.Day)
	}
	if len(rep.Apps) != 3 {
		t.Fatalf("apps = %d, want 3", len(rep.Apps))
	}

	byKey := map[string]ReportApp{}
	for _, a := range rep.Apps {
		byKey[a.Key] = a
	}
	k1 := byKey["k1"]
	if k1.Total != 107 || k1.Day != 5 || k1.AfterPublish == nil || *k1.AfterPublish != 3 {
		t.Fatalf("k1 = %+v (after=%v)", k1, k1.AfterPublish)
	}
	if !k1.PublishedToday || k1.PublishDay != day || k1.PublishUser == nil || *k1.PublishUser != "张三" {
		t.Fatalf("k1 publish = %+v", k1)
	}
	if k1.Tag != "基础信息" {
		t.Fatalf("k1 tag = %q", k1.Tag)
	}
	k2 := byKey["k2"]
	if k2.AfterPublish != nil || k2.PublishedToday || k2.PublishDay != "2026-11-10" {
		t.Fatalf("k2 = %+v", k2)
	}
	if byKey["k3"].AfterPublish != nil {
		t.Fatalf("k3 afterPublish = %v", *byKey["k3"].AfterPublish)
	}

	if len(rep.Trend) != 7 || rep.Trend[6].Day != day {
		t.Fatalf("trend = %+v", rep.Trend)
	}
	wantTrend := map[string]int64{
		"2026-11-07": 1, "2026-11-08": 0, "2026-11-09": 0,
		"2026-11-10": 0, "2026-11-11": 0, "2026-11-12": 2, "2026-11-13": 12,
	}
	for _, p := range rep.Trend {
		if p.Count != wantTrend[p.Day] {
			t.Fatalf("trend %s = %d, want %d", p.Day, p.Count, wantTrend[p.Day])
		}
	}

	if len(rep.Hours) != 24 || rep.Hours[10].Count != 1 {
		t.Fatalf("hours = %+v", rep.Hours)
	}
	wantSum := ReportSum{Apps: 3, Groups: 2, Total: 107 + 100 + 108, Day: 12, WithData: 2, Published: 1}
	if rep.Sum != wantSum {
		t.Fatalf("sum = %+v, want %+v", rep.Sum, wantSum)
	}
	if rep.GeneratedAt <= 0 {
		t.Fatalf("generatedAt = %d", rep.GeneratedAt)
	}

	// The report is cached for 120s: a second call must not touch upstream.
	hitsBefore := f.totalHits()
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/report?day="+day, nil))
	if f.totalHits() != hitsBefore {
		t.Fatal("second request was not served from cache")
	}

	// fresh=1 rebuilds, and the alias date= selects the same day.
	// Counts are cached independently of the report, so drop them first to make
	// the rebuild observable upstream.
	s.cache.mu.Lock()
	s.cache.counts = map[countKey]countEntry{}
	s.cache.pubs = map[pubKey]pubEntry{}
	s.cache.mu.Unlock()
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/report?date="+day+"&fresh=1", nil))
	if f.totalHits() == hitsBefore {
		t.Fatal("fresh=1 did not rebuild")
	}
	var again Report
	if err := unmarshalJSON(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Day != day || again.GeneratedAt < rep.GeneratedAt {
		t.Fatalf("fresh report day=%q generatedAt=%d", again.Day, again.GeneratedAt)
	}

	prog := s.prog.snapshot()
	if prog.Phase != "done" || prog.Done != 3 || prog.Total != 3 || len(prog.Logs) == 0 || prog.At <= 0 {
		t.Fatalf("progress = %+v", prog)
	}
	if !strings.Contains(prog.Logs[len(prog.Logs)-1].Msg, "完成：3 个应用") {
		t.Fatalf("last log = %+v", prog.Logs[len(prog.Logs)-1])
	}
}

func TestReportInvalidDay(t *testing.T) {
	f := newFakeUpstream(t)
	s := newTestServer(t, f)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/report?day=13/11/2026", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] == "" {
		t.Fatal("missing error message")
	}
}

func TestReportDefaultsToToday(t *testing.T) {
	f := newFakeUpstream(t)
	s := newTestServer(t, f)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/report", nil))
	var rep Report
	if err := unmarshalJSON(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Day != today() {
		t.Fatalf("day = %q, want %q", rep.Day, today())
	}
}

// TestLiveUpstream talks to qingflow.com; skip it with -short.
func TestLiveUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("network test skipped in -short mode")
	}
	authPath := os.Getenv("QF_AUTH")
	if authPath == "" {
		t.Skip("QF_AUTH not set")
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Skipf("auth file %s: %v", authPath, err)
	}
	s, err := New(Options{AuthPath: authPath})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Groups []AppGroup `json:"groups"`
		Apps   int        `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Apps == 0 || len(got.Groups) == 0 {
		t.Fatalf("empty live tree: %d apps / %d groups", got.Apps, len(got.Groups))
	}
}
