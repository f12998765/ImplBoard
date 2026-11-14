package qf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	appName       = "ImplBoard"
	legacyAppName = "DataBoard"
	authFileName  = "qf_auth.json"
)

// Profile is one saved Qingflow account. The token/wsid pair is what every
// upstream request carries.
//
// wsid is kept as an untyped JSON value because older auth files store it as a
// number while newly pasted cURLs yield a string; the Python version round-trips
// it unchanged, so do we.
type Profile struct {
	ID          string
	Name        string
	Token       string
	WSID        any
	QFVersion   string
	CookieExtra string

	// extra keeps unknown keys from an existing auth file so rewriting the file
	// (add/switch/delete) never drops data written by another tool.
	extra map[string]json.RawMessage
}

func (p *Profile) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	p.ID = rawString(raw["id"])
	p.Name = rawString(raw["name"])
	p.Token = rawString(raw["token"])
	p.WSID = rawValue(raw["wsid"])
	p.QFVersion = rawString(raw["qfVersion"])
	p.CookieExtra = rawString(raw["cookieExtra"])
	for _, k := range []string{"id", "name", "token", "wsid", "qfVersion", "cookieExtra"} {
		delete(raw, k)
	}
	if len(raw) > 0 {
		p.extra = raw
	}
	return nil
}

func (p Profile) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(p.extra)+6)
	for k, v := range p.extra {
		out[k] = v
	}
	set := func(k string, v any) error {
		b, err := marshalNoEscape(v)
		if err != nil {
			return err
		}
		out[k] = b
		return nil
	}
	for _, err := range []error{
		set("id", p.ID),
		set("name", p.Name),
		set("token", p.Token),
		set("wsid", p.WSID),
		set("qfVersion", p.QFVersion),
		set("cookieExtra", p.CookieExtra),
	} {
		if err != nil {
			return nil, err
		}
	}
	return marshalNoEscape(out)
}

// WSIDText is the form used for the "wsid" request header and for display.
func (p Profile) WSIDText() string {
	if p.WSID == nil {
		return ""
	}
	if s, ok := p.WSID.(string); ok {
		return s
	}
	if n, ok := p.WSID.(json.Number); ok {
		return n.String()
	}
	return fmt.Sprint(p.WSID)
}

// AuthConfig mirrors qf_auth.json: {"profiles": [...], "active": "q123"}.
type AuthConfig struct {
	Profiles []Profile
	Active   *string

	extra map[string]json.RawMessage
}

func (c *AuthConfig) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	c.Profiles = nil
	c.Active = nil
	if v, ok := raw["profiles"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &c.Profiles); err != nil {
			return err
		}
	}
	if v, ok := raw["active"]; ok && string(v) != "null" {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return err
		}
		c.Active = &s
	}
	delete(raw, "profiles")
	delete(raw, "active")
	if len(raw) > 0 {
		c.extra = raw
	}
	return nil
}

func (c AuthConfig) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(c.extra)+2)
	for k, v := range c.extra {
		out[k] = v
	}
	profiles := c.Profiles
	if profiles == nil {
		profiles = []Profile{}
	}
	pb, err := marshalNoEscape(profiles)
	if err != nil {
		return nil, err
	}
	out["profiles"] = pb
	ab, err := marshalNoEscape(c.Active)
	if err != nil {
		return nil, err
	}
	out["active"] = ab
	return marshalNoEscape(out)
}

// ActiveProfile returns the profile matching cfg.Active, or nil. Python's
// behaviour when there is no match is exactly "not configured yet".
func (c *AuthConfig) ActiveProfile() *Profile {
	if c == nil || c.Active == nil {
		return nil
	}
	for i := range c.Profiles {
		if c.Profiles[i].ID == *c.Active {
			return &c.Profiles[i]
		}
	}
	return nil
}

func (c *AuthConfig) setActive(id string) {
	c.Active = &id
}

