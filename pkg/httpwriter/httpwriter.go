package httpwriter

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

type WriteCloser interface {
	io.Writer
	io.Closer
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

func ForRequest(w http.ResponseWriter, req *http.Request) WriteCloser {
	var encoding string
	for _, header := range req.Header["Accept-Encoding"] {
		// FIXME: Propert quality parsing
		// https://tools.ietf.org/html/rfc7231#section-5.3.4
		// https://github.com/golang/go/issues/19307
		for _, enc := range strings.Split(header, ",") {
			if strings.TrimSpace(enc) == "gzip" {
				encoding = "gzip"
				break
			}
		}
		if encoding != "" {
			break
		}
	}

	if encoding == "gzip" {
		w.Header().Set("Content-Encoding", "gzip")
		return gzip.NewWriter(w)
	}

	return nopCloser{w}
}
