package jdy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

// formEntry is one form as discovered in the application tree.
type formEntry struct {
	EntryID string
	Name    string
	Group   string
	HasFlow bool
}

// FormRow is one line of the report tables. The page reads every field here.
type FormRow struct {
	EntryID     string          `json:"entryId"`
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	HasFlow     bool            `json:"hasFlow"`
	UpdateTime  any             `json:"updateTime"`
	Items       int             `json:"items"`
	Total       int             `json:"total"`
	Count       int             `json:"count"`
	UpdateLocal string          `json:"updateLocal"`
	UpdateAgo   string          `json:"updateAgo"`
	UpdatedOn   bool            `json:"updatedOn"`
	AfterUpdate json.RawMessage `json:"afterUpdate,omitempty"`
}

type rowError struct {
	Name    string `json:"name"`
	EntryID string `json:"entryId"`
	Error   string `json:"error"`
}

type groupRow struct {
	Name    string `json:"name"`
	Forms   int    `json:"forms"`
	Updated int    `json:"updated"`
	Added   int    `json:"added"`
	Records int    `json:"records"`
}

// Report is the /api/report payload. Trend is filled by the handler: nil omits
// the key (trend=0), a raw null keeps it as null when the trend query failed.
type Report struct {
	GeneratedAt   string    `json:"generatedAt"`
	Date          string    `json:"date"`
	IsToday       bool      `json:"isToday"`
	AppID         string    `json:"appId"`
	AppName       string    `json:"appName"`
	Host          string    `json:"host"`
	TotalForms    int       `json:"totalForms"`
	UpdatedCount  int       `json:"updatedCount"`
	DataFormCount int       `json:"dataFormCount"`
	RecordCount   int       `json:"recordCount"`
	TotalRecords  int       `json:"totalRecords"`
	GroupTotal    int       `json:"groupTotal"`
	DurationMs    int       `json:"durationMs"`
	Updated       []FormRow `json:"updated"`
	WithData      []FormRow `json:"withData"`
	Forms         []FormRow `json:"forms"`
	Groups        []groupRow `json:"groups"`
	Errors        []rowError `json:"errors"`
	Trend         any       `json:"trend,omitempty"`
}

// activeSet is the ordered "currently reading" list shown in the progress bar.
// Python relied on dict insertion order; a slice keeps that behaviour.
type activeSet struct {
	mu    sync.Mutex
	order []string
	state map[string]string
}

func newActiveSet() *activeSet {
	return &activeSet{state: make(map[string]string)}
}

func (a *activeSet) set(name, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.state[name]; !ok {
		a.order = append(a.order, name)
	}
	a.state[name] = state
}

func (a *activeSet) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.state[name]; !ok {
		return
	}
	delete(a.state, name)
	for i, n := range a.order {
		if n == name {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
}

// current renders up to eight "name · phase" labels.
func (a *activeSet) current() []any {
	a.mu.Lock()
	defer a.mu.Unlock()
	limit := len(a.order)
	if limit > 8 {
		limit = 8
	}
	out := make([]any, 0, limit)
	for _, n := range a.order[:limit] {
		out = append(out, n+" · "+a.state[n])
	}
	return out
}

func (a *activeSet) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.order)
}

// dayAdded counts records created on `day`. The count endpoint ignores `lt`,
// so a past day is the difference between two `gte` counts.
func dayAdded(c *Client, entryID, since, nextSince string, isToday bool) (int, error) {
	first, err := c.count(entryID, since, "")
	if err != nil {
		return 0, err
	}
	if first <= 0 || isToday {
		return first, nil
	}
	next, err := c.count(entryID, nextSince, "")
	if err != nil {
		return 0, err
	}
	if first-next < 0 {
		return 0, nil
	}
	return first - next, nil
}

