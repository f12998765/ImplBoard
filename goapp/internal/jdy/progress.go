package jdy

import "sync"

// progressStore keeps the per-application scan progress that /api/progress
// serves while /api/report is still running. Everything is in memory: progress
// is a live view, not something worth persisting.
type progressStore struct {
	mu   sync.Mutex
	data map[string]map[string]any
}

func newProgressStore() *progressStore {
	return &progressStore{data: make(map[string]map[string]any)}
}

func (p *progressStore) entry(key string) map[string]any {
	cur := p.data[key]
	if cur == nil {
		cur = map[string]any{}
		p.data[key] = cur
	}
	return cur
}

// set merges fields into the progress record and stamps the activity time,
// which the page uses to detect a stalled request.
func (p *progressStore) set(key string, fields map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.entry(key)
	for k, v := range fields {
		cur[k] = v
	}
	cur["at"] = unixNow()
}

// log appends one line to the progress console, keeping the last 40.
func (p *progressStore) log(key, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.entry(key)
	logs, _ := cur["logs"].([]any)
	logs = append(logs, map[string]any{"t": nowInShanghai().Format("15:04:05"), "m": line})
	if len(logs) > 40 {
		logs = logs[len(logs)-40:]
	}
	cur["logs"] = logs
	cur["at"] = unixNow()
}

// snapshot returns a shallow copy so encoding cannot race with a writer.
func (p *progressStore) snapshot(key string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.data[key]
	if cur == nil {
		return map[string]any{"phase": "idle", "done": 0, "total": 0}
	}
	out := make(map[string]any, len(cur))
	for k, v := range cur {
		out[k] = v
	}
	return out
}
