package metricdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/jmoiron/sqlx"
	"github.com/openshift/ci-search/prow"
	gcpoption "google.golang.org/api/option"
	"k8s.io/klog"
)

type DB struct {
	path      string
	statusURL url.URL
	db        *sqlx.DB
	maxAge    time.Duration

	recentlyDeleted int64

	lock            sync.Mutex
	jobsByName      map[string]int64
	jobCountsByName map[string]int64
	metricsByName   map[string]int64
}

func New(path string, statusURL url.URL, maxAge time.Duration) (*DB, error) {
	db, err := sqlx.Open("sqlite", fmt.Sprintf("file:%s?_timeout=3000", url.PathEscape(path)))
	if err != nil {
		return nil, fmt.Errorf("unable to open database: %v", err)
	}
	return &DB{
		path:      path,
		statusURL: statusURL,
		maxAge:    maxAge,
		db:        db,
	}, nil
}

func (d *DB) Run() error {
	start := time.Now()
	// TODO: check schema
	if err := CreateSchema(d.db); err != nil {
		return fmt.Errorf("unable to create database schema: %v", err)
	}
	if err := d.refreshJobIdentifiers(); err != nil {
		return fmt.Errorf("unable to load initial job identifiers: %v", err)
	}
	if err := d.refreshMetricIdentifiers(); err != nil {
		return fmt.Errorf("unable to load initial metric identifiers: %v", err)
	}
	if err := d.refreshJobCounts(); err != nil {
		return fmt.Errorf("unable to load job counts: %v", err)
	}
	if d.recentlyDeleted > 10000 {
		if _, err := d.db.Exec("VACUUM"); err != nil {
			klog.Errorf("unable to vacuum database: %v", err)
		} else {
			d.recentlyDeleted = 0
		}
	}
	if _, err := d.db.Exec("PRAGMA OPTIMIZE"); err != nil {
		klog.Errorf("unable to optimize database: %v", err)
	}
	return d.indexFromGCS(start)
}

func (d *DB) NewReadConnection() (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", fmt.Sprintf("file:%s?_timeout=3000&mode=ro", d.path))
	if err != nil {
		return nil, fmt.Errorf("unable to open database: %v", err)
	}
	return db, err
}

func (d *DB) refreshMetricIdentifiers() error {
	metricIds := make(map[string]int64)
	rows, err := d.db.Query("SELECT id, name FROM metric")
	if err != nil {
		return err
	}
	var name string
	var id int64
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		metricIds[name] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}

	d.lock.Lock()
	defer d.lock.Unlock()
	d.metricsByName = metricIds
	return nil
}

func (d *DB) refreshJobIdentifiers() error {
	jobIds := make(map[string]int64)
	rows, err := d.db.Query("SELECT id, name FROM job")
	if err != nil {
		return err
	}
	var name string
	var id int64
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		jobIds[name] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}

	d.lock.Lock()
	defer d.lock.Unlock()
	d.jobsByName = jobIds
	return nil
}

func (d *DB) refreshJobCounts() error {
	jobCounts := make(map[string]int64)
	rows, err := d.db.Query(`
		SELECT name, count(release_job.job_number) as count
		FROM job,release_job
		WHERE release_job.job_id = job.id
		GROUP BY release_job.job_id
	`)
	if err != nil {
		return err
	}
	var name string
	var count int64
	for rows.Next() {
		if err := rows.Scan(&name, &count); err != nil {
			return err
		}
		jobCounts[name] = count
	}
	if err := rows.Err(); err != nil {
		return err
	}

	d.lock.Lock()
	defer d.lock.Unlock()
	d.jobCountsByName = jobCounts
	return nil
}

func (d *DB) MetricsByName() map[string]int64 {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.metricsByName
}

func (d *DB) JobsByName() map[string]int64 {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.jobsByName
}

func (d *DB) JobCountsByName() map[string]int64 {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.jobCountsByName
}

func copyMapStringInt64(m map[string]int64) map[string]int64 {
	copied := make(map[string]int64, len(m))
	for k, v := range m {
		copied[k] = v
	}
	return copied
}

