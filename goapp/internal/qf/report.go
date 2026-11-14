package qf

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Cache lifetimes, matching the Python server.
const (
	appsTTL    = 600 * time.Second
	reportTTL  = 120 * time.Second
	countTTL   = 300 * time.Second
	hoursInDay = 24
	trendDays  = 7
)

// AppRef is one application in a package tree.
type AppRef struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Trial bool   `json:"trial"`
}

// AppGroup is one application package ("标签") with its applications.
type AppGroup struct {
	TagID   any      `json:"tagId"`
	TagName string   `json:"tagName"`
	Apps    []AppRef `json:"apps"`
}

// ReportApp is one row of the report table.
type ReportApp struct {
	Key            string  `json:"key"`
	Name           string  `json:"name"`
	Tag            string  `json:"tag"`
	Total          int64   `json:"total"`
	Day            int64   `json:"day"`
	AfterPublish   *int64  `json:"afterPublish"`
	Publish        *int64  `json:"publish"`
	PublishDay     string  `json:"publishDay"`
	PublishUser    *string `json:"publishUser"`
	PublishedToday bool    `json:"publishedToday"`
}

type TrendPoint struct {
	Day   string `json:"d"`
	Count int64  `json:"n"`
}

type HourBucket struct {
	Hour  int   `json:"h"`
	Count int64 `json:"n"`
}

type ReportSum struct {
	Apps      int   `json:"apps"`
	Groups    int   `json:"groups"`
	Total     int64 `json:"total"`
	Day       int64 `json:"day"`
	WithData  int   `json:"withData"`
	Published int   `json:"published"`
}

// Report is the payload of GET /api/report.
type Report struct {
	Day         string       `json:"day"`
	Apps        []ReportApp  `json:"apps"`
	Trend       []TrendPoint `json:"trend"`
	Hours       []HourBucket `json:"hours"`
	Sum         ReportSum    `json:"sum"`
	GeneratedAt int64        `json:"generatedAt"`
}

type reportKey struct{ Profile, Day string }
type countKey struct{ Profile, App, Day, Since string }
type pubKey struct{ Profile, App string }

type appsEntry struct {
	at   time.Time
	tags []any
}

type reportEntry struct {
	at  time.Time
	rep *Report
}

type countEntry struct {
	at time.Time
	n  *int64
}

type pubEntry struct {
	at   time.Time
	pub  *int64
	user *string
}

// cache holds the shared upstream responses so a date switch does not re-query
// 116 applications. Everything is in memory, like the Python server.
type cache struct {
	mu       sync.Mutex
	apps     map[string]appsEntry
	reports  map[reportKey]reportEntry
	counts   map[countKey]countEntry
	pubs     map[pubKey]pubEntry
	keylocks map[reportKey]*sync.Mutex
}

func newCache() *cache {
	return &cache{
		apps:     map[string]appsEntry{},
		reports:  map[reportKey]reportEntry{},
		counts:   map[countKey]countEntry{},
		pubs:     map[pubKey]pubEntry{},
		keylocks: map[reportKey]*sync.Mutex{},
	}
}

func (c *cache) appsFor(profile string) ([]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.apps[profile]
	if !ok || time.Since(e.at) > appsTTL {
		return nil, false
	}
	return e.tags, true
}

func (c *cache) storeApps(profile string, tags []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apps[profile] = appsEntry{at: time.Now(), tags: tags}
}

func (c *cache) report(key reportKey) (*Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.reports[key]
	if !ok || time.Since(e.at) > reportTTL {
		return nil, false
	}
	return e.rep, true
}

func (c *cache) storeReport(key reportKey, rep *Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports[key] = reportEntry{at: time.Now(), rep: rep}
}

func (c *cache) clearReports() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = map[reportKey]reportEntry{}
}

func (c *cache) count(key countKey) (*int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.counts[key]
	if !ok || time.Since(e.at) > countTTL {
		return nil, false
	}
	return e.n, true
}

func (c *cache) storeCount(key countKey, n *int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key] = countEntry{at: time.Now(), n: n}
}

func (c *cache) publish(key pubKey) (*int64, *string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.pubs[key]
	if !ok || time.Since(e.at) > countTTL {
		return nil, nil, false
	}
	return e.pub, e.user, true
}

func (c *cache) storePublish(key pubKey, pub *int64, user *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs[key] = pubEntry{at: time.Now(), pub: pub, user: user}
}