// DefaultDataDir is the per-user writable directory. It falls back to the old
// DataBoard directory when an installation has not been migrated yet.
func DefaultDataDir() string {
	var base string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			base = home
		}
	default:
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
	}
	d := filepath.Join(base, appName)
	if _, err := os.Stat(d); err != nil {
		old := filepath.Join(base, legacyAppName)
		if _, err := os.Stat(old); err == nil {
			return old
		}
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

func defaultAuthPath(dataDir string) string {
	if p := os.Getenv("QF_AUTH"); p != "" {
		return p
	}
	dir := dataDir
	if dir == "" {
		dir = DefaultDataDir()
	}
	return filepath.Join(dir, authFileName)
}

func loadAuth(path string) *AuthConfig {
	cfg := &AuthConfig{}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := unmarshalJSON(b, cfg); err != nil {
		return &AuthConfig{}
	}
	return cfg
}

func saveAuth(path string, cfg *AuthConfig) error {
	data, err := marshalIndent(cfg)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// curlInfo is what parseCurl extracts from a browser "Copy as cURL".
type curlInfo struct {
	Token       string
	WSID        string
	QFVersion   string
	CookieExtra string
}

var (
	curlTokenRe  = regexp.MustCompile(`(?i)-H\s+["']token:\s*([^"']+)["']`)
	curlWSIDRe   = regexp.MustCompile(`(?i)-H\s+["']wsid:\s*([^"']+)["']`)
	curlCookieRe = regexp.MustCompile(`(?i)(?:-b|--cookie)\s+["']([^"']+)["']`)
)

// parseCurl pulls token / wsid / qfVersion out of a pasted cURL command.
// Header flags win over cookies; cookies only fill the gaps.
func parseCurl(text string) curlInfo {
	text = strings.ReplaceAll(text, "\\\n", " ")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " ")

	out := curlInfo{QFVersion: "mongo1"}
	if m := curlTokenRe.FindStringSubmatch(text); m != nil {
		out.Token = strings.TrimSpace(m[1])
	}
	if m := curlWSIDRe.FindStringSubmatch(text); m != nil {
		out.WSID = strings.TrimSpace(m[1])
	}

	var cookie string
	if m := curlCookieRe.FindStringSubmatch(text); m != nil {
		cookie = m[1]
	}
	if cookie == "" {
		return out
	}

	jar := map[string]string{}
	for _, part := range strings.Split(cookie, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		jar[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if out.Token == "" {
		out.Token = jar["prodtoken"]
	}
	if out.WSID == "" {
		out.WSID = jar["prodwsId"]
	}
	if v := jar["qfVersion"]; v != "" {
		out.QFVersion = v
	}
	var keep []string
	for _, k := range []string{"prodUid", "JSESSIONID", "prodwsId"} {
		if jar[k] != "" {
			keep = append(keep, k+"="+jar[k])
		}
	}
	out.CookieExtra = strings.Join(keep, "; ")
	return out
}

// seedFromEnv lets the desktop shell inject credentials with QF_TOKEN without
// touching the UI. It behaves like Python's seed_from_env: an existing profile
// with the same token is updated and activated, otherwise a "qenv" profile is
// created/replaced.
func seedFromEnv(path string) {
	token := os.Getenv("QF_TOKEN")
	if token == "" {
		return
	}
	wsid := os.Getenv("QF_WSID")
	if wsid == "" {
		wsid = "690763"
	}
	qfver := os.Getenv("QF_VERSION")
	if qfver == "" {
		qfver = "mongo1"
	}

	cfg := loadAuth(path)
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Token == token {
			cfg.Profiles[i].WSID = wsid
			cfg.Profiles[i].QFVersion = qfver
			cfg.setActive(cfg.Profiles[i].ID)
			_ = saveAuth(path, cfg)
			return
		}
	}

	kept := cfg.Profiles[:0]
	for _, p := range cfg.Profiles {
		if p.ID != "qenv" {
			kept = append(kept, p)
		}
	}
	cfg.Profiles = append(kept, Profile{
		ID:        "qenv",
		Name:      "锋芒（env）",
		Token:     token,
		WSID:      wsid,
		QFVersion: qfver,
	})
	cfg.setActive("qenv")
	_ = saveAuth(path, cfg)
}

// rawValue decodes a raw JSON scalar for storage, keeping numbers exact.
func rawValue(b json.RawMessage) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

func rawString(b json.RawMessage) string {
	v := rawValue(b)
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func unmarshalJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}
