package metricdb

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

func SplitMetricKey(s string) (string, string) {
	if i := strings.Index(s, "{"); i != -1 {
		return s[:i], s[i:]
	}
	return s, ""
}

func ValueFromValidSelector(selector, label string) (string, bool) {
	equals := strings.Index(selector, "=")
	if equals == -1 {
		return "", false
	}
	if label != selector[:equals] {
		return "", false
	}
	value := selector[equals+1:]
	last := len(value) - 1
	if len(value) < 2 || value[0] != '"' || value[last] != '"' {
		return "", false
	}
	return value[1:last], true
}

func CheckMetricSelector(s string) (string, bool) {
	if len(s) == 0 {
		return "", true
	}
	if len(s) < 6 {
		return "", false
	}
	last := len(s) - 1
	if s[0] != '{' || s[last-1] != '"' || s[last] != '}' {
		return "", false
	}
	equals := strings.Index(s, "=")
	if equals == -1 || equals >= (last-3) || s[equals+1] != '"' {
		return "", false
	}
	return s[1:last], true
}

var reReleaseVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-0\.(ci|nightly|okd)-(\d{4})-(\d{2})-(\d{2})-(\d{6})(-[a-z0-9\.-]+)?|-[a-z0-9\.-]+|)$`)

func VersionParts(version string) (major, minor, micro int, stream string, t time.Time, pre string, ok bool) {
	m := reReleaseVersion.FindStringSubmatch(version)
	if m == nil {
		return 0, 0, 0, "", time.Time{}, "", false
	}
	var err error
	major, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, 0, "", time.Time{}, "", false
	}
	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, 0, "", time.Time{}, "", false
	}
	micro, err = strconv.Atoi(m[3])
	if err != nil {
		return 0, 0, 0, "", time.Time{}, "", false
	}
	if len(m[4]) == 0 {
		// this is an official release
		ok = true
		return
	}
	if len(m[5]) == 0 {
		// this is a prerelease but not a release stream
		pre = m[4]
		ok = true
		return
	}
	stream = m[5]
	year, _ := strconv.Atoi(m[6])
	month, _ := strconv.Atoi(m[7])
	day, _ := strconv.Atoi(m[8])
	hour, _ := strconv.Atoi(m[9][:2])
	minute, _ := strconv.Atoi(m[9][2:4])
	second, _ := strconv.Atoi(m[9][4:6])
	t = time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	pre = m[10]
	ok = true
	return
}

type Int64Range struct {
	Min int64
	Max int64
}

func (r Int64Range) Includes(v int64) bool {
	if r.Min == 0 || r.Max == 0 {
		return false
	}
	return r.Min <= v && v <= r.Max
}

type OutputMetric struct {
	Timestamp int64  `json:"timestamp"`
	Value     string `json:"value"`
}
