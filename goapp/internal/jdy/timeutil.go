package jdy

import (
	"strconv"
	"strings"
	"time"
)

// The board is anchored to Asia/Shanghai: day boundaries, the `since` strings
// sent to Jiandaoyun and every rendered timestamp all use this zone.
var shanghai = loadShanghai()

func loadShanghai() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	// Fallback for hosts with no tzdata installed.
	return time.FixedZone("CST", 8*60*60)
}

func nowInShanghai() time.Time {
	return time.Now().In(shanghai)
}

func unixNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func unixOf(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// isoTime formats like Python's datetime.isoformat(): microseconds only when
// they are non-zero, offset always present.
func isoTime(t time.Time) string {
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05-07:00")
	}
	return t.Format("2006-01-02T15:04:05.000000-07:00")
}

// localize converts a Jiandaoyun updateTime (UTC ISO string) to Shanghai time.
// Python returns None for anything unparsable, and so do we.
func localize(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, ok := parseISOTime(s)
	if !ok {
		return time.Time{}, false
	}
	return t.In(shanghai), true
}

func parseISOTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	// Some payloads use +0800 instead of +08:00.
	if t, err := time.Parse("2006-01-02T15:04:05.999999999-0700", s); err == nil {
		return t, true
	}
	// Naive timestamps: Python's astimezone assumes system local time; the
	// board only ever runs in Shanghai, so parse them there.
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, shanghai); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func humanize(d time.Duration) string {
	secs := int64(d / time.Second)
	switch {
	case secs < 60:
		return "刚刚"
	case secs < 3600:
		return strconv.FormatInt(secs/60, 10) + " 分钟前"
	case secs < 86400:
		return strconv.FormatInt(secs/3600, 10) + " 小时前"
	default:
		return strconv.FormatInt(secs/86400, 10) + " 天前"
	}
}

// dayAfter returns the calendar day after a YYYY-MM-DD string.
func dayAfter(day string) string {
	t, err := time.ParseInLocation("2006-01-02", day, shanghai)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// startOfDay parses a YYYY-MM-DD string at Shanghai midnight.
func startOfDay(day string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", day, shanghai)
}

func midnight(t time.Time) time.Time {
	t = t.In(shanghai)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, shanghai)
}

// tsOf is the Python _ts helper: an ISO timestamp as epoch seconds, 0 when
// unparsable.
func tsOf(v any) float64 {
	if t, ok := localize(v); ok {
		return unixOf(t)
	}
	return 0
}
