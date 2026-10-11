package reinhardt

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"reflect"
	"strings"
	"text/template/parse"
	"time"
	"unicode"
)

// TemplateContext carries request data and the URL/translation callbacks. Each
// include receives its own context when a page overrides a loop or display value.
type TemplateContext = map[string]any

type PageTemplate struct {
	*template.Template
	UsesLayout bool
}

type Templates map[string]PageTemplate

func newTemplates() (Templates, error) {
	files, err := fs.Glob(assets, "templates/*/*.html")
	if err != nil {
		return nil, err
	}
	baseName := "reinhardt/_base.html"
	base := template.New(baseName).Funcs(template.FuncMap{
		"scope": templateScope, "length": templateLength, "first": templateFirst,
		"regroup": regroup, "loopInfo": templateLoopInfo, "contains": templateContains,
		"eq": templateEqual, "ne": func(a, b any) bool { return !templateEqual(a, b) },
		"gt":           func(a, b any) bool { return number(a) > number(b) },
		"defaultValue": templateDefault, "yesno": templateYesNo, "djangoBool": templateBool,
		"truncate": templateTruncate, "humanDuration": templateDuration,
		"hasManyArtists": templateHasManyArtists, "title": templateTitle, "urlencode": templateURLQuery,
	}).Option("missingkey=zero")
	data, err := assets.ReadFile("templates/" + baseName)
	if err != nil {
		return nil, err
	}
	if _, err = base.Parse(string(data)); err != nil {
		return nil, err
	}
	for _, file := range files {
		name := strings.TrimPrefix(file, "templates/")
		data, err = assets.ReadFile(file)
		if err != nil {
			return nil, err
		}
		if name == baseName || !strings.HasPrefix(path.Base(name), "_") || strings.Contains(string(data), `{{ define "main" }}`) {
			continue
		}
		if _, err = base.New(name).Parse(string(data)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	result := Templates{}
	for _, file := range files {
		name := strings.TrimPrefix(file, "templates/")
		// Pages override shared names such as "main". A private clone prevents
		// whichever page was parsed last from replacing every other page's body.
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if t.Lookup(name) == nil {
			data, err = assets.ReadFile(file)
			if err != nil {
				return nil, err
			}
			if _, err = t.New(name).Parse(string(data)); err != nil {
				return nil, fmt.Errorf("parse %s: %w", name, err)
			}
		}
		result[name] = PageTemplate{Template: t, UsesLayout: name == baseName || templateUsesLayout(t.Lookup(name).Tree.Root, baseName)}
	}
	return result, nil
}

func templateUsesLayout(list *parse.ListNode, baseName string) bool {
	// This checks direct calls and their control-flow branches, not transitive
	// includes. Pages that need layout context must call _base.html themselves.
	if list == nil {
		return false
	}
	for _, node := range list.Nodes {
		switch node := node.(type) {
		case *parse.TemplateNode:
			if node.Name == baseName {
				return true
			}
		case *parse.IfNode:
			if templateUsesLayout(node.List, baseName) || templateUsesLayout(node.ElseList, baseName) {
				return true
			}
		case *parse.RangeNode:
			if templateUsesLayout(node.List, baseName) || templateUsesLayout(node.ElseList, baseName) {
				return true
			}
		case *parse.WithNode:
			if templateUsesLayout(node.List, baseName) || templateUsesLayout(node.ElseList, baseName) {
				return true
			}
		}
	}
	return false
}

func templateScope(ctx TemplateContext, pairs ...any) TemplateContext {
	// Includes need isolated bindings like Django's "include ... with". This is
	// a shallow copy: assigning a binding is safe, but nested Rows remain shared.
	result := make(TemplateContext, len(ctx)+len(pairs)/2)
	for key, value := range ctx {
		result[key] = value
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		result[fmt.Sprint(pairs[i])] = pairs[i+1]
	}
	return result
}

func templateLength(value any) int {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return 0
	}
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String:
		return v.Len()
	}
	return 0
}

func templateFirst(value any) any {
	if templateLength(value) == 0 {
		return Row{}
	}
	return reflect.ValueOf(value).Index(0).Interface()
}

func templateLoopInfo(index int) Row {
	return Row{"first": index == 0, "counter": index + 1}
}

func templateContains(value, list any) bool {
	v := reflect.ValueOf(list)
	if !v.IsValid() || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) {
		return false
	}
	for i := 0; i < v.Len(); i++ {
		if templateEqual(value, v.Index(i).Interface()) {
			return true
		}
	}
	return false
}

func templateEqual(a, b any) bool {
	// SQL IDs are int64 while Go template integer literals are int.
	isInteger := func(v any) bool {
		r := reflect.ValueOf(v)
		return r.IsValid() && r.Kind() >= reflect.Int && r.Kind() <= reflect.Uint64
	}
	if isInteger(a) && isInteger(b) {
		return number(a) == number(b)
	}
	return reflect.DeepEqual(a, b)
}

