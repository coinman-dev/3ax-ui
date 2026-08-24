package web

import (
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestTemplatesParse is the safety net under getHtmlTemplate, which swallows
// parse errors on purpose (a directory of the tree that holds no templates must
// not stop the server). That leniency means a mistyped action in a page is only
// discovered when somebody opens it.
func TestTemplatesParse(t *testing.T) {
	funcMap := template.FuncMap{
		"i18n": func(key string, params ...string) string { return key },
	}
	dirs := map[string]bool{}
	err := fs.WalkDir(htmlFS, "html", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".html") {
			dirs[path.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the template tree: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("no templates were found at all")
	}

	tpl := template.New("").Funcs(funcMap)
	for dir := range dirs {
		if tpl, err = tpl.ParseFS(htmlFS, dir+"/*.html"); err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
	}
}

// i18nCall matches {{ i18n "some.key" }} and its {{i18n "k" "arg"}} form.
var i18nCall = regexp.MustCompile(`i18n\s+"([^"]+)"`)

// TestTemplateTranslationKeysExist catches the other half of the same problem:
// a key that is not in the bundle renders as the bare key, which looks like a
// typo in the UI rather than a missing translation.
//
// Only en_US is required. go-i18n falls back to it for every other language, so
// a key missing there is a hole with nothing behind it, while a key missing from
// a translation is merely untranslated.
func TestTemplateTranslationKeysExist(t *testing.T) {
	var bundle map[string]any
	raw, err := i18nFS.ReadFile("translation/translate.en_US.toml")
	if err != nil {
		t.Fatalf("read the en_US bundle: %v", err)
	}
	if err := toml.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("parse the en_US bundle: %v", err)
	}
	known := flattenKeys(bundle, "")

	err = fs.WalkDir(htmlFS, "html", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, err := fs.ReadFile(htmlFS, p)
		if err != nil {
			return err
		}
		for _, match := range i18nCall.FindAllStringSubmatch(string(body), -1) {
			key := match[1]
			// The bot bundle lives in the same file under its own prefix and
			// is not addressed from templates.
			if !known[key] {
				t.Errorf("%s uses the translation key %q, which is not in translate.en_US.toml", p, key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the template tree: %v", err)
	}
}

// boundAttr matches a Vue-bound attribute (:foo=, v-bind:foo=, @foo=) whose
// value begins straight with a Go template action.
var boundAttr = regexp.MustCompile(`(?:^|\s)(?::|v-bind:|@)([a-zA-Z][\w.-]*)=("|')\s*\{\{`)

// TestBoundAttributesAreExpressions catches attributes written as
// :title='{{ i18n "some.key" }}'.
//
// The colon makes Vue evaluate the value as JavaScript, and translated text is
// not JavaScript — "Что изменится" parses as an identifier and the whole app
// fails to mount, leaving a blank page with everything still hidden behind
// v-cloak. Nothing else notices: the template parses, the key exists, the page
// returns 200. The panel's own convention is a plain attribute for static text
// (title='{{ i18n "key" }}') or a quoted literal when a binding is really
// wanted (:title="'{{ i18n "key" }}'").
func TestBoundAttributesAreExpressions(t *testing.T) {
	err := fs.WalkDir(htmlFS, "html", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, err := fs.ReadFile(htmlFS, p)
		if err != nil {
			return err
		}
		for _, match := range boundAttr.FindAllStringSubmatch(string(body), -1) {
			t.Errorf("%s: %s is a Vue binding but is given template output directly; "+
				"drop the colon for static text, or wrap it as \"'{{ ... }}'\"", p, match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the template tree: %v", err)
	}
}

// TestTranslationsHaveNoStrayKeys keeps the language files in one shape: a key
// present in a translation but not in en_US is a leftover from a rename, and
// nothing will ever read it.
func TestTranslationsHaveNoStrayKeys(t *testing.T) {
	entries, err := fs.ReadDir(i18nFS, "translation")
	if err != nil {
		t.Fatalf("read the translation directory: %v", err)
	}

	var english map[string]bool
	read := func(name string) map[string]bool {
		raw, err := i18nFS.ReadFile("translation/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var doc map[string]any
		if err := toml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return flattenKeys(doc, "")
	}
	english = read("translate.en_US.toml")

	for _, e := range entries {
		if e.IsDir() || e.Name() == "translate.en_US.toml" {
			continue
		}
		for key := range read(e.Name()) {
			if !english[key] {
				t.Errorf("%s has the key %q, which en_US does not", e.Name(), key)
			}
		}
	}
}

func flattenKeys(doc map[string]any, prefix string) map[string]bool {
	out := map[string]bool{}
	for k, v := range doc {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			for nk := range flattenKeys(nested, key) {
				out[nk] = true
			}
			continue
		}
		out[key] = true
	}
	return out
}
