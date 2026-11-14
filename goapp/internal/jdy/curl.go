package jdy

import (
	"net/url"
	"regexp"
	"strings"
)

// curlInfo is what the page sends us when the user pastes a "Copy as cURL"
// snippet: the cookie, csrf token and the app the request came from.
type curlInfo struct {
	Host            string
	Scheme          string
	Cookie          string
	CSRF            string
	AppID           string
	UserAgent       string
	JdyVer          string
	SessionShiftBri string
}

var (
	reCurlContinuation = regexp.MustCompile(`\\\s*\r?\n`)
	reCurlURLToken     = regexp.MustCompile(`^https?://`)
	reCurlAppIDJSON    = regexp.MustCompile(`"appId"\s*:\s*"([a-f0-9]{16,32})"`)

	// Go's RE2 has no non-capturing groups, so `/_/(?:admin/)?app/` is spelled
	// out as two patterns.
	reCurlAppIDPaths = []*regexp.Regexp{
		regexp.MustCompile(`/_/admin/app/([a-f0-9]{16,32})/`),
		regexp.MustCompile(`/_/app/([a-f0-9]{16,32})/`),
		regexp.MustCompile(`/dashboard/app/([a-f0-9]{16,32})/`),
		regexp.MustCompile(`/dashboard#/app/([a-f0-9]{16,32})/`),
	}
)

// splitShellArgs splits a cURL command line into arguments, honouring single
// quotes, double quotes and backslash escapes. Regexes cannot do this portably
// (Go's RE2 has no backreferences), and a tokenizer is easier to reason about.
func splitShellArgs(s string) []string {
	args := make([]string, 0, 16)
	var cur strings.Builder
	var quote byte
	started := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			started = true
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			started = true
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if started {
		args = append(args, cur.String())
	}
	return args
}

// parseCurl understands the bash flavour of "Copy as cURL": folded line
// continuations, -H headers, -b/--cookie, --url and the app id buried in the
// request path or POST body.
func parseCurl(text string) curlInfo {
	curl := reCurlContinuation.ReplaceAllString(text, " ")
	args := splitShellArgs(curl)

	headers := make(map[string]string, 8)
	cookie := ""
	rawURL := ""
	for i := 0; i < len(args); i++ {
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch args[i] {
		case "-H", "--header":
			value, ok := next()
			if !ok {
				continue
			}
			if k, v, found := strings.Cut(value, ":"); found {
				headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		case "-b", "--cookie":
			if value, ok := next(); ok {
				cookie = value
			}
		case "--url":
			if value, ok := next(); ok {
				rawURL = value
			}
		}
	}

	if rawURL == "" {
		for _, arg := range args {
			if reCurlURLToken.MatchString(arg) {
				rawURL = arg
				break
			}
		}
	}

	if value := headers["cookie"]; value != "" {
		cookie = value
	}
	csrf := headers["x-csrf-token"]

	host, scheme := DefaultHost, "https"
	if u, err := url.Parse(rawURL); err == nil {
		if u.Host != "" {
			host = u.Host
		}
		if u.Scheme != "" {
			scheme = u.Scheme
		}
	}

	appID := ""
	for _, re := range reCurlAppIDPaths {
		if m := re.FindStringSubmatch(rawURL); m != nil {
			appID = m[1]
			break
		}
	}
	if appID == "" {
		if m := reCurlAppIDJSON.FindStringSubmatch(curl); m != nil {
			appID = m[1]
		}
	}

	return curlInfo{
		Host:            host,
		Scheme:          scheme,
		Cookie:          cookie,
		CSRF:            csrf,
		AppID:           appID,
		UserAgent:       headers["user-agent"],
		JdyVer:          headers["x-jdy-ver"],
		SessionShiftBri: headers["x-sessionshift-bridge"],
	}
}
