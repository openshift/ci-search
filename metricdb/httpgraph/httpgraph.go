package httpgraph

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openshift/ci-search/metricdb"
	"github.com/openshift/ci-search/pkg/httpwriter"
	"k8s.io/klog/v2"
	_ "modernc.org/sqlite"
)

type Server struct {
	DB *metricdb.DB
}

type Graph struct{}

func (g *Graph) From(req *http.Request) error {
	return nil
}

func (g Graph) String() string {
	return "graph{}"
}

func (s *Server) HandleGraph(w http.ResponseWriter, req *http.Request) {
	if s.DB == nil {
		http.Error(w, "Metrics graphing is disabled", http.StatusMethodNotAllowed)
		return
	}

	var graph Graph
	var success bool
	start := time.Now()
	var queryDuration, renderDuration time.Duration
	defer func() {
		klog.Infof("Render graph %s query=%s render=%s duration=%s success=%t", graph.String(), queryDuration.Truncate(time.Millisecond/10), renderDuration.Truncate(time.Millisecond/10), time.Now().Sub(start).Truncate(time.Millisecond), success)
	}()

	if err := graph.From(req); err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if err := req.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("Bad form input: %v", err), http.StatusBadRequest)
		return
	}

	jobIdsByName := s.DB.JobsByName()
	jobNamesById := make(map[int64]string)
	for k, v := range jobIdsByName {
		jobNamesById[v] = k
	}

	metricIdsByName := s.DB.MetricsByName()
	metricNamesById := make(map[int64]string)
	for k, v := range metricIdsByName {
		metricNamesById[v] = k
	}

	jobCountsByName := s.DB.JobCountsByName()

	var warnings []string

	var jobNames []string
	var jobIds []int64
	jobs := req.Form["job"]
	if len(jobs) == 0 || (len(jobs) == 1 && len(jobs[0]) == 0) {
		jobs = []string{"periodic-ci-openshift-release-master-ci-4.8-e2e-aws"}
	}
	for _, job := range jobs {
		if id, ok := jobIdsByName[job]; ok {
			jobNames = append(jobNames, job)
			jobIds = append(jobIds, id)
		} else {
			warnings = append(warnings, fmt.Sprintf("The requested job %q does not exist", job))
		}
	}

	var metricName string
	metric := req.FormValue("metric")
	if len(metric) == 0 {
		metric = "cluster:usage:cpu:total:seconds"
	}
	if len(metric) > 0 {
		if _, ok := metricIdsByName[metric]; ok {
			metricName = metric
		} else {
			warnings = append(warnings, fmt.Sprintf("The requested metric %q does not exist", metric))
		}
	}

	var metricNames []string
	for name := range metricIdsByName {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	metricOptions := make([]string, 0, len(metricNames))
	for _, name := range metricNames {
		metricOptions = append(metricOptions, fmt.Sprintf(`<option %s>%s</option>`, stringSelected(metricName, name), html.EscapeString(name)))
	}

	var allJobNames []string
	for name := range jobIdsByName {
		allJobNames = append(allJobNames, name)
	}
	sort.Strings(allJobNames)
	jobOptions := make([]string, 0, len(allJobNames)+1)
	if len(jobIds) == 0 {
		jobOptions = append(jobOptions, `<option value="">--- Select a metric ---</option>`)
	}
	for _, name := range allJobNames {
		if count, ok := jobCountsByName[name]; ok {
			escapedName := html.EscapeString(name)
			jobOptions = append(jobOptions, fmt.Sprintf(`<option value="%s" %s>%s <em>%d</em></option>`, escapedName, stringSliceSelected(jobNames, name), escapedName, count))
		} else {
			jobOptions = append(jobOptions, fmt.Sprintf(`<option %s>%s</option>`, stringSliceSelected(jobNames, name), html.EscapeString(name)))
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer := httpwriter.ForRequest(w, req)
	defer writer.Close()

	var htmlWarning string
	if len(warnings) > 0 {
		for i, w := range warnings {
			warnings[i] = html.EscapeString(w)
		}
		htmlWarning = fmt.Sprintf(`<ul id="warnings" class="alert alert-warning"><li>%s</ul>`, strings.Join(warnings, "<li>"))
	} else {
		htmlWarning = `<ul id="warnings" class="alert alert-warning" style="display: none"></ul>`
	}
	fmt.Fprintf(writer, htmlPageStart, "Graph CI Metrics", "")
	fmt.Fprintf(writer, htmlIndexForm,
		strings.Join(metricOptions, ""),
		strings.Join(jobOptions, ""),
	)

	fmt.Fprint(writer, htmlWarning)
	fmt.Fprint(writer, htmlGraphVersions)
	fmt.Fprint(writer, htmlPageEnd)
	success = true
}

const htmlPageStart = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8"><title>%s</title>
<link rel="stylesheet" href="/static/bootstrap-4.4.1.min.css" integrity="sha384-Vkoo8x4CGsO3+Hhxv8T/Q5PaXtkKtu6ug5TOeNV6gBiFeWPGFN9MuhOf23Q9Ifjh" crossorigin="anonymous">
<link rel="stylesheet" href="/static/bootstrap-multiselect.min.css">
<link rel="stylesheet" href="/static/uPlot.min.css">
<script src="/static/jquery-3.6.0.min.js"></script>
<script src="/static/uPlot.iife.js"></script>
<script src="/static/placement.min.js"></script>
<script src="/static/popper-1.16.0.min.js"></script>
<script src="/static/bootstrap-4.4.1.min.js"></script>
<script src="/static/bootstrap-multiselect.min.js"></script>
<meta name="viewport" content="width=device-width, initial-scale=1, shrink-to-fit=no">
<style>
.input-group > .multiselect-native-select > div.btn-group {
	height: 100%%;
}
.input-group-lg > .multiselect-native-select > div.btn-group > .custom-select {
	font-size: 1.25rem;
	height: calc(1.5em + 1rem + 2px);
	line-height: 1.5;
	padding-top: 8px;
	padding-bottom: 8px;
}
.u-legend {
	//position: absolute;
	//background: rgba(0, 0, 0, 0.8);
	background: white;
	padding: 0.5rem;
	margin: 0.75rem;
	//color: #fff;
	z-index: 10;
	pointer-events: none;
	//white-space: pre-line;
	text-align: left;
}
.u-legend tr td:last-child {
	text-align: right;
}
</style>
</head>
<body>
<div id="results" class="container-fluid %s">
`

const htmlPageEnd = `
</div>
</body>
</html>
`

const htmlIndexForm = `
<form id="graph-controls" class="form mt-4 mb-4" method="GET">
	<div class="input-group input-group-lg mb-2">
		<div class="input-group-prepend"><span class="input-group-text" for="name">Metric:</span></div>
		<select title="Metrics to visualize" class="form-control custom-select" name="metric" onchange="refresh();">%[1]s</select>
		<select id="graph-controls-job" title="Jobs to show metrics for" class="form-control custom-select" name="job" multiple="multiple" onchange="refresh();">%[2]s</select>
	</div>
</form>
<script>$(document).ready(function() { $('#graph-controls-job').multiselect(); });</script>
`

const htmlGraphVersions = `
<div id="width"></div>
<div id="graph">
	<div class="ml-3" style="margin-top: 3rem; color: #666;">
		<p>Display metrics from CI runs.</p>
	</div>
</div>
<script>

function onclick(u, seriesIdx, dataIdx) {
	console.log("clicked "+seriesIdx+" "+dataIdx)
}

function tooltipPlugin() {
	let over, bLeft, bTop;
	let seriesIdx = null;
	let dataIdx = null;

	function syncBounds() {
		let bbox = over.getBoundingClientRect();
		bLeft = bbox.left;
		bTop = bbox.top;
	}

	// if ($( "#overlay" ).length == 0) {
	// 	const overlay = document.createElement("div");
	// 	overlay.id = "overlay";
	// 	overlay.className = "tooltip-overlay"
	// 	overlay.style.display = "none";
	// 	overlay.style.position = "absolute";
	// 	document.body.appendChild(overlay);
	// }
	let overlay = null;

	return {
		hooks: {
			init: u => {
				overlay = u.root.querySelector(".u-legend");
				over = u.root.querySelector(".u-over");
				over.onmouseenter = () => { overlay.style.display = "block" };
				over.onmouseleave = () => { overlay.style.display = "none" };
			},
			destroy: u => {
				over.onmouseenter = null
				over.onmouseleave = null
			},
			ready: u => {
				overlay = u.root.querySelector(".u-legend");
				overlay.style.display = "none";
				overlay.style.position = "absolute";

				over = u.root.querySelector(".u-over");
				let clientX;
				let clientY;
				over.onmousedown = e => {
					clientX = e.clientX;
					clientY = e.clientY;
				};
				over.onmouseup = e => {
					if (e.clientX == clientX && e.clientY == clientY) {
						console.log("attempt clicked "+seriesIdx+" "+dataIdx)
						if (seriesIdx != null && dataIdx != null) {
							onclick(u, seriesIdx, dataIdx);
						}
					}
				};
			},
			setSize: u => {
				syncBounds();
			},
			setSeries: (u, sidx) => {
				if (seriesIdx != sidx) {
					seriesIdx = sidx;
				}
			},
			setCursor: u => {
				const { left, top, idx } = u.cursor;
				if (dataIdx != idx) {
					dataIdx = idx;
				}
				if (dataIdx == null) {
					overlay.style.display = "none"
					return
				}

				/*const { left, top, idx } = u.cursor;
				const x = u.data[0][idx];
				let data = [];
				for (i = 1; i<u.data.length-1; i++) {
					if (u.data[i][idx]) {
						data.push([u.series[i].label, Number(u.data[i][idx])]);
					}
				}
				data.sort((a,b) => { return b[1] - a[1] });
				let msg = data.map(d => { return ` + "`" + `<tr><td>${d[0]}</td><td align="right">${d[1].toFixed(0)}</td></tr>` + "`" + ` });
				const label = u.data[u.data.length-1][idx];
				overlay.innerHTML = ` + "`" + `${label}:<table>${msg.join("")}</table>` + "`" + `;*/

				overlay.style.display = "block"
 				const anchor = { left: left + bLeft, top: top + bTop };
				placement(overlay, anchor, "right", "start", { over });
			},
		}
	};
}

function graphControlState() {
	return $( "#graph-controls" ).serializeArray().reduce((acc, el) => { 
		if (!acc[el.name]) { 
			acc[el.name] = el.value 
		} else {
			if (!Array.isArray(acc[el.name])) { acc[el.name] = [acc[el.name]] }
			acc[el.name].push(el.value);
		}
		return acc;
	},{})
}

let page = {
	ordinal: true,
};

function refresh() {
	inputs = graphControlState();
	if (JSON.stringify(page.inputs) == JSON.stringify(inputs)) {
		console.log("inputs unchanged")
		return;
	}

	if (page.load) {
		page.load.abort()
		page.load = null
	}

	window.history.pushState({}, "", window.location.pathname + "?" + $.param(inputs, true))

	page.load = $.ajax("/graph/api/metrics/job",{data: inputs, traditional: true}).always((x, res) => {
		w = $( "#warnings" )[0];
		if (res != "success") {
			w.innerHTML = "<li>An error occurred, unable to retrieve metric data";
			w.style.display = "";
			return;
		}
		if (!x.success) {
			w.innerHTML = "<li>";
			w.firstChild.textContext = "Unable to load metrics: "+(x.message || "unknown error");
			w.style.display = "";
			return;
		}

		let series = x.series
		if (series.length < 2) {
			w.innerHTML = "<li>The requested metric does not exist for this job";
			w.style.display = "";
			return;
		}
		w.style.display = "none";

		let data = []
		series.forEach((e, i) => { data.push(x.data[series[i].label || ""]) })
		data.push(x.labels)

		if (page.graph) {
			page.handleResize.off();
			page.graph.destroy();
			page.handleResize = page.graph = null
		}
	
		function getSize() { 
			let o = document.getElementById("width")
			let w = o.scrollWidth
			return { width: w, height: w * 1/2,	} 
		}
		valueFn = (u, sidx, idx) => {
			let value = Number(data[sidx][idx])
			if (value < 1) {
				value = value.toFixed(3)
			} else if (value < 10) {
				value = value.toFixed(2)
			} else if (value < 100) {
				value = value.toFixed(1)
			} else {
				value = value.toFixed(0)
			}
			return {
				Release: data[data.length-1][idx],
				Value: value,
			};
		}
		series.forEach((el,i) => {
			el.values = valueFn
			el.spanGaps = true
			switch ((i+2) % 3) {
			case 0:
				el.stroke = "blue";
				break;
			case 1:
				el.stroke = "red";
				break;
			case 2:
				el.stroke = "green"
				break;
			}
		})

		let opt = {
			plugins: [
				tooltipPlugin(onclick),
			],
			legend: {show: true},
			...getSize(),
			series: series,
			cursor: {
				dataIdx: (u, seriesIdx, hoveredIdx) => {
					let seriesData = u.data[seriesIdx];

					if (seriesData[hoveredIdx] == null) {
						let nonNullLft = hoveredIdx,
							nonNullRgt = hoveredIdx,
							i;

						i = hoveredIdx;
						while (nonNullLft == hoveredIdx && i-- > 0)
							if (seriesData[i] != null)
								nonNullLft = i;

						i = hoveredIdx;
						while (nonNullRgt == hoveredIdx && i++ < seriesData.length)
							if (seriesData[i] != null)
								nonNullRgt = i;

						return nonNullRgt - hoveredIdx > hoveredIdx - nonNullLft ? nonNullLft : nonNullRgt;
					}

					return hoveredIdx;
				},
			},
			scales: {},
			axes: [
				{
					rotate: -60,
					size: 290,
				},
				{
					values: (u, vals) => {
						return vals.map(v => {
							return (
								v >= 1e12 ? v/1e12 + "T" :
								v >= 1e9  ? v/1e9  + "G" :
								v >= 1e6  ? v/1e6  + "M" :
								v >= 1e3  ? v/1e3  + "k" :
								v
							);
						});
					},
				},
			],
		}
		if (page.ordinal) {
			data[0].forEach((e, i) => { data[0][i] = i+1; })
			opt.scales.x = {
				distr: 2,
				time: false,	
			}
			opt.axes[0].values = (u, vals) => {
				return vals.map((v) => {
					return u.data[u.data.length-1][v];
				});
			}
		}

		const graphNode = $( "#graph")[0]
		graphNode.innerHTML = ""
		page.inputs = inputs
		const graph = new uPlot(opt, data, graphNode);	
		page.graph = graph
		page.handleResize = $( window ).resize(() => { graph.setSize(getSize()); })
	})
}

refresh()
</script>
`

func stringSelected(current, expected string) string {
	if current == expected {
		return "selected"
	}
	return ""
}

func stringSliceSelected(current []string, expected string) string {
	for _, s := range current {
		if s == expected {
			return "selected"
		}
	}
	return ""
}
