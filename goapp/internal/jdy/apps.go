package jdy

import (
	"regexp"
	"strings"
	"sync"
)

// nameKeys / nestKeys are the fields worth probing for a human readable app
// name; the dashboard payload is not documented and varies per tenant.
var (
	nameKeys = []string{"name", "appName", "showName", "displayName", "title", "label", "text"}
	nestKeys = []string{"app", "appInfo", "info", "meta", "base", "data"}

	reAppID = regexp.MustCompile(`^[a-f0-9]{16,32}$`)
)

// AppItem is one entry of the /api/apps dropdown.
type AppItem struct {
	AppID string `json:"appId"`
	Name  string `json:"name"`
}

func validAppID(id string) bool {
	return reAppID.MatchString(id)
}

// pickName digs a readable name out of whatever shape the API returned.
func pickName(node map[string]any) string {
	for _, k := range nameKeys {
		if v, ok := node[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	for _, k := range nestKeys {
		if nested := asMap(node[k]); nested != nil {
			if got := pickName(nested); got != "" {
				return got
			}
		}
	}
	return ""
}

func pickNameAny(v any) string {
	if m := asMap(v); m != nil {
		return pickName(m)
	}
	return ""
}

// walkApps hoists every {appId, name} pair out of an arbitrary JSON payload.
// Later occurrences only fill in a name that is still empty.
func walkApps(node any, out *[]AppItem, depth int) {
	if depth > 5 {
		return
	}
	switch v := node.(type) {
	case []any:
		for _, child := range v {
			walkApps(child, out, depth+1)
		}
	case map[string]any:
		id := firstNonEmpty(asString(v["appId"]), asString(v["app_id"]), asString(v["_id"]))
		if validAppID(id) {
			name := pickName(v)
			if idx := indexOfApp(*out, id); idx < 0 {
				*out = append(*out, AppItem{AppID: id, Name: name})
			} else if name != "" && (*out)[idx].Name == "" {
				(*out)[idx].Name = name
			}
		}
		for _, child := range v {
			switch child.(type) {
			case []any, map[string]any:
				walkApps(child, out, depth+1)
			}
		}
	}
}

func indexOfApp(apps []AppItem, id string) int {
	for i := range apps {
		if apps[i].AppID == id {
			return i
		}
	}
	return -1
}

// resolveAppName asks the application itself when the dashboard list has no
// name for it. Every probe is best-effort; failures just fall through.
func resolveAppName(c *Client, appID string) string {
	probes := []struct {
		method string
		path   string
	}{
		{"POST", "/_/app/" + appID + "/menu"},
		{"GET", "/_/admin/app/" + appID + "/info_get"},
		{"POST", "/_/admin/app/" + appID + "/info_get"},
		{"GET", "/_/app/" + appID + "/info_get"},
		{"POST", "/_/app/" + appID + "/get"},
	}
	for _, p := range probes {
		var data any
		var err error
		if p.method == "GET" {
			data, err = c.get(p.path)
		} else {
			data, err = c.post(p.path, nil)
		}
		if err != nil {
			continue
		}
		name := pickNameAny(data)
		// A menu response carries entryMap; its "name" keys are form names,
		// not the application's.
		if name != "" && !strings.Contains(headJSON(data, 200), "entryMap") {
			return name
		}
	}
	return ""
}

func headJSON(v any, n int) string {
	raw, err := encodeJSON(v)
	if err != nil {
		return ""
	}
	return truncateRunes(string(raw), n)
}

// fetchApps builds the application package list. It prefers the documented
// dashboard endpoint and retries a handful of legacy paths when that is empty.
func fetchApps(c *Client, currentAppID string) []AppItem {
	apps := make([]AppItem, 0, 16)

	add := func(id, name string) {
		if !validAppID(id) {
			return
		}
		if idx := indexOfApp(apps, id); idx < 0 {
			apps = append(apps, AppItem{AppID: id, Name: name})
		} else if name != "" && apps[idx].Name == "" {
			apps[idx].Name = name
		}
	}

	if data, err := c.appsRaw(); err == nil {
		if doc := asMap(data); doc != nil {
			for _, item := range asList(doc["apps"]) {
				a := asMap(item)
				if a == nil {
					continue
				}
				add(firstNonEmpty(asString(a["appId"]), asString(a["_id"])),
					firstNonEmpty(asString(a["name"]), asString(a["appName"])))
			}
			for _, group := range asList(doc["app_group_list"]) {
				g := asMap(group)
				if g == nil {
					continue
				}
				for _, item := range asList(g["apps"]) {
					a := asMap(item)
					if a == nil {
						continue
					}
					add(firstNonEmpty(asString(a["appId"]), asString(a["_id"])),
						asString(a["name"]))
				}
			}
		}
	}

	if len(apps) == 0 {
		probes := []struct {
			method string
			path   string
		}{
			{"GET", "/_/dashboard/apps?withGroup=1"},
			{"GET", "/_/dashboard/apps"},
			{"GET", "/dashboard/apps"},
			{"GET", "/_/dashboard/spaces"},
			{"POST", "/_/dashboard/apps"},
			{"POST", "/_/dashboard/app_group"},
		}
		for _, p := range probes {
			var data any
			var err error
			if p.method == "GET" {
				data, err = c.get(p.path)
			} else {
				data, err = c.post(p.path, nil)
			}
			if err != nil {
				continue
			}
			walkApps(data, &apps, 0)
			if anyNamed(apps) {
				break
			}
		}
	}

	if len(apps) == 0 {
		apps = append(apps, AppItem{AppID: currentAppID})
	}
	return apps
}

func anyNamed(apps []AppItem) bool {
	for _, a := range apps {
		if a.Name != "" {
			return true
		}
	}
	return false
}

// NameMap resolves the display name for a single app, caching nothing.
func (s *Server) appNameFromList(appID string) string {
	apps, err := s.apps()
	if err != nil {
		return ""
	}
	for _, a := range apps {
		if a.AppID == appID {
			return a.Name
		}
	}
	return ""
}

// resolveNames fills in missing names via resolveAppName, six at a time.
func resolveNames(c *Client, apps []AppItem) {
	todo := make([]int, 0, len(apps))
	for i := range apps {
		if apps[i].Name == "" {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 || len(todo) > 12 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, i := range todo {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			apps[i].Name = resolveAppName(c, apps[i].AppID)
		}()
	}
	wg.Wait()
}

// flattenTree turns the menu + entryMap into a flat form list plus the number
// of groups. Without a menu it falls back to the entryMap parent relations.
func flattenTree(tree map[string]any) ([]formEntry, int) {
	entryMap := asMap(tree["entryMap"])
	if entryMap == nil {
		entryMap = map[string]any{}
	}
	menu := asList(tree["menu"])
	forms := make([]formEntry, 0, len(entryMap))

	var walk func(nodes []any, ancestors []string)
	walk = func(nodes []any, ancestors []string) {
		for _, item := range nodes {
			node := asMap(item)
			if node == nil {
				continue
			}
			id := asString(node["id"])
			meta := asMap(entryMap[id])
			name := asString(meta["name"])
			if asString(meta["type"]) == "form" {
				group := ""
				if len(ancestors) > 0 {
					group = ancestors[len(ancestors)-1]
				}
				forms = append(forms, formEntry{
					EntryID: id,
					Name:    name,
					Group:   group,
					HasFlow: asBool(meta["hasFlow"]),
				})
				continue
			}
			next := ancestors
			if name != "" {
				next = append(append([]string(nil), ancestors...), name)
			}
			walk(asList(node["child"]), next)
		}
	}

	if len(menu) > 0 {
		walk(menu, nil)
	}

	if len(forms) == 0 {
		groups := make(map[string]string, len(entryMap))
		for _, item := range entryMap {
			m := asMap(item)
			if asString(m["type"]) == "group" {
				groups[asString(m["entryId"])] = asString(m["name"])
			}
		}
		for _, id := range sortedKeys(entryMap) {
			m := asMap(entryMap[id])
			if asString(m["type"]) != "form" {
				continue
			}
			forms = append(forms, formEntry{
				EntryID: asString(m["entryId"]),
				Name:    asString(m["name"]),
				Group:   groups[asString(m["parent"])],
				HasFlow: asBool(m["hasFlow"]),
			})
		}
	}

	groupCount := 0
	for _, item := range entryMap {
		if asString(asMap(item)["type"]) == "group" {
			groupCount++
		}
	}
	return forms, groupCount
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// sortedKeys gives deterministic iteration where Python relied on JSON order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