func (d *DB) indexFromGCS(start time.Time) error {
	if d.maxAge == 0 {
		return nil
	}

	metricIds := copyMapStringInt64(d.MetricsByName())
	jobIds := copyMapStringInt64(d.JobsByName())

	// find job exclusions (oldest and newest job timestamp for any given job)
	jobCompletion := make(map[string]Int64Range)
	var jobName string
	var min, max int64
	if err := RowsOf(d.db.Queryx(`
		SELECT job.name, min(timestamp), max(timestamp) 
		FROM job, metric_value, metric 
		WHERE 
			job.id = metric_value.job_id AND metric_id = metric.id AND 
			metric.name == 'job:duration:total:seconds' 
		GROUP BY job.name
	`)).Every([]interface{}{&jobName, &min, &max}, func() {
		jobCompletion[jobName] = Int64Range{Min: min, Max: max}
	}); err != nil {
		return err
	}

	// find our last recorded scrape position
	var lastKey string
	var lastScrapeTimestamp int64
	if err := RowsOf(d.db.Queryx(`
		SELECT last_key, timestamp FROM scrape WHERE name = "job-metrics"
	`)).Every([]interface{}{&lastKey, &lastScrapeTimestamp}, func() {}); err != nil {
		return err
	}

	index := &prow.Index{
		Bucket:    "origin-ci-test",
		IndexName: "job-metrics",
	}

	switch {
	case len(lastKey) > 0:
		index.FromKey = lastKey
		klog.Infof("Resuming metrics scrape from key %q from %s ago", lastKey, start.Sub(time.Unix(lastScrapeTimestamp, 0).Truncate(time.Second)))
	case d.maxAge > 0:
		index.FromTime(start.Add(-d.maxAge))
		klog.Infof("Scraping metrics newer than key %q with retention %s", index.FromKey, d.maxAge.String())
	default:
		klog.Infof("Scraping metrics from start, retaining all metrics")
	}

	if d.maxAge > 0 {
		oldestTimestamp := start.Add(-d.maxAge).Unix()
		res, err := d.db.Exec("DELETE FROM metric_value WHERE metric_value.timestamp < ?", oldestTimestamp)
		if err != nil {
			return fmt.Errorf("unable to delete metrics older than timestamp %d: %v", oldestTimestamp, err)
		}
		if rows, err := res.RowsAffected(); err == nil {
			klog.Infof("Removed %d metrics older than %s", rows, d.maxAge)
			d.recentlyDeleted += rows
		}
	}

	b, err := NewBatchInserter(d.db, 1000)
	if err != nil {
		return err
	}

	gcsClient, err := storage.NewClient(context.Background(), gcpoption.WithoutAuthentication())
	if err != nil {
		return fmt.Errorf("Unable to build gcs client: %v", err)
	}

	var keysScanned int
	var insertedJobs, insertedMetrics, insertedValues, insertedReleases int
	var skippedBeforeDecode, skippedAfterDecode, skippedVersion, skippedSelector int
	versionsByType := make(map[string]string, 10)

	defer func() {
		klog.Infof("Scraped %d metrics in %s", insertedValues, time.Now().Sub(start).Truncate(time.Second/10))
	}()

	b.CompletedKey(index.IndexName, lastKey)

	if err := index.EachJob(context.TODO(), gcsClient, 0, d.statusURL, func(partialJob prow.Job, attr *storage.ObjectAttrs) error {
		keysScanned++
		if keysScanned%1000 == 0 {
			klog.Infof("DEBUG: Scanned %d job-metrics keys", keysScanned)
		}

		jobName, jobNumberString := partialJob.Spec.Job, partialJob.Status.BuildID
		jobNumber, err := strconv.ParseInt(jobNumberString, 10, 64)
		if err != nil {
			klog.Warningf("Ignored job %s with invalid job number %s: %v", jobName, jobNumberString, err)
			b.CompletedKey(index.IndexName, attr.Name)
			return nil
		}

		// use the completed string as a way to avoid reprocessing an already stored value when scanning
		jobRange := jobCompletion[jobName]
		if completedString, ok := attr.Metadata["completed"]; ok {
			if completed, err := strconv.ParseInt(completedString, 10, 64); err == nil {
				if jobRange.Includes(completed) {
					//klog.Infof("Skipping %s/%s because %d <= %d <= %d", jobName, jobID, jobRange.Min, completed, jobRange.Max)
					skippedBeforeDecode++
					b.CompletedKey(index.IndexName, attr.Name)
					return nil
				}
			}
		}

		h := gcsClient.Bucket(index.Bucket).Object(attr.Name)
		r, err := h.NewReader(context.TODO())
		if err != nil {
			return fmt.Errorf("failed to read %s: %v", attr.Name, err)
		}

		metrics := make(map[string]OutputMetric, 40)
		d := json.NewDecoder(r)
		if err := d.Decode(&metrics); err != nil {
			return err
		}

		if value, ok := metrics["job:duration:total:seconds"]; ok && jobRange.Includes(value.Timestamp) {
			//klog.Infof("Skipping %s/%s because %d <= %d <= %d", jobName, jobID, jobRange.Min, value.Timestamp, jobRange.Max)
			skippedAfterDecode++
			b.CompletedKey(index.IndexName, attr.Name)
			return nil
		}

		for k := range versionsByType {
			delete(versionsByType, k)
		}

		jobID, ok := jobIds[jobName]
		if !ok {
			var err error
			jobID, err = b.InsertJob(jobName)
			if err != nil {
				return err
			}
			klog.V(4).Infof("assigned job %s id %d", jobName, jobID)
			insertedJobs++
			jobIds[jobName] = jobID
		}

		for k, v := range metrics {
			name, selector := SplitMetricKey(k)
			if len(name) == 0 {
				continue
			}
			selector, ok := CheckMetricSelector(selector)
			if !ok {
				klog.Infof("warning: Invalid selector %q in %s/%d", k, jobName, jobNumber)
				skippedSelector++
				continue
			}

			id, ok := metricIds[name]
			if !ok {
				id, err := b.InsertMetric(name)
				if err != nil {
					return err
				}
				klog.V(4).Infof("assigned metric %s id %d", name, id)
				insertedMetrics++
				metricIds[name] = id
			}
			if err := b.InsertMetricValue(jobID, jobNumber, id, selector, v.Timestamp, v.Value); err != nil {
				return err
			}
			insertedValues++

			// if the metric identifies a release version, include in the reference table
			switch name {
			case "cluster:version:info:total":
				if value, ok := ValueFromValidSelector(selector, "version"); ok {
					versionsByType[value] = "target"
				}
			case "cluster:version:info:install":
				if value, ok := ValueFromValidSelector(selector, "version"); ok {
					versionsByType[value] = "initial"
				}
			case "cluster:version:current:seconds":
				if value, ok := ValueFromValidSelector(selector, "version"); ok {
					if _, ok := versionsByType[value]; !ok {
						versionsByType[value] = "initial"
					}
				}
			case "cluster:version:updates:seconds":
				if value, ok := ValueFromValidSelector(selector, "version"); ok {
					existing := versionsByType[value]
					if existing != "target" && existing != "initial" {
						versionsByType[value] = "upgrade"
					}
				}
			}
			// klog.Infof("%s %s %s %s", partialJob.Spec.Job, partialJob.Status.BuildID, k, v.Value)
		}

		if len(versionsByType) > 0 {
			for version, versionType := range versionsByType {
				major, minor, micro, stream, t, pre, ok := VersionParts(version)
				if !ok {
					klog.Infof("warning: %s is not a well formed version label", version)
					skippedVersion++
					continue
				}
				var unix int64
				if !t.IsZero() {
					unix = t.Unix()
				}
				if err := b.InsertReleaseJob(major, minor, micro, stream, unix, pre, version, jobID, jobNumber, versionType); err != nil {
					return err
				}
				insertedReleases++
			}
		}

		b.CompletedKey(index.IndexName, attr.Name)
		return nil
	}); err != nil {
		return err
	}

	if err := b.Flush(); err != nil {
		return err
	}

	klog.Infof("Saw keys=%d Inserted jobs=%d metrics=%d releases=%d values=%d, skipped before_decode=%d after_decode=%d bad_version=%d bad_selector=%d",
		keysScanned,
		insertedJobs, insertedMetrics, insertedReleases, insertedValues,
		skippedBeforeDecode, skippedAfterDecode, skippedVersion, skippedSelector,
	)
	return nil
}
