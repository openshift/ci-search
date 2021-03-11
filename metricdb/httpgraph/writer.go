package httpgraph

import (
	"fmt"
	"strconv"
)

type seriesDefinition struct {
	label  string
	hide   bool
	fill   string
	stroke string
}

type GraphDataWriter struct {
	buf    []byte
	series []seriesDefinition
}

func (w *GraphDataWriter) Var(variable string) *GraphDataWriter {
	if w.buf == nil {
		w.buf = make([]byte, 0, 4096)
	}
	w.buf = append(w.buf, []byte(fmt.Sprintf("<script>let %s = [", variable))...)
	return w
}

func (w *GraphDataWriter) nextSeries(label string) seriesDefinition {
	d := seriesDefinition{label: label}
	switch (len(w.series) + 2) % 3 {
	case 0:
		d.stroke = "blue"
		d.fill = "rgba(0,0,255,0.05)"
	case 1:
		d.stroke = "red"
		d.fill = "rgba(255,0,0,0.05)"
	case 2:
		d.stroke = "green"
		d.fill = "rgba(0,255,0,0.05)"
	}
	return d
}

func (w *GraphDataWriter) Int64Series(label string, arr []int64) *GraphDataWriter {
	buf := w.buf
	if len(w.series) > 0 {
		buf = append(buf, []byte("],\n[")...)
	} else {
		buf = append(buf, []byte("\n[")...)
	}
	w.series = append(w.series, w.nextSeries(label))
	for i, v := range arr {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendInt(buf, v, 10)
	}
	w.buf = buf
	return w
}

func (w *GraphDataWriter) StringSeries(label string, arr []string) *GraphDataWriter {
	buf := w.buf
	if len(w.series) > 0 {
		buf = append(buf, []byte("],\n[")...)
	} else {
		buf = append(buf, []byte("\n[")...)
	}
	w.series = append(w.series, w.nextSeries(label))
	for i, v := range arr {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendQuote(buf, v)
	}
	w.buf = buf
	return w
}

func (w *GraphDataWriter) FloatSeries(label string, arr []float64) *GraphDataWriter {
	buf := w.buf
	if len(w.series) > 0 {
		buf = append(buf, []byte("],\n[")...)
	} else {
		buf = append(buf, []byte("\n[")...)
	}
	w.series = append(w.series, w.nextSeries(label))
	for i, v := range arr {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, v, 'f', 5, 64)
	}
	w.buf = buf
	return w
}

func (w *GraphDataWriter) Series(label string, fn func(buf []byte) []byte) *GraphDataWriter {
	buf := w.buf
	if len(w.series) > 0 {
		buf = append(buf, []byte("],\n[")...)
	} else {
		buf = append(buf, []byte("\n[")...)
	}
	w.series = append(w.series, w.nextSeries(label))
	w.buf = fn(buf)
	return w
}

func (w *GraphDataWriter) HideLast() *GraphDataWriter {
	w.series[len(w.series)-1].hide = true
	return w
}

func (w *GraphDataWriter) Done(seriesVariable string) []byte {
	if len(w.series) > 0 {
		w.buf = append(w.buf, []byte("]")...)
	}
	w.buf = append(w.buf, []byte("\n]\n")...)
	if len(seriesVariable) > 0 {
		w.buf = append(w.buf, []byte(fmt.Sprintf("let %s = [", seriesVariable))...)
		for i, definition := range w.series {
			if i > 0 {
				w.buf = append(w.buf, []byte(",")...)
			}
			w.buf = append(w.buf, []byte(fmt.Sprintf("{label:%q,show:%t", definition.label, !definition.hide))...)
			if len(definition.fill) > 0 {
				w.buf = append(w.buf, []byte(fmt.Sprintf(",fill:%q", definition.fill))...)
			}
			if len(definition.stroke) > 0 {
				w.buf = append(w.buf, []byte(fmt.Sprintf(",stroke:%q", definition.stroke))...)
			}
			w.buf = append(w.buf, []byte("}")...)
		}
		w.buf = append(w.buf, []byte("]\n")...)
	}
	w.buf = append(w.buf, []byte("</script>")...)
	return w.buf
}
