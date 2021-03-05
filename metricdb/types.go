package metricdb

import "strings"

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
