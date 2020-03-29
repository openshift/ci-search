package prow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"

	"k8s.io/klog"
)

type Client struct {
	Base    url.URL
	Client  *http.Client
	Retries int
}

func NewClient(base url.URL) *Client {
	return &Client{
		Base:   base,
		Client: http.DefaultClient,
	}
}

func (c *Client) addRequestHeaders(req *http.Request) {
	if req == nil {
		return
	}
}

func (c *Client) newRequestValues() url.Values {
	v := make(url.Values)
	return v
}

func (c *Client) ListJobs(ctx context.Context) (*JobList, error) {
	u := c.Base
	v := c.newRequestValues()
	u.RawQuery = v.Encode()

	var list *JobList
	err := readJSONIntoObject(ctx, c.Retries, c.Client, func() (interface{}, *http.Request, error) {
		list = &JobList{}
		req, err := http.NewRequest("GET", u.String(), nil)
		c.addRequestHeaders(req)
		return list, req, err
	})
	return list, err
}

func readJSONIntoObject(ctx context.Context, retries int, client *http.Client, fn func() (interface{}, *http.Request, error)) error {
	var lastErr error
	for i := 0; i < (retries + 1); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		obj, req, err := fn()
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if err := func() error {
			defer resp.Body.Close()
			defer io.Copy(ioutil.Discard, resp.Body)
			contentType := resp.Header.Get("Content-Type")
			mediaType, _, err := mime.ParseMediaType(contentType)
			if err != nil {
				return fmt.Errorf("unrecognized content type from Bugzilla API: %s: %v", contentType, err)
			}
			if resp.StatusCode != 200 {
				data, _ := ioutil.ReadAll(&io.LimitedReader{N: 2048, R: resp.Body})
				return fmt.Errorf("unknown client error %d: %q", resp.StatusCode, data)
			}
			if mediaType != "application/json" {
				return fmt.Errorf("unrecognized 200 response from Prow API: %s", contentType)
			}
			data, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			glogBody("Response body: ", data)
			if err := json.Unmarshal(data, obj); err != nil {
				return err
			}
			return nil
		}(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// truncateBody decides if the body should be truncated, based on the glog Verbosity.
func truncateBody(body string) string {
	max := 0
	switch {
	case bool(klog.V(10)):
		return body
	case bool(klog.V(9)):
		max = 10240
	case bool(klog.V(8)):
		max = 1024
	}

	if len(body) <= max {
		return body
	}

	return body[:max] + fmt.Sprintf(" [truncated %d chars]", len(body)-max)
}

// glogBody logs a body output that could be either JSON or protobuf. It explicitly guards against
// allocating a new string for the body output unless necessary. Uses a simple heuristic to determine
// whether the body is printable.
func glogBody(prefix string, body []byte) {
	if klog.V(8) {
		if bytes.IndexFunc(body, func(r rune) bool {
			return r < 0x0a
		}) != -1 {
			klog.Infof("%s:\n%s", prefix, truncateBody(hex.Dump(body)))
		} else {
			klog.Infof("%s: %s", prefix, truncateBody(string(body)))
		}
	}
}
