package qf

import "sync"

// maxLogLines keeps the progress panel readable; the oldest lines are dropped.
const maxLogLines = 40

type logLine struct {
	Time string `json:"t"`
	Msg  string `json:"m"`
}

// progressSnapshot is exactly what GET /api/progress returns.
type progressSnapshot struct {
	Phase   string    `json:"phase"`
	Done    int       `json:"done"`
	Total   int       `json:"total"`
	Current string    `json:"current"`
	Logs    []logLine `json:"logs"`
	At      float64   `json:"at"`
}

// progress is the shared build state the browser polls every 600ms while a
// report is being assembled.
type progress struct {
	mu sync.Mutex

	phase   string
	done    int
	total   int
	current string
	logs    []logLine
	at      float64
}

func newProgress() *progress {
	return &progress{phase: "idle", logs: []logLine{}}
}

func (p *progress) snapshot() progressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	logs := make([]logLine, len(p.logs))
	copy(logs, p.logs)
	return progressSnapshot{
		Phase:   p.phase,
		Done:    p.done,
		Total:   p.total,
		Current: p.current,
		Logs:    logs,
		At:      p.at,
	}
}

func (p *progress) update(fn func(*progress)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
	p.at = nowSeconds()
}

// startCount resets the state for a new report build.
func (p *progress) startCount() {
	p.update(func(s *progress) {
		s.phase = "count"
		s.done = 0
		s.total = 0
		s.current = ""
		s.logs = []logLine{}
	})
}

func (p *progress) setTotal(total int) {
	p.update(func(s *progress) { s.total = total })
}

func (p *progress) setDone(done int) {
	p.update(func(s *progress) { s.done = done })
}

func (p *progress) startTrend(total int) {
	p.update(func(s *progress) {
		s.phase = "trend"
		s.done = 0
		s.total = total
		s.current = "7 日趋势"
	})
}

func (p *progress) finish(done int) {
	p.update(func(s *progress) {
		s.phase = "done"
		s.done = done
		s.current = ""
	})
}

func (p *progress) log(msg string) {
	p.update(func(s *progress) {
		s.logs = append(s.logs, logLine{Time: clockNow(), Msg: msg})
		if len(s.logs) > maxLogLines {
			s.logs = s.logs[len(s.logs)-maxLogLines:]
		}
	})
}
