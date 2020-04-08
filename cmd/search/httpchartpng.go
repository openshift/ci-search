package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
)

type scatter struct {
	points []image.Point
	radius int
	height int
	width  int
}

func (s *scatter) ColorModel() color.Model {
	return color.AlphaModel
}

func (s *scatter) Bounds() image.Rectangle {
	return image.Rect(0, 0, s.width, s.height)
}

func (s *scatter) At(x, y int) color.Color {
	for _, point := range s.points {
		xx, yy, rr := float64(x-point.X), float64(y-point.Y), float64(s.radius)
		if xx*xx+yy*yy < rr*rr {
			return color.Alpha{200}
		}
	}
	return color.Alpha{0}
}

func (o *options) handleChartPNG(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	defer func() { klog.Infof("Render chart PNG in %s", time.Now().Sub(start).Truncate(time.Millisecond)) }()

	if req.Header.Get("Accept") == "text/html" {
		o.handleChart(w, req)
		return
	}

	index, err := parseRequest(req, "chart", o.MaxAge)
	if err != nil {
		http.Error(w, fmt.Sprintf("Bad input: %v", err), http.StatusBadRequest)
		return
	}

	if len(index.Search) == 0 {
		http.Error(w, "The 'search' query parameter is required", http.StatusBadRequest)
		return
	}

	width := 640
	height := width / 21 * 9

	jobs, err := o.jobAccessor.List(labels.Everything())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load jobs: %v", err), http.StatusInternalServerError)
		return
	}

	maxTime := time.Now()
	minTime := maxTime.Add(-index.MaxAge)
	xScale := float64(width) / index.MaxAge.Seconds()
	result, err := o.searchResult(req.Context(), index)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed search: %v", err), http.StatusInternalServerError)
	}

	maxDuration := 0
	scatters := make([]*scatter, len(index.Search)+4)
	for _, job := range jobs {
		start, stop := job.Status.StartTime.Time, job.Status.CompletionTime.Time
		if start.Before(minTime) {
			continue
		}

		i := -1
		if job.Status.State == "failure" {
			matches, ok := result[job.Status.URL]
			if ok {
				for j, search := range index.Search {
					if _, ok := matches[search]; ok {
						i = j
						break
					}
				}
			}
		}
		if i < 0 {
			switch job.Status.State {
			case "error":
				i = len(scatters) - 4
			case "failure":
				i = len(scatters) - 3
			case "pending":
				i = len(scatters) - 2
			case "success":
				i = len(scatters) - 1
			}

			if i < 0 {
				continue
			}
		}

		if scatters[i] == nil {
			scatters[i] = &scatter{
				points: make([]image.Point, 0, 1),
				radius: 5,
				height: height,
				width:  width,
			}
		}

		if stop.IsZero() {
			stop = maxTime
		}

		dur := int(stop.Sub(start).Seconds())
		if dur > maxDuration {
			maxDuration = dur
		}

		scatters[i].points = append(scatters[i].points, image.Point{
			X: int(start.Sub(minTime).Seconds() * xScale),
			Y: dur,
		})
	}

	for _, scatter := range scatters {
		if scatter == nil {
			continue
		}
		for i := range scatter.points {
			scatter.points[i].Y = height - (scatter.points[i].Y*height)/maxDuration
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := img.Bounds()
	for i := len(scatters) - 1; i >= 0; i-- {
		scatter := scatters[i]
		if scatter == nil {
			continue
		}

		var clr color.Color
		switch len(scatters) - i {
		case 1:
			clr = specialColors["success"]
		case 2:
			clr = specialColors["pending"]
		case 3:
			clr = specialColors["failure"]
		case 4:
			clr = specialColors["error"]
		default:
			if i < len(colors) {
				clr = colors[i]
			} else {
				clr = color.Black
			}
		}

		draw.DrawMask(img, bounds, &image.Uniform{clr}, image.ZP, scatter, image.ZP, draw.Over)
	}

	w.Header().Set("Cache-Control", "public,max-age=30")
	w.Header().Set("Content-Type", "image/png")
	if err = png.Encode(w, img); err != nil {
		klog.Errorf("Failed to write response: %v", err)
	}
}
