package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"

	"os"
	"os/signal"
	"strings"
	"syscall"

	"cloud.google.com/go/storage"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/profile"
	gcpoption "google.golang.org/api/option"
	_ "modernc.org/sqlite"

	"github.com/openshift/ci-search/metricdb"
	"github.com/openshift/ci-search/prow"
)

func main() {
	defer Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()

	db, err := sqlx.Open("sqlite", "file:search.db")
	if err != nil {
		log.Fatal(err)
	}

	if err := metricdb.CreateSchema(db); err != nil {
		log.Fatal(err)
	}

	metricIds := make(map[string]int64)
	rows, err := db.Query("SELECT id, name FROM metric")
	if err != nil {
		log.Fatal(err)
	}
	var name string
	var id int64
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		metricIds[name] = id
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	jobIds := make(map[string]int64)
	rows, err = db.Query("SELECT id, name FROM job")
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		jobIds[name] = id
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	jobCompletion := make(map[string]metricdb.Int64Range)
	rows, err = db.Query("SELECT job.name, min(timestamp), max(timestamp) FROM job, metric_value, metric WHERE job.id = metric_value.job_id AND metric_id = metric.id AND metric.name == 'job:duration:total:seconds' GROUP BY job.name")
	if err != nil {
		log.Fatalf("completion query: %v", err)
	}
	var jobName string
	var min, max int64
	for rows.Next() {
		if err := rows.Scan(&jobName, &min, &max); err != nil {
			log.Fatal(err)
		}
		jobCompletion[jobName] = metricdb.Int64Range{Min: min, Max: max}
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	insertJob, err := db.Preparex("INSERT INTO job (name) VALUES(?)")
	if err != nil {
		log.Fatal(err)
	}

	insertMetric, err := db.Preparex("INSERT INTO metric (name) VALUES(?)")
	if err != nil {
		log.Fatal(err)
	}

	insertMetricValue, err := db.Preparex("INSERT INTO metric_value (job_id, job_number, metric_id, metric_selector, timestamp, value) VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING")
	if err != nil {
		log.Fatal(err)
	}

	insertReleaseJob, err := db.Preparex("INSERT INTO release_job (major, minor, micro, timestamp, stream, pre, version, job_id, job_number, type) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING")
	if err != nil {
		log.Fatal(err)
	}

	gcsClient, err := storage.NewClient(context.Background(), gcpoption.WithoutAuthentication())
	if err != nil {
		log.Fatalf("Unable to build gcs client: %v", err)
	}

	statusURL, _ := url.Parse("https://prow.svc.ci.openshift.org")

	var tx *sqlx.Tx
	var txInsertJob *sqlx.Stmt
	var txInsertMetric *sqlx.Stmt
	var txInsertMetricValue *sqlx.Stmt
	var txInsertReleaseJob *sqlx.Stmt
	var batchSize int
	var insertedJobs, insertedJobMetrics, skippedBeforeDecode, skippedAfterDecode int
	versionsByType := make(map[string]string, 10)

	index := &prow.Index{
		Bucket:    "origin-ci-test",
		IndexName: "job-metrics",
	}
	if err := index.EachJob(context.TODO(), gcsClient, 0, *statusURL, func(partialJob prow.Job, attr *storage.ObjectAttrs) error {
		h := gcsClient.Bucket(index.Bucket).Object(attr.Name)
		r, err := h.NewReader(context.TODO())
		if err != nil {
			return fmt.Errorf("failed to read %s: %v", attr.Name, err)
		}

		jobName, jobNumber := partialJob.Spec.Job, partialJob.Status.BuildID
		jobRange := jobCompletion[jobName]

		// use the completed string as a way to avoid reprocessing an already stored value when scanning
		if completedString, ok := attr.Metadata["completed"]; ok {
			if completed, err := strconv.ParseInt(completedString, 10, 64); err == nil {
				if jobRange.Includes(completed) {
					//log.Printf("Skipping %s/%s because %d <= %d <= %d", jobName, jobID, jobRange.Min, completed, jobRange.Max)
					skippedBeforeDecode++
					return prow.ErrSkip
				}
			}
		}

		metrics := make(map[string]metricdb.OutputMetric, 40)
		d := json.NewDecoder(r)
		if err := d.Decode(&metrics); err != nil {
			return err
		}

		if value, ok := metrics["job:duration:total:seconds"]; ok && jobRange.Includes(value.Timestamp) {
			//log.Printf("Skipping %s/%s because %d <= %d <= %d", jobName, jobID, jobRange.Min, value.Timestamp, jobRange.Max)
			skippedAfterDecode++
			return prow.ErrSkip
		}

		for k := range versionsByType {
			delete(versionsByType, k)
		}

		jobID, ok := jobIds[jobName]
		if !ok {
			if tx == nil {
				tx, err = db.Beginx()
				if err != nil {
					log.Fatal(err)
				}
				batchSize = 0
				txInsertJob = nil
				txInsertMetric = nil
				txInsertMetricValue = nil
				txInsertReleaseJob = nil
			}

			if txInsertJob == nil {
				txInsertJob = tx.Stmtx(insertJob)
			}
			r, err := txInsertJob.Exec(jobName)
			if err != nil {
				log.Fatalf("insert job: %v", err)
			}
			batchSize++
			jobID, err = r.LastInsertId()
			if err != nil {
				log.Fatalf("insert job: %v", err)
			}
			log.Printf("assigned job %s id %d", jobName, jobID)
			jobIds[jobName] = jobID
		}

		for k, v := range metrics {
			name, selector := metricdb.SplitMetricKey(k)
			if len(name) == 0 {
				continue
			}
			selector, ok := metricdb.CheckMetricSelector(selector)
			if !ok {
				log.Printf("warning: Invalid selector %q in %s/%s", k, jobName, jobNumber)
				continue
			}

			if tx == nil {
				tx, err = db.Beginx()
				if err != nil {
					log.Fatal(err)
				}
				batchSize = 0
				txInsertJob = nil
				txInsertMetric = nil
				txInsertMetricValue = nil
				txInsertReleaseJob = nil
			}

			id, ok := metricIds[name]
			if !ok {
				if txInsertMetric == nil {
					txInsertMetric = tx.Stmtx(insertMetric)
				}
				r, err := txInsertMetric.Exec(name)
				if err != nil {
					log.Fatal(err)
				}
				batchSize++
				id, err = r.LastInsertId()
				if err != nil {
					log.Fatal(err)
				}
				log.Printf("assigned metric %s id %d", name, id)
				metricIds[name] = id
			}

			if txInsertMetricValue == nil {
				txInsertMetricValue = tx.Stmtx(insertMetricValue)
			}
			_, err := txInsertMetricValue.Exec(jobID, jobNumber, id, selector, v.Timestamp, v.Value)
			if err != nil {
				log.Fatalf("insert metric_value: %v", err)
			}
			insertedJobMetrics++
			batchSize++

			// if the metric identifies a release version, include in the reference table
			switch name {
			case "cluster:version:info:total":
				if value, ok := metricdb.ValueFromValidSelector(selector, "version"); ok {
					versionsByType[value] = "target"
				}
			case "cluster:version:info:install":
				if value, ok := metricdb.ValueFromValidSelector(selector, "version"); ok {
					versionsByType[value] = "initial"
				}
			case "cluster:version:current:seconds":
				if value, ok := metricdb.ValueFromValidSelector(selector, "version"); ok {
					if _, ok := versionsByType[value]; !ok {
						versionsByType[value] = "initial"
					}
				}
			case "cluster:version:updates:seconds":
				if value, ok := metricdb.ValueFromValidSelector(selector, "version"); ok {
					existing := versionsByType[value]
					if existing != "target" && existing != "initial" {
						versionsByType[value] = "upgrade"
					}
				}
			}
			// log.Printf("%s %s %s %s", partialJob.Spec.Job, partialJob.Status.BuildID, k, v.Value)
		}

		if len(versionsByType) > 0 {
			if txInsertReleaseJob == nil {
				txInsertReleaseJob = tx.Stmtx(insertReleaseJob)
			}
			for version, versionType := range versionsByType {
				major, minor, micro, stream, t, pre, ok := metricdb.VersionParts(version)
				if !ok {
					log.Printf("warning: %s is not a well formed version label", version)
				}
				var unix int64
				if !t.IsZero() {
					unix = t.Unix()
				}

				_, err := txInsertReleaseJob.Exec(major, minor, micro, unix, stream, pre, version, jobID, jobNumber, versionType)
				if err != nil {
					log.Fatalf("insert release job: %v", err)
				}
				batchSize++
			}
		}

		insertedJobs++

		if tx != nil && batchSize >= 1000 {
			log.Printf("Committing batch at %d jobs %d metrics", insertedJobs, insertedJobMetrics)
			if err := tx.Commit(); err != nil {
				log.Fatalf("committing batch: %v", err)
			}
			tx = nil
		}

		return nil
	}); err != nil {
		log.Fatal(err)
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			log.Fatal(err)
		}
		tx = nil
	}

	log.Printf("inserted %d job results and %d metrics total, skipped %d jobs before decode and %d jobs after decode ", insertedJobs, insertedJobMetrics, skippedBeforeDecode, skippedAfterDecode)

	if err := db.Close(); err != nil {
		log.Fatal(err)
	}
}

