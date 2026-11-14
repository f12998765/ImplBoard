package jdy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	appFolderName = "ImplBoard"
	legacyFolder  = "DataBoard"

	// DefaultAppID is the application package the board opens when nothing
	// else is configured.
	DefaultAppID = "6ab0c3281a10a54713c6abf5"
	// DefaultHost is the Jiandaoyun host used when a profile omits it.
	DefaultHost = "www.jiandaoyun.com"
	// DefaultWorkers is how many forms are queried in parallel.
	DefaultWorkers = 16

	defaultWorkers = DefaultWorkers
)

// resolveDataDir picks the writable directory for credentials, progress and
// caches, following the Python layout:
//
//	macOS   ~/Library/Application Support/ImplBoard
//	Windows %APPDATA%/ImplBoard
//	other   $XDG_CONFIG_HOME/ImplBoard or ~/.config/ImplBoard
//
// Older installs used "DataBoard"; if that directory exists and ImplBoard does
// not, it is reused so credentials survive the rename.
func resolveDataDir(dir string) (string, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}

	var base string
	switch {
	case runtime.GOOS == "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support")
	case runtime.GOOS == "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = home
		}
	default:
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".config")
		}
	}

	dir = filepath.Join(base, appFolderName)
	if _, err := os.Stat(dir); err != nil {
		legacy := filepath.Join(base, legacyFolder)
		if st, err := os.Stat(legacy); err == nil && st.IsDir() {
			return legacy, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// expandHome turns a leading "~/" into the user's home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// Profile is the public shape of a stored credential, exactly what the page
// consumes from /api/auth and /api/status.
type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	AppID     string `json:"appId"`
	Env       string `json:"env"`
	UpdatedAt int    `json:"updatedAt"`
	Active    bool   `json:"active"`
}

// store is the on-disk credential document. Profiles stay as raw maps so
// unknown fields written by other builds survive a read/write cycle.
type store struct {
	Active   string
	Profiles []map[string]any
}

func (s store) empty() bool {
	return len(s.Profiles) == 0
}

// loadStore reads the auth file. A missing or unreadable file is an empty
// store, never an error — the server then simply reports "not authenticated".
func loadStore(path string) store {
	raw, err := os.ReadFile(path)
	if err != nil {
		return store{}
	}
	var doc map[string]any
	if err := decodeJSON(raw, &doc); err != nil {
		return store{}
	}
	if list, ok := doc["profiles"].([]any); ok {
		profiles := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m := asMap(item); m != nil {
				profiles = append(profiles, m)
			}
		}
		return store{Active: asString(doc["active"]), Profiles: profiles}
	}
	// Legacy single-credential file.
	if cookie := asString(doc["cookie"]); cookie != "" {
		return store{
			Active: "p1",
			Profiles: []map[string]any{{
				"id":         "p1",
				"name":       orDefault(asString(doc["host"]), "默认"),
				"host":       orDefault(asString(doc["host"]), DefaultHost),
				"scheme":     orDefault(asString(doc["scheme"]), "https"),
				"app_id":     orDefault(asString(doc["app_id"]), DefaultAppID),
				"cookie":     cookie,
				"csrf":       asString(doc["csrf"]),
				"user_agent": asString(doc["user_agent"]),
				"jdy_ver":    asString(doc["jdy_ver"]),
				"createdAt":  time.Now().Unix(),
			}},
		}
	}
	return store{}
}

// saveStore writes atomically with 0600 permissions, like the Python service.
func saveStore(path string, s store) error {
	if s.Profiles == nil {
		s.Profiles = []map[string]any{}
	}
	doc := map[string]any{"active": s.Active, "profiles": s.Profiles}
	data, err := encodeJSONIndent(doc)
	if err != nil {
		return err
	}
	ext := filepath.Ext(path)
	tmp := strings.TrimSuffix(path, ext) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func findProfile(profiles []map[string]any, id string) map[string]any {
	for _, p := range profiles {
		if asString(p["id"]) == id {
			return p
		}
	}
	return nil
}

// publicProfile strips cookies and other secrets from a stored profile.
func publicProfile(p map[string]any, activeID string) Profile {
	host := asString(p["host"])
	name := firstNonEmpty(asString(p["name"]), host, "未命名")
	env := "简道云"
	if strings.HasPrefix(host, "lark.") {
		env = "飞书"
	}
	return Profile{
		ID:        asString(p["id"]),
		Name:      name,
		Host:      host,
		AppID:     asString(p["app_id"]),
		Env:       env,
		UpdatedAt: profileTime(p),
		Active:    activeID != "" && asString(p["id"]) == activeID,
	}
}

// profileTime is `int(p.get("updatedAt") or p.get("createdAt") or 0)`.
func profileTime(p map[string]any) int {
	if v := asInt(p["updatedAt"]); v != 0 {
		return v
	}
	return asInt(p["createdAt"])
}
