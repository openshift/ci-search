package bugzilla

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
	"path"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

type Client struct {
	Base    url.URL
	Client  *http.Client
	Retries int

	APIKey string
	Token  string
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
	// Bugzilla 5.0.4 and below don't support these headers
	if len(c.APIKey) > 0 {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.APIKey))
	}
	if len(c.Token) > 0 {
		req.Header["X-Bugzilla-Token"] = []string{c.Token}
	}
}

func (c *Client) BugCommentsByID(ctx context.Context, bugs ...int) (*BugCommentsList, error) {
	if len(bugs) == 0 {
		return &BugCommentsList{}, nil
	}
	u := c.Base
	u.Path = path.Join(u.Path, "bug", url.PathEscape(strconv.Itoa(bugs[0])), "comment")
	v := make(url.Values)
	for _, bug := range bugs[1:] {
		v.Add("ids", strconv.Itoa(bug))
	}
	v.Add("include_fields", strings.Join(bugCommentFields, ","))
	u.RawQuery = v.Encode()

	var bugList *BugCommentsList
	err := readJSONIntoObject(ctx, c.Retries, c.Client, func() (interface{}, *http.Request, error) {
		bugList = &BugCommentsList{}
		req, err := http.NewRequest("GET", u.String(), nil)
		c.addRequestHeaders(req)
		return bugList, req, err
	})
	return bugList, err
}

func (c *Client) SearchBugs(ctx context.Context, args SearchBugsArgs) (*BugInfoList, error) {
	u := c.Base
	u.Path = path.Join(u.Path, "bug")
	if args.IncludeFields == nil {
		args.IncludeFields = bugInfoFields
	}
	v := make(url.Values)
	args.Add(v)
	u.RawQuery = v.Encode()

	var bugList *BugInfoList
	err := readJSONIntoObject(ctx, c.Retries, c.Client, func() (interface{}, *http.Request, error) {
		bugList = &BugInfoList{}
		req, err := http.NewRequest("GET", u.String(), nil)
		c.addRequestHeaders(req)
		return bugList, req, err
	})
	return bugList, err
}

func (c *Client) BugsByID(ctx context.Context, bugs ...int) (*BugInfoList, error) {
	return c.SearchBugs(ctx, SearchBugsArgs{IDs: bugs})
}

type ClientError struct {
	Err Error
}

func (e *ClientError) Error() string {
	return e.Err.Message
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
				var clientErr ClientError
				if mediaType == "application/json" {
					data, err := ioutil.ReadAll(resp.Body)
					if err != nil {
						return err
					}
					glogBody("Response body: ", data)
					if err := json.Unmarshal(data, &clientErr); err != nil {
						return err
					}
				}
				if !clientErr.Err.Error {
					return &ClientError{Err: Error{Error: true, Code: resp.StatusCode, Message: fmt.Sprintf("unknown client error %d", resp.StatusCode)}}
				}
				return &clientErr
			}

			if mediaType != "application/json" {
				return fmt.Errorf("unrecognized 200 response from Bugzilla API: %s", contentType)
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
	case klog.V(10).Enabled():
		return body
	case klog.V(9).Enabled():
		max = 10240
	case klog.V(8).Enabled():
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
	if klog.V(8).Enabled() {
		if bytes.IndexFunc(body, func(r rune) bool {
			return r < 0x0a
		}) != -1 {
			klog.Infof("%s:\n%s", prefix, truncateBody(hex.Dump(body)))
		} else {
			klog.Infof("%s: %s", prefix, truncateBody(string(body)))
		}
	}
}
