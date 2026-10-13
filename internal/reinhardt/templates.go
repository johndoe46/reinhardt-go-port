package reinhardt

import (
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
		"dict":  templateDict,
		"scope": templateScope, "length": templateLength, "first": templateFirst,
		"regroup": regroup, "loopInfo": templateLoopInfo, "contains": templateContains,
		"eq": templateEqual, "ne": func(left, right any) bool { return !templateEqual(left, right) },
		"gt":           func(left, right any) bool { return number(left) > number(right) },
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
		pageTemplate, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if pageTemplate.Lookup(name) == nil {
			data, err = assets.ReadFile(file)
			if err != nil {
				return nil, err
			}
			if _, err = pageTemplate.New(name).Parse(string(data)); err != nil {
				return nil, fmt.Errorf("parse %s: %w", name, err)
			}
		}
		result[name] = PageTemplate{Template: pageTemplate, UsesLayout: name == baseName || templateUsesLayout(pageTemplate.Lookup(name).Tree.Root, baseName)}
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
	for index := 0; index+1 < len(pairs); index += 2 {
		result[fmt.Sprint(pairs[index])] = pairs[index+1]
	}
	return result
}

func templateDict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict requires key/value pairs")
	}
	result := make(map[string]any, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		key, ok := pairs[index].(string)
		if !ok {
			return nil, fmt.Errorf("dict key must be a string")
		}
		result[key] = pairs[index+1]
	}
	return result, nil
}

func templateLength(value any) int {
	reflectedValue := reflect.ValueOf(value)
	if !reflectedValue.IsValid() {
		return 0
	}
	switch reflectedValue.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String:
		return reflectedValue.Len()
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
	reflectedList := reflect.ValueOf(list)
	if !reflectedList.IsValid() || (reflectedList.Kind() != reflect.Slice && reflectedList.Kind() != reflect.Array) {
		return false
	}
	for index := 0; index < reflectedList.Len(); index++ {
		if templateEqual(value, reflectedList.Index(index).Interface()) {
			return true
		}
	}
	return false
}

func templateEqual(left, right any) bool {
	// SQL IDs are int64 while Go template integer literals are int.
	isInteger := func(value any) bool {
		reflectedValue := reflect.ValueOf(value)
		return reflectedValue.IsValid() && reflectedValue.Kind() >= reflect.Int && reflectedValue.Kind() <= reflect.Uint64
	}
	if isInteger(left) && isInteger(right) {
		return number(left) == number(right)
	}
	return reflect.DeepEqual(left, right)
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
	if booleanValue, ok := value.(bool); ok {
		if booleanValue {
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
	seconds := number(value)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
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
	for _, character := range fmt.Sprint(value) {
		if previousCased {
			result.WriteRune(unicode.ToLower(character))
		} else {
			result.WriteRune(unicode.ToTitle(character))
		}
		previousCased = unicode.IsLower(character) || unicode.IsUpper(character) || unicode.IsTitle(character)
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

func regroup(rows []Row, key string) []Row {
	// Django regroup combines consecutive values; it neither sorts nor merges
	// distant matches. Query ordering therefore determines the displayed groups.
	groups := []Row{}
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].S("grouper") != row.S(key) {
			groups = append(groups, Row{"grouper": row[key], "list": []Row{}})
		}
		group := groups[len(groups)-1]
		group["list"] = append(group["list"].([]Row), row)
	}
	return groups
}
func (store *Store) URL(name string, args ...any) string {
	arg := func(argumentIndex int) string {
		if argumentIndex >= len(args) {
			return ""
		}
		return fmt.Sprint(args[argumentIndex])
	}
	path := name + "/"
	switch name {
	case "index":
		path = ""
	case "login":
		path = "accounts/login/"
	case "music_file_search_page":
		path = "dj/"
	case "music_file_search_api":
		path = "api/music_files/search/"
	case "dj_next_song_api":
		path = "api/dj/next_song/"
	case "download_database":
		path = "preferences/database/"
	case "artist_add_to_tag", "artist_add_to_queue", "artist_add_to_favorites", "search_add_to_tag", "search_add_to_queue", "search_add_to_favorites", "queue_add_to_tag", "queue_add_to_favorites":
		path = strings.Replace(name, "_add_", "/add_", 1) + "/"
	case "ytrip_metadata":
		path = "ytrip/metadata/"
	case "ytrip_failures":
		path = "ytrip/failures/"
	case "retry_ytrip_failure":
		path = "ytrip/failures/" + arg(0) + "/retry/"
	case "rename_tag", "track_details_modal":
		path = name + "/" + arg(0)
	case "artwork", "artwork_modal":
		path = name + "/" + arg(0) + "/" + arg(1) + ".jpg"
	case "shared_tag", "download_shared_tag", "serve_shared_music_file":
		path = "shared/" + arg(0) + "/" + arg(1) + "/"
		if name == "download_shared_tag" {
			path += "download/"
		}
		if name == "serve_shared_music_file" {
			path += "track/" + arg(2) + "/"
		}
	}
	return store.Config.Path(path)
}

func generatedAt() string { return time.Now().UTC().Format("2006-01-02 15:04:05") }
