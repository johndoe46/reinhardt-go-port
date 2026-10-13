package reinhardt

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

type Language struct {
	Code string
	Name string
}

// Translations is initialized once; requests only read its bundle and matcher.
type Translations struct {
	bundle    *i18n.Bundle
	matcher   language.Matcher
	tags      []language.Tag
	Languages []Language
}

func newTranslations(files fs.FS) (*Translations, error) {
	paths, err := fs.Glob(files, "messages/active.*.json")
	if err != nil {
		return nil, err
	}
	bundle := i18n.NewBundle(language.English)
	for _, path := range paths {
		if _, err := bundle.LoadMessageFileFS(files, path); err != nil {
			return nil, fmt.Errorf("load translations %s: %w", path, err)
		}
	}
	// LanguageTags shares the bundle's storage; sorting it would invalidate
	// the bundle's matcher indices when catalogues normalize to another order.
	tags := slices.Clone(bundle.LanguageTags())
	sort.Slice(tags, func(i, j int) bool {
		if tags[i] == tags[j] {
			return false
		}
		if tags[i] == language.English {
			return true
		}
		if tags[j] == language.English {
			return false
		}
		return tags[i].String() < tags[j].String()
	})
	if len(tags) == 0 || tags[0] != language.English {
		return nil, fmt.Errorf("English message catalogue is required")
	}
	translations := &Translations{bundle: bundle, matcher: language.NewMatcher(tags), tags: tags}
	for _, tag := range tags {
		name, matched, err := i18n.NewLocalizer(bundle, tag.String()).LocalizeWithTag(&i18n.LocalizeConfig{MessageID: "language_name"})
		if err != nil || matched != tag || name == "" {
			return nil, fmt.Errorf("catalogue %s must define language_name", tag)
		}
		translations.Languages = append(translations.Languages, Language{Code: tag.String(), Name: name})
	}
	return translations, nil
}

func (translations *Translations) supports(code string) bool {
	for _, lang := range translations.Languages {
		if lang.Code == code {
			return true
		}
	}
	return false
}

type Translator struct {
	Code      string
	localizer *i18n.Localizer
}

func (translations *Translations) forLanguage(code string) *Translator {
	_, index := language.MatchStrings(translations.matcher, code)
	// Use the supported catalogue's tag, rather than a regional extension from
	// the requested tag, for both the localizer and the HTML language attribute.
	matched := translations.tags[index].String()
	return &Translator{Code: matched, localizer: i18n.NewLocalizer(translations.bundle, matched)}
}

// Message supports named template data and CLDR plural counts from catalogues.
func (translator *Translator) Message(id string, data map[string]any, count any) (string, error) {
	text, err := translator.localizer.Localize(&i18n.LocalizeConfig{MessageID: id, TemplateData: data, PluralCount: count})
	var missing *i18n.MessageNotFoundErr
	if text != "" && errors.As(err, &missing) {
		// go-i18n reports missing translations even when English fallback works.
		return text, nil
	}
	return text, err
}

func (translator *Translator) Text(value any) string {
	id := fmt.Sprint(value)
	text, err := translator.Message(id, nil, nil)
	if err != nil && text == "" {
		return id
	}
	return text
}

func (translator *Translator) Format(id string, data map[string]any) (string, error) {
	return translator.Message(id, data, nil)
}

func (app *App) translator(user *User) *Translator {
	code := user.LanguageCode
	if code == "" {
		code = app.Store.Config.LanguageCode
	}
	return app.Translations.forLanguage(code)
}
