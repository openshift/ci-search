package httpgraph

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/openshift/ci-search/pkg/httpwriter"
	"k8s.io/klog/v2"
	_ "modernc.org/sqlite"
)

type APIJobGraphResponse struct {
	Success bool                              `json:"success"`
	Reason  string                            `json:"reason"`
	Message string                            `json:"message"`
	Labels  []string                          `json:"labels"`
	Data    map[string]APIGraphSeriesNullable `json:"data"`
	Series  []APIGraphSeriesDefinition        `json:"series"`

	MaxValue float64 `json:"maxValue"`
}

type APIGraphSeriesDefinition struct {
	Label  string `json:"label,omitempty"`
	Show   *bool  `json:"show,omitempty"`
	Fill   string `json:"fill,omitempty"`
	Stroke string `json:"stroke,omitempty"`
}

type APIGraphSeriesNullable interface {
	MarshalJSON() ([]byte, error)
}

type APIGraphSeriesValuesNullableFromInt64 []int64

func (s APIGraphSeriesValuesNullableFromInt64) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte(`[]`), nil
	}
	buf := make([]byte, 0, len(s)*16)
	buf = append(buf, []byte(`[`)...)
	for i, v := range s {
		if i > 0 {
			buf = append(buf, []byte(`,`)...)
		}
		if v == 0 {
			buf = append(buf, []byte(`null`)...)
			continue
		}
		buf = strconv.AppendInt(buf, v, 10)
	}
	buf = append(buf, []byte(`]`)...)
	return buf, nil
}

type APIGraphSeriesValuesNullableFromFloat64 []float64

func (s APIGraphSeriesValuesNullableFromFloat64) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte(`[]`), nil
	}
	buf := make([]byte, 0, len(s)*16)
	buf = append(buf, []byte(`[`)...)
	for i, v := range s {
		if i > 0 {
			buf = append(buf, []byte(`,`)...)
		}
		if v == 0 {
			buf = append(buf, []byte(`null`)...)
			continue
		}
		buf = strconv.AppendFloat(buf, v, 'f', -1, 64)
	}
	buf = append(buf, []byte(`]`)...)
	return buf, nil
}