// lockFor serialises report builds of the same (profile, day) pair.
func (c *cache) lockFor(key reportKey) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lk, ok := c.keylocks[key]
	if !ok {
		lk = &sync.Mutex{}
		c.keylocks[key] = lk
	}
	return lk
}

// flatten turns the tag tree into packages of applications, dropping duplicate
// app keys inside a package and unnamed packages becoming "未分组".
func flatten(tags []any) []AppGroup {
	groups := make([]AppGroup, 0, len(tags))
	for _, raw := range tags {
		t := asMap(raw)
		apps := []AppRef{}
		seen := map[string]bool{}
		var walk func(items []any)
		walk = func(items []any) {
			for _, item := range items {
				it := asMap(item)
				if kind, ok := asInt64(it["itemType"]); ok && kind == 1 {
					if key := asString(it["appKey"]); key != "" && !seen[key] {
						seen[key] = true
						apps = append(apps, AppRef{
							Key:   key,
							Name:  firstNonEmpty(asString(it["title"]), asString(it["relationKey"]), key),
							Trial: truthy(it["beingTrial"]),
						})
					}
				}
				walk(asSlice(it["subItems"]))
			}
		}
		walk(asSlice(t["tagItems"]))

		name := firstNonEmpty(asString(t["tagName"]), "未分组")
		groups = append(groups, AppGroup{TagID: t["tagId"], TagName: name, Apps: apps})
	}
	return groups
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// cachedCount wraps api.amount with the 5 minute count cache.
func (s *Server) cachedCount(api *qfAPI, profile, appKey, day, since string) *int64 {
	key := countKey{Profile: profile, App: appKey, Day: orDash(day), Since: orDash(since)}
	if n, ok := s.cache.count(key); ok {
		return n
	}
	n := api.amount(appKey, day, since)
	s.cache.storeCount(key, n)
	return n
}

func (s *Server) cachedPublish(api *qfAPI, profile, appKey string) (*int64, *string) {
	key := pubKey{Profile: profile, App: appKey}
	if pub, user, ok := s.cache.publish(key); ok {
		return pub, user
	}
	pub, user := api.publish(appKey)
	s.cache.storePublish(key, pub, user)
	return pub, user
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// buildReport returns the cached report for a day unless fresh is set.
func (s *Server) buildReport(api *qfAPI, day string, fresh bool) (*Report, error) {
	key := reportKey{Profile: profileKey(api.token), Day: day}
	if !fresh {
		if rep, ok := s.cache.report(key); ok {
			return rep, nil
		}
	}

	lk := s.cache.lockFor(key)
	lk.Lock()
	defer lk.Unlock()

	if !fresh {
		if rep, ok := s.cache.report(key); ok {
			return rep, nil
		}
	}
	return s.buildReportLocked(api, day, key)
}

// appStats is what one worker produces for one application.
type appStats struct {
	total       int64
	day         int64
	after       *int64
	publish     *int64
	publishUser *string
}

type reportAppRef struct {
	AppRef
	Tag string
}

func (s *Server) buildReportLocked(api *qfAPI, day string, key reportKey) (*Report, error) {
	s.prog.startCount()
	s.prog.log("读取应用树")

	profile := key.Profile
	tags, ok := s.cache.appsFor(profile)
	if !ok {
		var err error
		tags, err = api.tree()
		if err != nil {
			return nil, err
		}
		s.cache.storeApps(profile, tags)
	}
	groups := flatten(tags)

	apps := make([]reportAppRef, 0)
	seen := map[string]bool{}
	for _, g := range groups {
		for _, a := range g.Apps {
			if seen[a.Key] {
				continue
			}
			seen[a.Key] = true
			apps = append(apps, reportAppRef{AppRef: a, Tag: g.TagName})
		}
	}

	start, err := dayStart(day)
	if err != nil {
		return nil, err
	}
	days := make([]string, 0, trendDays)
	for i := trendDays - 1; i >= 0; i-- {
		days = append(days, start.Add(-time.Duration(i)*24*time.Hour).Format(dayLayout))
	}

	s.prog.setTotal(len(apps))
	s.prog.log(fmt.Sprintf("共 %d 个应用（%d 个应用包），开始统计", len(apps), len(groups)))

	stats := s.collectAppStats(api, profile, day, apps)

	s.prog.startTrend(len(apps))
	s.prog.log(fmt.Sprintf("统计 7 日趋势（%s ~ %s）", days[0], days[len(days)-1]))
	trend := s.collectTrend(api, profile, apps, days)

	outApps := make([]ReportApp, 0, len(apps))
	var hours [hoursInDay]int64
	published := 0
	for i, a := range apps {
		st := stats[i]
		publishDay := ""
		if st.publish != nil && *st.publish != 0 {
			publishDay = dayOfMillis(*st.publish)
			if publishDay == day {
				published++
				if h := hourOfMillis(*st.publish); h >= 0 && h < hoursInDay {
					hours[h]++
				}
			}
		}
		outApps = append(outApps, ReportApp{
			Key:            a.Key,
			Name:           a.Name,
			Tag:            a.Tag,
			Total:          st.total,
			Day:            st.day,
			AfterPublish:   st.after,
			Publish:        st.publish,
			PublishDay:     publishDay,
			PublishUser:    st.publishUser,
			PublishedToday: publishDay == day,
		})
	}

	withData := 0
	var sumTotal, sumDay int64
	for _, a := range outApps {
		sumTotal += a.Total
		sumDay += a.Day
		if a.Day > 0 {
			withData++
		}
	}

	hourBuckets := make([]HourBucket, 0, hoursInDay)
	for h := 0; h < hoursInDay; h++ {
		hourBuckets = append(hourBuckets, HourBucket{Hour: h, Count: hours[h]})
	}

	report := &Report{
		Day:   day,
		Apps:  outApps,
		Trend: trend,
		Hours: hourBuckets,
		Sum: ReportSum{
			Apps:      len(outApps),
			Groups:    len(groups),
			Total:     sumTotal,
			Day:       sumDay,
			WithData:  withData,
			Published: published,
		},
		GeneratedAt: nowMillis(),
	}
	s.cache.storeReport(key, report)

	s.prog.finish(len(apps))
	s.prog.log(fmt.Sprintf("完成：%d 个应用，当日发布 %d 个", len(apps), published))
	return report, nil
}

// collectAppStats runs the per-application queries with a bounded pool, the
// same way Python's ThreadPoolExecutor(WORKERS) does.
func (s *Server) collectAppStats(api *qfAPI, profile, day string, apps []reportAppRef) []appStats {
	stats := make([]appStats, len(apps))
	var done int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.workers)

	for i := range apps {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			a := apps[idx]
			st := appStats{}
			if v := s.cachedCount(api, profile, a.Key, "", ""); v != nil {
				st.total = *v
			}
			if v := s.cachedCount(api, profile, a.Key, day, ""); v != nil {
				st.day = *v
			}
			pub, user := s.cachedPublish(api, profile, a.Key)
			st.publish, st.publishUser = pub, user
			if pub != nil && *pub != 0 && dayOfMillis(*pub) == day {
				zero := int64(0)
				if v := s.cachedCount(api, profile, a.Key, day, sinceOfMillis(*pub)); v != nil {
					st.after = v
				} else {
					st.after = &zero
				}
			}
			stats[idx] = st

			n := atomic.AddInt64(&done, 1)
			total := int64(len(apps))
			if n%5 == 0 || n == total {
				s.prog.setDone(int(n))
			}
			if n%20 == 0 || n == total {
				s.prog.log(fmt.Sprintf("已统计 %d/%d", n, total))
			}
		}(i)
	}
	wg.Wait()
	return stats
}

// collectTrend sums the record count of every application for each of the 7
// days ending at the report day.
func (s *Server) collectTrend(api *qfAPI, profile string, apps []reportAppRef, days []string) []TrendPoint {
	trend := make([]TrendPoint, 0, len(days))
	for i, d := range days {
		var sum int64
		var wg sync.WaitGroup
		sem := make(chan struct{}, s.workers)
		for _, a := range apps {
			wg.Add(1)
			sem <- struct{}{}
			go func(appKey string) {
				defer wg.Done()
				defer func() { <-sem }()
				if v := s.cachedCount(api, profile, appKey, d, ""); v != nil {
					atomic.AddInt64(&sum, *v)
				}
			}(a.Key)
		}
		wg.Wait()

		trend = append(trend, TrendPoint{Day: d, Count: sum})
		s.prog.setDone(i + 1)
		s.prog.log(fmt.Sprintf("%s 新增 %d", d, sum))
	}
	return trend
}