func templateDefault(value, fallback any) any {
	if truth, _ := template.IsTrue(value); truth {
		return value
	}
	return fallback
}

func templateYesNo(value any, choices string) string {
	parts := strings.Split(choices, ",")
	if truth, _ := template.IsTrue(value); truth {
		return parts[0]
	}
	return parts[1]
}

func templateBool(value any) string {
	if value == nil {
		return ""
	}
	if v, ok := value.(bool); ok {
		if v {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(value)
}

func templateTruncate(value any, count int) string {
	runes := []rune(fmt.Sprint(value))
	if len(runes) <= count {
		return string(runes)
	}
	if count < 1 {
		return ""
	}
	return string(runes[:count-1]) + "…"
}

func templateDuration(value any) string {
	n := number(value)
	return fmt.Sprintf("%02d:%02d:%02d", n/3600, n/60%60, n%60)
}

func templateHasManyArtists(rows []Row) bool {
	artists := map[string]bool{}
	for _, row := range rows {
		artists[row.S("artist")] = true
	}
	return len(artists) > 1
}

func templateTitle(value any) string {
	var result strings.Builder
	previousCased := false
	for _, r := range fmt.Sprint(value) {
		if previousCased {
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(unicode.ToTitle(r))
		}
		previousCased = unicode.IsLower(r) || unicode.IsUpper(r) || unicode.IsTitle(r)
	}
	return result.String()
}

func templateURLQuery(value any) template.URL {
	if value == nil {
		return ""
	}
	encoded := strings.ReplaceAll(url.QueryEscape(fmt.Sprint(value)), "+", "%20")
	// Django leaves '/' safe. Every other reserved character is encoded before
	// marking this query component as a URL, preventing a second escaping pass.
	return template.URL(strings.ReplaceAll(encoded, "%2F", "/"))
}

func regroup(v []Row, key string) []Row {
	// Django regroup combines consecutive values; it neither sorts nor merges
	// distant matches. Query ordering therefore determines the displayed groups.
	groups := []Row{}
	for _, r := range v {
		if len(groups) == 0 || groups[len(groups)-1].S("grouper") != r.S(key) {
			groups = append(groups, Row{"grouper": r[key], "list": []Row{}})
		}
		g := groups[len(groups)-1]
		g["list"] = append(g["list"].([]Row), r)
	}
	return groups
}
func (s *Store) URL(name string, args ...any) string {
	arg := func(n int) string {
		if n >= len(args) {
			return ""
		}
		return fmt.Sprint(args[n])
	}
	p := name + "/"
	switch name {
	case "index":
		p = ""
	case "login":
		p = "accounts/login/"
	case "music_file_search_page":
		p = "dj/"
	case "music_file_search_api":
		p = "api/music_files/search/"
	case "dj_next_song_api":
		p = "api/dj/next_song/"
	case "download_database":
		p = "preferences/database/"
	case "artist_add_to_tag", "artist_add_to_queue", "artist_add_to_favorites", "search_add_to_tag", "search_add_to_queue", "search_add_to_favorites", "queue_add_to_tag", "queue_add_to_favorites":
		p = strings.Replace(name, "_add_", "/add_", 1) + "/"
	case "ytrip_metadata":
		p = "ytrip/metadata/"
	case "ytrip_failures":
		p = "ytrip/failures/"
	case "retry_ytrip_failure":
		p = "ytrip/failures/" + arg(0) + "/retry/"
	case "rename_tag", "track_details_modal":
		p = name + "/" + arg(0)
	case "artwork", "artwork_modal":
		p = name + "/" + arg(0) + "/" + arg(1) + ".jpg"
	case "shared_tag", "download_shared_tag", "serve_shared_music_file":
		p = "shared/" + arg(0) + "/" + arg(1) + "/"
		if name == "download_shared_tag" {
			p += "download/"
		}
		if name == "serve_shared_music_file" {
			p += "track/" + arg(2) + "/"
		}
	}
	return s.C.Path(p)
}

// Use the committed gettext catalogue, including multiline messages.
func translations() map[string]string {
	b, _ := assets.ReadFile("locale/fr/LC_MESSAGES/django.po")
	result := map[string]string{}
	key, value, mode := "", "", ""
	flush := func() {
		if key != "" && value != "" {
			result[key] = value
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "msgid ") {
			flush()
			key = ""
			value = ""
			mode = "id"
			line = strings.TrimPrefix(line, "msgid ")
		} else if strings.HasPrefix(line, "msgstr ") {
			mode = "str"
			line = strings.TrimPrefix(line, "msgstr ")
		} else if !strings.HasPrefix(line, "\"") {
			continue
		}
		var text string
		if json.Unmarshal([]byte(line), &text) != nil {
			continue
		}
		if mode == "id" {
			key += text
		} else {
			value += text
		}
	}
	flush()
	return result
}

func generatedAt() string { return time.Now().UTC().Format("2006-01-02 15:04:05") }
