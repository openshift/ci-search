package metricdb

import "github.com/jmoiron/sqlx"

func CreateSchema(db *sqlx.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scrape (
			name      TEXT UNIQUE,
			last_key  TEXT,
			timestamp UNSIGHNEDB BIG INT NOT NULL,
			PRIMARY KEY(name)
		);

		CREATE TABLE IF NOT EXISTS metric (
			id INTEGER PRIMARY KEY,
			name TEXT UNIQUE
		);

		CREATE TABLE IF NOT EXISTS job (
			id INTEGER PRIMARY KEY,
			name TEXT UNIQUE
		);

		CREATE TABLE IF NOT EXISTS metric_value (
			job_id     INTEGER NOT NULL,
			job_number UNSIGNED BIG INT NOT NULL,

			metric_id       INTEGER NOT NULL,
			metric_selector TEXT NOT NULL,

			timestamp UNSIGNED BIG INT NOT NULL,
			value     REAL NOT NULL,

			PRIMARY KEY(job_id,job_number,metric_id,metric_selector)
			FOREIGN KEY(metric_id) REFERENCES metrics(id)
			FOREIGN KEY(job_id) REFERENCES job(id)
		) WITHOUT ROWID;

		CREATE TABLE IF NOT EXISTS release_job (
			major     INTEGER NOT NULL CHECK(major >= 0),
			minor     INTEGER NOT NULL CHECK(minor >= 0),
			micro     INTEGER NOT NULL CHECK(micro >= 0),
			timestamp UNSIGNED BIG INT NOT NULL CHECK(micro >= 0),
			stream    TEXT NOT NULL,
			pre       TEXT NOT NULL,
			version   TEXT NOT NULL,

			job_id     INTEGER NOT NULL,
			job_number UNSIGNED BIG INT NOT NULL,
			type       TEXT check("type" in ('', 'initial', 'upgrade', 'target')) NOT NULL,

			PRIMARY KEY(major,minor,micro,timestamp,stream,pre,version,job_id,job_number,type)
		) WITHOUT ROWID;

		CREATE TABLE IF NOT EXISTS job_state (
			job_id     INTEGER NOT NULL,
			job_number UNSIGNED BIG INT NOT NULL,

			state     TEXT check("state" in ('', 'success', 'failed', 'error')),
			started   UNSIGNED BIG INT,
			completed UNSIGNED BIG INT,

			PRIMARY KEY(job_id,job_number)
			FOREIGN KEY(job_id) REFERENCES job(id)
		) WITHOUT ROWID;
		`,
	); err != nil {
		return err
	}
	return nil
}
