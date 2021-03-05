package metricdb

import "github.com/jmoiron/sqlx"

func CreateSchema(db *sqlx.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS metric (
			id INTEGER PRIMARY KEY,
			name TEXT UNIQUE
		);

		CREATE TABLE IF NOT EXISTS metric (
			id INTEGER PRIMARY KEY,
			name TEXT UNIQUE
		);

		CREATE TABLE IF NOT EXISTS metric_value (
			job_name TEXT NOT NULL,
			job_id   UNSIGNED BIG INT NOT NULL,

			metric_id       INTEGER NOT NULL,
			metric_selector TEXT NOT NULL,

			timestamp UNSIGNED BIG INT NOT NULL,
			value     REAL NOT NULL,

			PRIMARY KEY(job_name,job_id,metric_id,metric_selector)
			FOREIGN KEY(metric_id) REFERENCES metrics(id)
		);

		CREATE TABLE IF NOT EXISTS release_job (
			version  TEXT NOT NULL,
			job_name TEXT NOT NULL,
			job_id   UNSIGNED BIG INT NOT NULL,
			type     TEXT check("type" in ('', 'initial', 'upgrade', 'target')) NOT NULL,

			PRIMARY KEY(version,job_name,job_id,type)
		);

		CREATE TABLE IF NOT EXISTS job (
			job_name TEXT NOT NULL,
			job_id   UNSIGNED BIG INT NOT NULL,

			state     TEXT check("state" in ('', 'success', 'failed', 'error')),
			started   UNSIGNED BIG INT,
			completed UNSIGNED BIG INT,

			PRIMARY KEY(job_name,job_id)
		);
		`,
	); err != nil {
		return err
	}
	return nil
}
