# Message catalogues

Runtime translations use the vendored `github.com/nicksnyder/go-i18n/v2/i18n`
package. `active.en.json` is the English fallback catalogue; `active.fr.json`
contains the existing French translations. Existing English source strings are
kept as message IDs so template calls and dynamic status messages share the
same catalogue.

To add a language, add `active.<BCP-47-tag>.json`, such as `active.es.json`, and
rebuild the executable. The file is embedded automatically and the language
appears in Preferences. Every catalogue must include its own `language_name`:

```json
{
  "language_name": { "other": "Español" },
  "Language": { "other": "Idioma" },
  "Play %(title)s": { "other": "Reproducir {{.Title}}" }
}
```

Untranslated IDs use the English catalogue. Unknown dynamic strings are returned
unchanged. `language_name` is an autonym, so the selector remains usable even
when the current language is unfamiliar. Existing accounts and anonymous pages
use the server's `LANGUAGE_CODE` until an account saves an override.

Named variables use Go `text/template` syntax. The `%(...)s` wording in a few
legacy message IDs is an identifier only; interpolation uses `{{.Title}}` or
`{{.URL}}` inside their catalogue values. Translators can reorder these variables.
Messages return plain strings and `html/template` escapes the final output.
Keep markup in the HTML templates.

Simple template messages use the existing helpers:

```gotemplate
{{ call .translate_literal "Language" }}
{{ call .translate_format "Play %(title)s" .music_file.title }}
```

For arbitrary variables or plural forms, use `localize` with `dict` and a plural
count (`nil` for a message without plurals):

```gotemplate
{{ call .localize "track_count" (dict "Count" .tag.track_count) .tag.track_count }}
```

The corresponding catalogue message would be:

```json
{
  "track_count": {
    "one": "{{.Count}} track",
    "other": "{{.Count}} tracks"
  }
}
```

Other CLDR plural categories (`zero`, `two`, `few`, `many`) are supported where
the language requires them. Go handlers obtain `app.translator(user)` and use
`Text`, `Format`, or `Message`; do not change a global language during a request.
Date month names, clock formatting, validation messages, and login emails use
the same catalogues. `TIME_ZONE` continues to control time-zone conversion.

The `goi18n extract` tool extracts Go `i18n.Message` declarations, rather than
the string IDs in these HTML templates. Maintain catalogue entries alongside
new UI strings. Tests ensure that literal template messages have an English
entry. Air rebuilds automatically when a catalogue JSON file changes.