// buildReport queries every form of an application for one day. Work is spread
// across `workers` goroutines, progress is published as it goes, and a broken
// form only lands in `errors` — it never aborts the report.
func buildReport(c *Client, appID, appName string, workers int, day string, prog *progressStore) (*Report, error) {
	if appName == "" {
		appName = resolveAppName(c, appID)
	}
	now := nowInShanghai()
	today := now.Format("2006-01-02")
	if day == "" {
		day = today
	}
	isToday := day == today
	since := day + " 00:00:00"
	nextSince := dayAfter(day) + " 00:00:00"
	key := appID + "|" + day

	started := time.Now()
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	entries, groupTotal := flattenTree(tree)

	var (
		mu     sync.Mutex
		rows   = make([]FormRow, 0, len(entries))
		errs   = make([]rowError, 0, len(entries))
		doneN  int
		doneOK int
	)
	active := newActiveSet()

	prog.set(key, map[string]any{
		"phase": "forms", "done": 0, "total": len(entries), "ok": 0, "logs": []any{},
		"started": unixOf(started), "finished": 0, "error": "",
		"appName": appName, "date": day,
	})
	prog.log(key, fmt.Sprintf("应用「%s」%s：%d 张表单，并发 %d",
		orDefault(appName, appID), day, len(entries), workers))

	publish := func() {
		prog.set(key, map[string]any{"current": active.current(), "running": active.size()})
	}

	fail := func(entry formEntry, err error) {
		mu.Lock()
		errs = append(errs, rowError{
			Name:    entry.Name,
			EntryID: entry.EntryID,
			Error:   truncateRunes(err.Error(), 160),
		})
		mu.Unlock()
	}

	one := func(entry formEntry) {
		name := orDefault(entry.Name, entry.EntryID)
		stage := func(phase string) {
			active.set(name, phase)
			publish()
		}
		defer func() {
			mu.Lock()
			doneN++
			n, ok := doneN, doneOK
			mu.Unlock()
			active.remove(name)
			prog.set(key, map[string]any{
				"done": n, "ok": ok, "total": len(entries),
				"current": active.current(), "running": active.size(),
			})
			if n%10 == 0 || n == len(entries) {
				prog.log(key, fmt.Sprintf("表单 %d/%d", n, len(entries)))
			}
		}()

		stage("结构")
		detail, err := c.formDetail(entry.EntryID)
		if err != nil {
			fail(entry, err)
			return
		}
		stage("总数")
		total, err := c.count(entry.EntryID, "", "")
		if err != nil {
			fail(entry, err)
			return
		}
		stage("新增")
		added, err := dayAdded(c, entry.EntryID, since, nextSince, isToday)
		if err != nil {
			fail(entry, err)
			return
		}

		row := FormRow{
			EntryID:    entry.EntryID,
			Name:       orDefault(asString(detail["name"]), entry.Name),
			Group:      entry.Group,
			HasFlow:    entry.HasFlow,
			UpdateTime: detail["updateTime"],
			Items:      contentItems(detail),
			Total:      total,
			Count:      added,
		}

		// How many records arrived between the form's last edit and the end of
		// the day — for past days again as a difference of two cumulative counts.
		if dt, ok := localize(row.UpdateTime); ok && dt.Format("2006-01-02") == day {
			v, err := c.count(entry.EntryID, dt.Format("2006-01-02 15:04:05"), "")
			if err == nil && !isToday {
				next, nextErr := c.count(entry.EntryID, nextSince, "")
				if nextErr != nil {
					err = nextErr
				} else {
					v -= next
					if v < 0 {
						v = 0
					}
				}
			}
			if err != nil {
				row.AfterUpdate = json.RawMessage("null")
			} else {
				row.AfterUpdate = json.RawMessage(strconv.Itoa(v))
			}
		}

		mu.Lock()
		rows = append(rows, row)
		doneOK++
		mu.Unlock()
	}

	maxWorkers := workers
	if maxWorkers < 2 {
		maxWorkers = 2
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)
	for _, entry := range entries {
		entry := entry
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			one(entry)
		}()
	}
	wg.Wait()

	prog.set(key, map[string]any{
		"phase": "done", "done": doneN, "ok": doneOK, "total": len(entries),
		"finished": unixNow(), "current": []any{}, "running": 0,
	})
	prog.log(key, fmt.Sprintf("表单读取完成：%d 张，成功 %d，失败 %d", doneN, doneOK, len(errs)))

	for i := range rows {
		dt, ok := localize(rows[i].UpdateTime)
		if !ok {
			continue
		}
		rows[i].UpdateLocal = dt.Format("15:04:05")
		rows[i].UpdateAgo = humanize(now.Sub(dt))
		rows[i].UpdatedOn = dt.Format("2006-01-02") == day
	}

	updated := make([]FormRow, 0, len(rows))
	withData := make([]FormRow, 0, len(rows))
	for _, r := range rows {
		if r.UpdatedOn {
			updated = append(updated, r)
		}
		if r.Count > 0 {
			withData = append(withData, r)
		}
	}
	sort.SliceStable(updated, func(i, j int) bool {
		return asString(updated[i].UpdateTime) > asString(updated[j].UpdateTime)
	})
	sort.SliceStable(withData, func(i, j int) bool {
		return withData[i].Count > withData[j].Count
	})

	groupTotals := make(map[string]*groupRow, 8)
	for _, r := range rows {
		g := orDefault(r.Group, "未分组")
		item := groupTotals[g]
		if item == nil {
			item = &groupRow{Name: g}
			groupTotals[g] = item
		}
		item.Forms++
		if r.UpdatedOn {
			item.Updated++
		}
		if r.Count != 0 {
			item.Added++
		}
		item.Records += r.Count
	}

	forms := make([]FormRow, len(rows))
	copy(forms, rows)
	sort.SliceStable(forms, func(i, j int) bool {
		gi, gj := orDefault(forms[i].Group, "未分组"), orDefault(forms[j].Group, "未分组")
		if gi != gj {
			return gi < gj
		}
		return tsOf(forms[i].UpdateTime) > tsOf(forms[j].UpdateTime)
	})

	groups := make([]groupRow, 0, len(groupTotals))
	for _, item := range groupTotals {
		groups = append(groups, *item)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	recordCount := 0
	for _, r := range withData {
		recordCount += r.Count
	}
	totalRecords := 0
	for _, r := range rows {
		totalRecords += r.Total
	}

	return &Report{
		GeneratedAt:   isoTime(nowInShanghai()),
		Date:          day,
		IsToday:       isToday,
		AppID:         appID,
		AppName:       appName,
		Host:          c.host,
		TotalForms:    len(entries),
		UpdatedCount:  len(updated),
		DataFormCount: len(withData),
		RecordCount:   recordCount,
		TotalRecords:  totalRecords,
		GroupTotal:    groupTotal,
		DurationMs:    int(time.Since(started) / time.Millisecond),
		Updated:       updated,
		WithData:      withData,
		Forms:         forms,
		Groups:        groups,
		Errors:        errs,
	}, nil
}

// contentItems is the number of form fields, `len(entry.content.items)`.
func contentItems(detail map[string]any) int {
	if content := asMap(detail["content"]); content != nil {
		return len(asList(content["items"]))
	}
	return 0
}