// Stop is a function to defer in your main call to provide profile info.
type Stop interface {
	Stop()
}

type stopper struct{}

func (stopper) Stop() {}

// Profile returns an interface to defer for a profile: `defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()` is common.
// Suffixing the mode with `-tmp` will have the profiler write the run to a temporary directory with a unique name, which
// is useful when running the same command multiple times.
func Profile(mode string) Stop {
	path := "."
	if strings.HasSuffix(mode, "-tmp") {
		mode = strings.TrimSuffix(mode, "-tmp")
		path = ""
	}
	var stop Stop
	switch mode {
	case "mem":
		stop = profileOnExit(profile.Start(profile.MemProfile, profile.ProfilePath(path), profile.NoShutdownHook, profile.Quiet))
	case "cpu":
		stop = profileOnExit(profile.Start(profile.CPUProfile, profile.ProfilePath(path), profile.NoShutdownHook, profile.Quiet))
	case "block":
		stop = profileOnExit(profile.Start(profile.BlockProfile, profile.ProfilePath(path), profile.NoShutdownHook, profile.Quiet))
	case "mutex":
		stop = profileOnExit(profile.Start(profile.MutexProfile, profile.ProfilePath(path), profile.NoShutdownHook, profile.Quiet))
	case "trace":
		stop = profileOnExit(profile.Start(profile.TraceProfile, profile.ProfilePath(path), profile.NoShutdownHook, profile.Quiet))
	default:
		stop = stopper{}
	}
	return stop
}

func profileOnExit(s Stop) Stop {
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		// Programs with more sophisticated signal handling
		// should ensure the Stop() function returned from
		// Start() is called during shutdown.
		// See http://godoc.org/github.com/pkg/profile
		s.Stop()

		os.Exit(1)
	}()
	return s
}
