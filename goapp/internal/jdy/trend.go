package jdy

import (
	"fmt"
	"sync"
	"time"
)

type trendDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// Trend is the /api/trend payload, also embedded in /api/report.
type Trend struct {
	Days        []trendDay `json:"days"`
	End         string     `json:"end"`
	GeneratedAt string     `json:"generatedAt"`
}

// buildTrend computes the last `days` daily additions. Each day is again a
// difference of two cumulative counts, so the loop counts days+1 boundaries
// per form and subtracts neighbours.
func buildTrend(c *Client, days int, only []string, endDay, key string, prog *progressStore) (*Trend, error) {
	if days <= 0 {
		days = 7
	}
	end := midnight(nowInShanghai())
	if endDay != "" {
		parsed, err := startOfDay(endDay)
		if err != nil {
			return nil, fmt.Errorf("date 需为 YYYY-MM-DD")
		}
		end = parsed
	}

	starts := make([]time.Time, days)
	for i := range starts {
		starts[i] = end.AddDate(0, 0, -(days - 1 - i))
	}
	bounds := make([]string, 0, days+1)
	for _, d := range starts {
		bounds = append(bounds, d.Format("2006-01-02")+" 00:00:00")
	}
	bounds = append(bounds, starts[len(starts)-1].AddDate(0, 0, 1).Format("2006-01-02")+" 00:00:00")

	entryMap, err := c.EntryMap()
	if err != nil {
		return nil, err
	}

	entries := make([]formEntry, 0, len(entryMap))
	wanted := make(map[string]struct{}, len(only))
	for _, id := range only {
		wanted[id] = struct{}{}
	}
	for _, id := range sortedKeys(entryMap) {
		m := asMap(entryMap[id])
		if asString(m["type"]) != "form" {
			continue
		}
		entryID := asString(m["entryId"])
		if len(wanted) > 0 {
			if _, ok := wanted[entryID]; !ok {
				continue
			}
		}
		entries = append(entries, formEntry{EntryID: entryID, Name: asString(m["name"])})
	}

	totals := make([]int, len(bounds))
	var mu sync.Mutex
	done := 0
	active := newActiveSet()

	if key != "" {
		prog.set(key, map[string]any{"phase": "trend", "done": 0, "total": len(entries), "current": []any{}})
		prog.log(key, fmt.Sprintf("开始统计近 %d 日新增（%d 张有数据的表单，每张 %d 次计数）",
			days, len(entries), len(bounds)))
	}

	one := func(entry formEntry) {
		name := orDefault(entry.Name, entry.EntryID)
		active.set(name, "趋势")
		if key != "" {
			prog.set(key, map[string]any{"current": active.current(), "running": active.size()})
		}

		values := make([]int, 0, len(bounds))
		failed := false
		for _, bound := range bounds {
			v, err := c.count(entry.EntryID, bound, "")
			if err != nil {
				failed = true
				break
			}
			values = append(values, v)
		}

		mu.Lock()
		if !failed {
			for i, v := range values {
				totals[i] += v
			}
		}
		active.remove(name)
		done++
		n := done
		mu.Unlock()

		if key != "" {
			prog.set(key, map[string]any{
				"done": n, "current": active.current(), "running": active.size(),
			})
			if n%10 == 0 || n == len(entries) {
				prog.log(key, fmt.Sprintf("趋势 %d/%d 张表单", n, len(entries)))
			}
		}
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

	series := make([]trendDay, 0, len(starts))
	for i, d := range starts {
		count := totals[i] - totals[i+1]
		if count < 0 {
			count = 0
		}
		series = append(series, trendDay{Date: d.Format("2006-01-02"), Count: count})
	}

	return &Trend{
		Days:        series,
		End:         starts[len(starts)-1].Format("2006-01-02"),
		GeneratedAt: isoTime(nowInShanghai()),
	}, nil
}
