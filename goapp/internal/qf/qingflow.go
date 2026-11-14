package qf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://qingflow.com/api"
	requestTimeout = 8 * time.Second
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
)

// qfAPI talks to the Qingflow read-only endpoints. Every method here is a GET
// or a filter query; nothing in this package mutates remote data.
type qfAPI struct {
	token       string
	wsid        string
	qfVersion   string
	cookieExtra string
	baseURL     string
	client      *http.Client
}

func newQfAPI(p *Profile, baseURL string, client *http.Client) *qfAPI {
	base := baseURL
	if base == "" {
		base = defaultBaseURL
	}
	c := client
	if c == nil {
		c = &http.Client{Timeout: requestTimeout}
	}
	version := p.QFVersion
	if version == "" {
		version = "mongo1"
	}
	return &qfAPI{
		token:       p.Token,
		wsid:        p.WSIDText(),
		qfVersion:   version,
		cookieExtra: p.CookieExtra,
		baseURL:     strings.TrimRight(base, "/"),
		client:      c,
	}
}

// profileKey is Python's api.token[-6:], the cache namespace for a token.
func profileKey(token string) string {
	if len(token) <= 6 {
		return token
	}
	return token[len(token)-6:]
}

func (a *qfAPI) buildRequest(path string, body any) (*http.Request, error) {
	var payload io.Reader
	if body != nil {
		b, err := marshalNoEscape(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, a.baseURL+path, payload)
	if err != nil {
		return nil, err
	}

	h := req.Header
	h.Set("token", a.token)
	h.Set("wsid", a.wsid)
	h.Set("accept", "application/json, text/plain, */*")
	h.Set("referer", "https://qingflow.com/")
	h.Set("user-agent", userAgent)
	h.Set("x-q-request-id", fmt.Sprintf("%032x", uint64(time.Now().UnixMilli())))

	cookie := "qfVersion=" + a.qfVersion +
		"; prodtoken=" + a.token +
		"; prodwsId=" + a.wsid
	if a.cookieExtra != "" {
		cookie += "; " + a.cookieExtra
	}
	h.Set("Cookie", cookie)

	if body != nil {
		h.Set("content-type", "application/json")
		// Qingflow's new interface check looks for the header, not its value.
		h["NewInterfaceSkipHeader"] = []string{""}
	}
	return req, nil
}

// request returns a decoded JSON object. Transport failures are shaped like
// Python's: {"code": -1, "message": ...} / {"code": <http status>, ...}.
func (a *qfAPI) request(path string, body any) map[string]any {
	req, err := a.buildRequest(path, body)
	if err != nil {
		return map[string]any{"code": -1, "message": err.Error()}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return map[string]any{"code": -1, "message": err.Error()}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return map[string]any{"code": resp.StatusCode, "message": fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	if readErr != nil {
		return map[string]any{"code": -1, "message": readErr.Error()}
	}
	var out map[string]any
	if err := unmarshalJSON(raw, &out); err != nil {
		return map[string]any{"code": -1, "message": err.Error()}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func responseCode(m map[string]any) (int, bool) {
	v, ok := m["code"]
	if !ok {
		return 0, false
	}
	n, ok := asInt64(v)
	if !ok {
		return 0, false
	}
	return int(n), true
}

// retryable reports whether Python would retry the call. Any code it does not
// recognise (including a missing one) is treated as final.
func retryable(code int, present bool) bool {
	if !present {
		return false
	}
	switch code {
	case -1, 500, 502, 503, 504:
		return true
	}
	return false
}

func responseError(m map[string]any) error {
	if msg, ok := m["message"].(string); ok && msg != "" {
		return errors.New(msg)
	}
	if code, ok := responseCode(m); ok {
		return errors.New(strconv.Itoa(code))
	}
	return errors.New("unknown error")
}

// tree fetches the application-package tree (GET /tag/apps).
func (a *qfAPI) tree() ([]any, error) {
	resp := a.request("/tag/apps", nil)
	if code, ok := responseCode(resp); !ok || code != 0 {
		return nil, responseError(resp)
	}
	data, _ := resp["data"].(map[string]any)
	tags, _ := data["tags"].([]any)
	return tags, nil
}

// amount counts records (POST /app/{key}/apply/filter/amount).
//
// With day only, the query covers that whole day; with since it spans
// [since, day 23:59:59]. A nil day means "all records".
func (a *qfAPI) amount(appKey, day, since string) *int64 {
	body := map[string]any{"pageNum": 1, "pageSize": 1, "type": 8}
	if since != "" {
		end := day
		if end == "" {
			end = today()
		}
		body["queries"] = []any{map[string]any{
			"queId": 2, "scope": 1, "minValue": since, "maxValue": end + " 23:59:59",
		}}
	} else if day != "" {
		body["queries"] = []any{map[string]any{
			"queId": 2, "scope": 1, "minValue": day, "maxValue": day,
		}}
	}

	for attempt := 0; attempt < 2; attempt++ {
		resp := a.request("/app/"+appKey+"/apply/filter/amount", body)
		code, present := responseCode(resp)
		if present && code == 0 {
			data, _ := resp["data"].(map[string]any)
			if v, ok := asInt64(data["resultAmount"]); ok {
				return &v
			}
			return nil
		}
		if !retryable(code, present) {
			return nil
		}
	}
	return nil
}

// publish reads the last publish time and publisher (GET /app/{key}/publish).
func (a *qfAPI) publish(appKey string) (*int64, *string) {
	for attempt := 0; attempt < 2; attempt++ {
		resp := a.request("/app/"+appKey+"/publish", nil)
		code, present := responseCode(resp)
		if present && code == 0 {
			data, _ := resp["data"].(map[string]any)
			var pub *int64
			if ms, ok := asInt64(data["lastPublishTime"]); ok {
				pub = &ms
			}
			var user *string
			if u, ok := data["lastPublishUser"].(map[string]any); ok {
				if name, ok := u["userName"].(string); ok {
					user = &name
				}
			}
			return pub, user
		}
		if !retryable(code, present) {
			return nil, nil
		}
	}
	return nil, nil
}

// --- small JSON helpers -------------------------------------------------

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		if f, err := t.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// truthy mirrors Python's bool() for decoded JSON values.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, err := t.Float64()
		return err == nil && f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}
