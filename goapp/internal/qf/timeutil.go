package qf

import "time"

// Everything user-visible is in Asia/Shanghai. Qingflow itself only deals in
// millisecond timestamps, so the zone lives here and nowhere else.
const (
	dayLayout   = "2006-01-02"
	sinceLayout = "2006-01-02 15:04:05"
	clockLayout = "15:04:05"
)

var shanghai = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}()

// today is the current date in Asia/Shanghai, e.g. "2026-11-13".
func today() string {
	return time.Now().In(shanghai).Format(dayLayout)
}

// dayStart parses a YYYY-MM-DD date at local midnight.
func dayStart(day string) (time.Time, error) {
	return time.ParseInLocation(dayLayout, day, shanghai)
}

// dayOfMillis converts a Qingflow millisecond timestamp to its local date.
func dayOfMillis(ms int64) string {
	return time.UnixMilli(ms).In(shanghai).Format(dayLayout)
}

// hourOfMillis is the local wall-clock hour of a millisecond timestamp.
func hourOfMillis(ms int64) int {
	return time.UnixMilli(ms).In(shanghai).Hour()
}

// sinceOfMillis formats a millisecond timestamp the way the amount query wants
// minValue for "records after publish".
func sinceOfMillis(ms int64) string {
	return time.UnixMilli(ms).In(shanghai).Format(sinceLayout)
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func clockNow() string { return time.Now().In(shanghai).Format(clockLayout) }
