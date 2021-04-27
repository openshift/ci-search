package metricdb

import (
	"time"

	"github.com/jmoiron/sqlx"
)

type metricBatchInserter struct {
	db *sqlx.DB

	insertJob            *sqlx.Stmt
	insertMetric         *sqlx.Stmt
	insertMetricValue    *sqlx.Stmt
	insertReleaseJob     *sqlx.Stmt
	insertScrapeProgress *sqlx.Stmt

	tx                  *sqlx.Tx
	txInsertJob         *sqlx.Stmt
	txInsertMetric      *sqlx.Stmt
	txInsertMetricValue *sqlx.Stmt
	txInsertReleaseJob  *sqlx.Stmt

	maxBatch int64
	inserted int64

	lastCompletedKey string
	lastIndex        string
	lastDirty        bool
}

func NewBatchInserter(db *sqlx.DB, maxBatch int64) (*metricBatchInserter, error) {
	b := &metricBatchInserter{
		db:       db,
		maxBatch: maxBatch,
	}
	return b, nil
}

func (b *metricBatchInserter) Flush() error {
	tx, err := b.addProgressToTxn()
	if err != nil {
		return err
	}

	if tx == nil {
		return nil
	}

	b.tx = nil
	b.inserted = 0

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (b *metricBatchInserter) txForInsert() (*sqlx.Tx, error) {
	if b.tx != nil {
		if b.inserted < b.maxBatch {
			return b.tx, nil
		}
		if err := b.Flush(); err != nil {
			return nil, err
		}
	}

	tx, err := b.db.Beginx()
	if err != nil {
		return nil, err
	}
	b.txInsertJob = nil
	b.txInsertMetric = nil
	b.txInsertMetricValue = nil
	b.txInsertReleaseJob = nil
	b.tx = tx
	return tx, nil
}

func (b *metricBatchInserter) InsertJob(jobName string) (int64, error) {
	tx, err := b.txForInsert()
	if err != nil {
		return 0, err
	}
	if b.insertJob == nil {
		b.insertJob, err = b.db.Preparex("INSERT INTO job (name) VALUES(?)")
		if err != nil {
			return 0, err
		}
	}
	if b.txInsertJob == nil {
		b.txInsertJob = tx.Stmtx(b.insertJob)
	}
	r, err := b.txInsertJob.Exec(jobName)
	if err != nil {
		return 0, err
	}
	jobID, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	b.inserted++
	return jobID, nil
}

func (b *metricBatchInserter) InsertMetric(metricName string) (int64, error) {
	tx, err := b.txForInsert()
	if err != nil {
		return 0, err
	}
	if b.insertMetric == nil {
		b.insertMetric, err = b.db.Preparex("INSERT INTO metric (name) VALUES(?)")
		if err != nil {
			return 0, err
		}
	}
	if b.txInsertMetric == nil {
		b.txInsertMetric = tx.Stmtx(b.insertMetric)
	}
	r, err := b.txInsertMetric.Exec(metricName)
	if err != nil {
		return 0, err
	}
	metricID, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	b.inserted++
	return metricID, nil
}

func (b *metricBatchInserter) InsertReleaseJob(major, minor, micro int, stream string, unix int64, pre, version string, jobID, jobNumber int64, versionType string) error {
	tx, err := b.txForInsert()
	if err != nil {
		return err
	}
	if b.insertReleaseJob == nil {
		b.insertReleaseJob, err = b.db.Preparex("INSERT INTO release_job (major, minor, micro, timestamp, stream, pre, version, job_id, job_number, type) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING")
		if err != nil {
			return err
		}
	}
	if b.txInsertReleaseJob == nil {
		b.txInsertReleaseJob = tx.Stmtx(b.insertReleaseJob)
	}
	if _, err := b.txInsertReleaseJob.Exec(major, minor, micro, unix, stream, pre, version, jobID, jobNumber, versionType); err != nil {
		return err
	}
	b.inserted++
	return nil
}

func (b *metricBatchInserter) InsertMetricValue(jobID, jobNumber int64, id int64, selector string, timestamp int64, value string) error {
	tx, err := b.txForInsert()
	if err != nil {
		return err
	}
	if b.insertMetricValue == nil {
		b.insertMetricValue, err = b.db.Preparex("INSERT INTO metric_value (job_id, job_number, metric_id, metric_selector, timestamp, value) VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING")
		if err != nil {
			return err
		}
	}
	if b.txInsertMetricValue == nil {
		b.txInsertMetricValue = tx.Stmtx(b.insertMetricValue)
	}
	if _, err := b.txInsertMetricValue.Exec(jobID, jobNumber, id, selector, timestamp, value); err != nil {
		return err
	}
	b.inserted++
	return nil
}

func (b *metricBatchInserter) CompletedKey(index, key string) {
	if index == b.lastIndex && b.lastCompletedKey == key {
		return
	}
	if len(b.lastIndex) > 0 && len(b.lastCompletedKey) > 0 {
		b.lastDirty = true
	}
	b.lastIndex = index
	b.lastCompletedKey = key
}

func (b *metricBatchInserter) addProgressToTxn() (*sqlx.Tx, error) {
	if !b.lastDirty || len(b.lastCompletedKey) == 0 || len(b.lastIndex) == 0 {
		return nil, nil
	}

	var err error
	if b.insertScrapeProgress == nil {
		b.insertScrapeProgress, err = b.db.Preparex(`
			INSERT INTO scrape (name, last_key, timestamp) VALUES(?,?,?)
				ON CONFLICT(name) DO UPDATE SET
					last_key=excluded.last_key,
					timestamp=excluded.timestamp
		`)
		if err != nil {
			return nil, err
		}
	}

	tx := b.tx
	if tx == nil {
		tx, err = b.db.Beginx()
		if err != nil {
			return nil, err
		}
	}

	t := time.Now().Unix()
	if _, err := tx.Stmtx(b.insertScrapeProgress).Exec(b.lastIndex, b.lastCompletedKey, t); err != nil {
		return nil, err
	}
	//klog.Infof("DEBUG: Recorded completing up to %s %s %d along with %d inserts", b.lastIndex, b.lastCompletedKey, t, b.inserted)
	b.lastDirty = false
	return tx, nil
}

type rh struct {
	rows *sqlx.Rows
	err  error
}

func RowsOf(rows *sqlx.Rows, err error) rh {
	return rh{
		rows: rows,
		err:  err,
	}
}

func (h rh) Every(values []interface{}, fn func()) error {
	rows := h.rows
	for rows.Next() {
		if err := rows.Scan(values...); err != nil {
			return err
		}
		fn()
	}
	return rows.Err()
}

func (h rh) Each(values []interface{}, fn func() error) error {
	rows := h.rows
	for rows.Next() {
		if err := rows.Scan(values...); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
	}
	return rows.Err()
}