func (s *Server) HandleAPIJobGraph(w http.ResponseWriter, req *http.Request) {
	if s.DB == nil {
		http.Error(w, "Metrics graphing is disabled", http.StatusMethodNotAllowed)
		return
	}

	var graph Graph
	var success bool
	start := time.Now()
	var queryDuration, renderDuration time.Duration
	defer func() {
		klog.Infof("Render API graph %s query=%s render=%s duration=%s success=%t", graph.String(), queryDuration.Truncate(time.Millisecond/10), renderDuration.Truncate(time.Millisecond/10), time.Now().Sub(start).Truncate(time.Millisecond), success)
	}()

	db, err := s.DB.NewReadConnection()
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to connect to database: %v", err), http.StatusInternalServerError)
		return
	}

	queryStart := time.Now()
	result, reason, err := handleAPIJobGraph(req, db)
	if err != nil {
		result = &APIJobGraphResponse{Reason: reason, Message: err.Error()}
	} else {
		result.Success = true
		result.Reason = ""
		result.Message = ""
	}
	renderStart := time.Now()
	queryDuration = renderStart.Sub(queryStart)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer := httpwriter.ForRequest(w, req)
	if !result.Success {
		switch result.Reason {
		case "BadRequest":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
	jw := json.NewEncoder(writer)
	if err := jw.Encode(result); err != nil {
		klog.Errorf("Failed to write response: %v", err)
	}
	if err := writer.Close(); err != nil {
		klog.Errorf("Failed to close response: %v", err)
	}
	renderDuration = time.Now().Sub(renderStart)
	success = true
}

func handleAPIJobGraph(req *http.Request, db *sqlx.DB) (*APIJobGraphResponse, string, error) {
	if err := req.ParseForm(); err != nil {
		return nil, "BadRequest", fmt.Errorf("invalid input, must be GET or POST with url encoded body")
	}

	jobNames := req.Form["job"]
	if len(jobNames) == 0 || (len(jobNames) == 1 && len(jobNames[0]) == 0) {
		return nil, "BadRequest", fmt.Errorf("'job' must be specified as one or more jobs to query")
	}

	metricName := req.FormValue("metric")
	if len(metricName) == 0 {
		return nil, "BadRequest", fmt.Errorf("'metric' must be specified as the name of a metric")
	}

	type seriesKey struct {
		jobId    int64
		selector string
	}

	seriesByJobId := make(map[seriesKey][]float64, 5)
	labels := make([]string, 0, 1024)
	timestamps := make([]int64, 0, 1024)

	var minValue, maxValue float64 = math.MaxFloat64, -math.MaxFloat64
	if err := func() error {
		if len(jobNames) == 0 {
			return nil
		}

		query, args, err := sqlx.In(`
		SELECT r.timestamp, r.version, m.job_id, m.metric_selector, avg(m.value) as value 
		FROM metric_value AS m, release_job AS r, job, metric
		WHERE
			m.metric_id == metric.id AND metric.name == ? AND
			r.job_id = m.job_id AND r.job_id = job.id AND job.name IN (?) AND
			m.job_number = r.job_number AND 
			r.type == 'target' 
		GROUP BY r.job_number, m.metric_selector
		ORDER by r.timestamp, r.version, r.job_id, m.metric_selector;
		`, metricName, jobNames)
		if err != nil {
			return fmt.Errorf("unable to query series: %v", err)
		}
		query = db.Rebind(query)
		klog.Infof("DEBUG: Query: %s\nArgs: %v", query, args)
		rows, err := db.Query(query, args...)
		if err != nil {
			return fmt.Errorf("unable to query series: %v", err)
		}
		var rowCount int64
		var timestamp int64
		var version string
		var jobId int64
		var selector string
		var value float64
		for rows.Next() {
			if err := rows.Scan(&timestamp, &version, &jobId, &selector, &value); err != nil {
				return fmt.Errorf("unable to scan query: %v", err)
			}
			rowCount++

			if value < minValue {
				minValue = value
			}
			if value > maxValue {
				maxValue = value
			}

			key := seriesKey{jobId: jobId, selector: selector}
			series, ok := seriesByJobId[key]
			if !ok {
				series = make([]float64, 0, 1024)
				seriesByJobId[key] = series
			}
			var seriesGrew bool

			last := len(timestamps) - 1
			if last == -1 || timestamps[last] != timestamp {
				timestamps = append(timestamps, timestamp)
				labels = append(labels, version)
				last = last + 1
				seriesGrew = true
			}
			missing := last - (len(series) - 1)
			if missing > 0 {
				series = append(series, make([]float64, missing)...)
				seriesGrew = true
			}
			series[last] = value
			if seriesGrew {
				seriesByJobId[key] = series
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("failed to return results for query: %v", err)
		}
		klog.Infof("DEBUG: read %d rows", rowCount)
		return nil
	}(); err != nil {
		return nil, "", err
	}

	expected := len(timestamps)
	for k, v := range seriesByJobId {
		if missing := expected - len(v); missing > 0 {
			v = append(v, make([]float64, missing)...)
			seriesByJobId[k] = v
		}
	}

	// populate series labels for everything not in cache
	seriesLabelById := make(map[int64]string, len(seriesByJobId))
	if err := func() error {
		var keys []int64
		for k := range seriesByJobId {
			keys = append(keys, k.jobId)
		}
		if len(keys) == 0 {
			return nil
		}
		query, args, err := sqlx.In(`SELECT name, id FROM job WHERE id IN (?);`, keys)
		if err != nil {
			return fmt.Errorf("unable to query series: %v", err)
		}
		query = db.Rebind(query)
		rows, err := db.Query(query, args...)
		var name string
		var id int64
		for rows.Next() {
			if err := rows.Scan(&name, &id); err != nil {
				return fmt.Errorf("unable to scan query: %v", err)
			}
			seriesLabelById[id] = name
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("failed to get job names for query: %v", err)
		}
		return nil
	}(); err != nil {
		return nil, "", err
	}

	var result APIJobGraphResponse

	if minValue >= 0 && maxValue > minValue {
		result.MaxValue = maxValue
	}

	result.Labels = labels

	result.Data = make(map[string]APIGraphSeriesNullable)

	result.Series = append(result.Series, APIGraphSeriesDefinition{
		Label: "",
	})
	result.Data[""] = APIGraphSeriesValuesNullableFromInt64(timestamps)
	for k, v := range seriesByJobId {
		label := seriesLabelById[k.jobId]
		if len(k.selector) > 0 {
			label = fmt.Sprintf("%s{%s}", label, k.selector)
		}
		result.Series = append(result.Series, APIGraphSeriesDefinition{
			Label:  label,
			Stroke: "blue",
		})
		result.Data[label] = APIGraphSeriesValuesNullableFromFloat64(v)
	}
	sort.Slice(result.Series, func(i, j int) bool {
		return result.Series[i].Label < result.Series[j].Label
	})
	return &result, "", nil
}
