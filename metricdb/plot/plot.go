package main

import (
	"log"
	"math"

	"github.com/jmoiron/sqlx"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	_ "modernc.org/sqlite"
)

func main() {
	db, err := sqlx.Open("sqlite", "file:search.db")
	if err != nil {
		log.Fatal(err)
	}

	p, err := plot.New()
	if err != nil {
		log.Fatal(err)
	}

	var read int
	rows, err := db.Query("SELECT r.version, m.job_name, avg(m.value) as value FROM metric_value AS m, release_job as r, metric AS metric WHERE metric.id = m.metric_id AND metric.name == 'cluster:usage:cpu:total:rate' AND r.job_name = m.job_name AND r.job_id = m.job_id AND r.version LIKE '4.8.0-0.ci-%' AND r.type == 'target' GROUP BY r.version,m.job_name ORDER by r.version;")
	if err != nil {
		log.Fatal(err)
	}
	var xNames []string
	series := make(map[string]plotter.XYs)
	var version, jobName string
	var value float64
	for rows.Next() {
		if err := rows.Scan(&version, &jobName, &value); err != nil {
			log.Fatal(err)
		}
		if l := len(xNames); l == 0 || xNames[l-1] != version {
			xNames = append(xNames, version)
		}
		series[jobName] = append(series[jobName], plotter.XY{X: float64(len(xNames) - 1), Y: value})
		read++
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	p.NominalX(xNames...)
	p.Legend.XAlign = draw.XLeft
	p.X.Tick.Label.Rotation = math.Pi / 4
	p.X.Tick.Label.XAlign = draw.XRight

	for jobName, s := range series {
		if len(s) < 2 {
			continue
		}
		if err := plotutil.AddLinePoints(p, jobName, s); err != nil {
			log.Fatal(err)
		}
	}
	if err := p.Save(24*vg.Inch, 12*vg.Inch, "points.svg"); err != nil {
		log.Fatal(err)
	}
	log.Printf("read %d", read)
}
