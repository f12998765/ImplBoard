package jdy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout = 10 * time.Second
	requestRetries = 1
	maxWorkers     = DefaultWorkers
)

// Client is the read-only Jiandaoyun client. Only lookup endpoints are wired
// up here (menu, app tree, form detail, record count); nothing in this package
// can create, update or delete data.
type Client struct {
	scheme string
	host   string
	base   string
	cookie string
	csrf   string
	extra  map[string]string
	appID  string
	http   *http.Client
}

func newClient(host, scheme, cookie, csrf string, extra map[string]string) *Client {
	if scheme == "" {
		scheme = "https"
	}
	if host == "" {
		host = DefaultHost
	}
	return &Client{
		scheme: scheme,
		host:   host,
		base:   scheme + "://" + host,
		cookie: cookie,
		csrf:   csrf,
		extra:  extra,
		http:   &http.Client{Timeout: requestTimeout},
	}
}

func (c *Client) request(method, path string, body any) (any, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}

	var last error
	for attempt := 0; attempt <= requestRetries; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(method, c.base+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("accept", "application/json, text/plain, */*")
		req.Header.Set("x-requested-with", "XMLHttpRequest")
		req.Header.Set("origin", c.base)
		req.Header.Set("referer", c.base+"/dashboard")
		if payload != nil {
			req.Header.Set("content-type", "application/json")
		}
		if c.cookie != "" {
			req.Header.Set("cookie", c.cookie)
		}
		if c.csrf != "" {
			req.Header.Set("x-csrf-token", c.csrf)
		}
		for k, v := range c.extra {
			if v != "" {
				req.Header.Set(k, v)
			}
		}

		resp, err := c.http.Do(req)
		if err != nil {
			last = err
		} else {
			raw, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case readErr != nil:
				last = readErr
			case resp.StatusCode >= 400:
				last = fmt.Errorf("HTTP %d %s", resp.StatusCode,
					truncateRunes(strings.TrimSpace(string(raw)), 100))
				if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
					// Bad cookie or csrf: retrying cannot help.
					return nil, last
				}
			case !strings.Contains(resp.Header.Get("content-type"), "json"):
				last = fmt.Errorf("非 JSON 响应（%s），Cookie 或 csrf 可能已过期",
					resp.Header.Get("content-type"))
			default:
				var out any
				if decErr := decodeJSON(raw, &out); decErr != nil {
					last = decErr
				} else {
					return out, nil
				}
			}
		}
		if attempt < requestRetries {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if last == nil {
		last = fmt.Errorf("请求失败")
	}
	return nil, last
}

func (c *Client) post(path string, body any) (any, error) {
	if body == nil {
		body = map[string]any{}
	}
	return c.request(http.MethodPost, path, body)
}

func (c *Client) get(path string) (any, error) {
	return c.request(http.MethodGet, path, nil)
}

// appsRaw lists application packages: POST /dashboard/apps.
func (c *Client) appsRaw() (any, error) {
	return c.post("/dashboard/apps", map[string]any{"has_app_group": true})
}

// EntryMap reads the application menu and returns its entryMap. Errors are
// propagated: callers that only want a best-effort tree swallow them.
func (c *Client) EntryMap() (map[string]any, error) {
	data, err := c.post("/_/app/"+c.appID+"/menu", nil)
	if err != nil {
		return nil, err
	}
	if m := asMap(data); m != nil {
		if em := asMap(m["entryMap"]); em != nil {
			return em, nil
		}
	}
	return map[string]any{}, nil
}

// Tree returns the application tree from the dashboard endpoint, falling back
// to the older menu endpoint when that returns nothing usable.
func (c *Client) Tree() (map[string]any, error) {
	if data, err := c.post("/dashboard/api_doc/list_entry", map[string]any{"appId": c.appID}); err == nil {
		if m := asMap(data); m != nil && truthy(m["entryMap"]) {
			return m, nil
		}
	}
	em, err := c.EntryMap()
	if err != nil {
		return nil, err
	}
	return map[string]any{"entryMap": em, "menu": nil, "entryList": nil}, nil
}

// formDetail fetches a form's structure (POST /_/admin/app/{app}/form/{entry}).
func (c *Client) formDetail(entryID string) (map[string]any, error) {
	data, err := c.post("/_/admin/app/"+c.appID+"/form/"+entryID, nil)
	if err != nil {
		return nil, err
	}
	if m := asMap(data); m != nil {
		if ent := asMap(m["entry"]); ent != nil {
			return ent, nil
		}
	}
	return map[string]any{}, nil
}

// count returns how many records match, optionally bounded by createTime.
// The endpoint ignores an `lt` condition, so day buckets are derived by
// subtracting two `gte` counts (see dayAdded).
func (c *Client) count(entryID, since, until string) (int, error) {
	filters := make([]any, 0, 2)
	if since != "" {
		filters = append(filters, map[string]any{"field": "createTime", "method": "gte", "value": since})
	}
	if until != "" {
		filters = append(filters, map[string]any{"field": "createTime", "method": "lt", "value": until})
	}
	body := map[string]any{"appId": c.appID, "entryId": entryID}
	if len(filters) > 0 {
		body["filter"] = map[string]any{"rel": "and", "cond": filters}
	}

	resp, err := c.post("/_/data_process/admin/data/count", body)
	if err != nil {
		if len(filters) > 0 {
			return 0, err
		}
		// The unfiltered form sometimes needs an explicit empty condition.
		body["filter"] = map[string]any{"rel": "and", "cond": []any{}}
		resp, err = c.post("/_/data_process/admin/data/count", body)
		if err != nil {
			return 0, err
		}
	}
	if m := asMap(resp); m != nil {
		return asInt(m["count"]), nil
	}
	return 0, nil
}
