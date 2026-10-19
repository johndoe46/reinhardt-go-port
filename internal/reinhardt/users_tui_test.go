package reinhardt

import (
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/rivo/tview"
)

func TestUsersTUIKeyboardWorkflow(test *testing.T) {
	for _, code := range []string{"en-us", "fr-fr", "zz"} {
		test.Run(code, func(test *testing.T) { testUsersTUIKeyboardWorkflow(test, code) })
	}
}

func testUsersTUIKeyboardWorkflow(test *testing.T, code string) {
	labels := map[string]string{
		"heading": "Reinhardt user management", "admin": "Administrator", "save": "Save", "cancel": "Cancel",
		"private": "Share this link privately", "duplicate": "already", "irreversible": "cannot be undone", "delete": "Delete account",
		"deleted": "Deleted account alice.", "reset": "Reset link",
	}
	if code == "fr-fr" {
		labels = map[string]string{
			"heading": "Gestion des utilisateurs de Reinhardt", "admin": "Administrateur", "save": "Enregistrer", "cancel": "Annuler",
			"private": "Partagez ce lien en privé", "duplicate": "existe déjà", "irreversible": "irréversible", "delete": "Supprimer le compte",
			"deleted": "Compte alice supprimé.", "reset": "Réinitialiser le lien",
		}
	}
	app := testApp(test)
	app.Store.Config.LanguageCode = code
	accountLanguage := "fr"
	if code == "fr-fr" {
		accountLanguage = "en"
	}
	if err := app.Store.SetUserLanguage(test.Context(), 1, accountLanguage); err != nil {
		test.Fatal(err)
	}
	ui, err := newUsersTUI(test.Context(), app.Store)
	if err != nil {
		test.Fatal(err)
	}
	if err := ui.refresh(); err != nil {
		test.Fatal(err)
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		test.Fatal(err)
	}
	test.Cleanup(screen.Fini)
	screen.SetSize(80, 24)
	ui.pages.SetRect(0, 0, 80, 24)
	draw := func() string {
		ui.pages.Draw(screen)
		screen.Show()
		contents, width, _ := screen.GetContents()
		var text strings.Builder
		for index, cell := range contents {
			if index%width == 0 {
				text.WriteByte('\n')
			}
			if len(cell.Runes) > 0 {
				text.WriteRune(cell.Runes[0])
			} else {
				text.WriteByte(' ')
			}
		}
		return text.String()
	}
	key := func(key tcell.Key, character rune) {
		ui.pages.InputHandler()(tcell.NewEventKey(key, character, tcell.ModNone), func(primitive tview.Primitive) { ui.app.SetFocus(primitive) })
		draw()
	}
	typeText := func(text string) {
		for _, character := range text {
			key(tcell.KeyRune, character)
		}
	}
	initial := draw()
	if !strings.Contains(initial, "alice") || !strings.Contains(initial, "bob") || !strings.Contains(initial, labels["admin"]) || !strings.Contains(initial, labels["heading"]) {
		test.Fatalf("user list missing fields: %s", initial)
	}
	key(tcell.KeyRune, 'n')
	typeText("charlie")
	key(tcell.KeyTab, 0)
	typeText("charlie@example.com")
	for range 4 {
		key(tcell.KeyTab, 0)
	}
	key(tcell.KeyRune, ' ')
	key(tcell.KeyTab, 0)
	form := draw()
	if !strings.Contains(form, labels["save"]) || !strings.Contains(form, labels["cancel"]) || !strings.Contains(form, "charlie@example.com") {
		test.Fatalf("create form missing fields: %s", form)
	}
	key(tcell.KeyEnter, 0)
	user := ui.selected()
	if user == nil || user.Username != "charlie" || !user.IsActive || !user.IsSuperuser || user.Email != "charlie@example.com" {
		test.Fatalf("keyboard creation failed: %+v; screen:%s", user, draw())
	}
	test.Logf("user list after creating an account:%s", draw())
	link, err := app.Store.userAccessURL(test.Context(), user.ID, false)
	if err != nil {
		test.Fatal(err)
	}
	key(tcell.KeyRune, 'l')
	if !strings.Contains(draw(), labels["private"]) {
		test.Fatalf("login link dialog missing: %s", draw())
	}
	key(tcell.KeyEscape, 0)
	key(tcell.KeyRune, 'r')
	if !strings.Contains(draw(), labels["reset"]) {
		test.Fatalf("reset confirmation not translated: %s", draw())
	}
	key(tcell.KeyEnter, 0) // Cancel is the default.
	if after, err := app.Store.userAccessURL(test.Context(), user.ID, false); err != nil || after != link {
		test.Fatalf("cancelled reset changed link=%q, err=%v", after, err)
	}
	key(tcell.KeyRune, 'r')
	key(tcell.KeyTab, 0)
	key(tcell.KeyEnter, 0)
	if after, err := app.Store.userAccessURL(test.Context(), user.ID, false); err != nil || after == link {
		test.Fatalf("confirmed reset did not change link=%q, err=%v", after, err)
	}
	key(tcell.KeyEscape, 0)
	key(tcell.KeyEnter, 0)
	key(tcell.KeyCtrlU, 0)
	typeText("alice")
	for range 6 {
		key(tcell.KeyTab, 0)
	}
	key(tcell.KeyEnter, 0)
	if !strings.Contains(draw(), labels["duplicate"]) {
		test.Fatalf("duplicate edit error missing: %s", draw())
	}
	key(tcell.KeyEscape, 0) // Dismiss error, returning to the edit form.
	key(tcell.KeyEscape, 0) // Cancel the unsaved edit.
	key(tcell.KeyRune, '/')
	typeText("BOB@EXAMPLE")
	key(tcell.KeyEnter, 0)
	if user := ui.selected(); user == nil || user.Username != "bob" || ui.table.GetRowCount() != 2 {
		test.Fatalf("email search failed: %+v", user)
	}
	key(tcell.KeyRune, '/')
	key(tcell.KeyCtrlU, 0)
	typeText("no matches")
	key(tcell.KeyEnter, 0)
	key(tcell.KeyRune, 'e')
	key(tcell.KeyRune, 'l')
	key(tcell.KeyRune, 'r')
	key(tcell.KeyRune, 'd')
	if ui.selected() != nil || ui.pages.HasPage("dialog") || ui.pages.HasPage("message") {
		test.Fatal("empty list allowed an account action")
	}
	key(tcell.KeyRune, '/')
	key(tcell.KeyEscape, 0) // Clear search, returning to alice.
	key(tcell.KeyRune, 'd')
	if text := draw(); !strings.Contains(text, labels["irreversible"]) || !strings.Contains(text, labels["delete"]) {
		test.Fatalf("permanent deletion confirmation missing: %s", text)
	}
	typeText("ALICE") // The confirmation must match exactly.
	key(tcell.KeyTab, 0)
	key(tcell.KeyTab, 0)
	key(tcell.KeyEnter, 0)
	if !ui.pages.HasPage("dialog") || len(ui.users) != 3 {
		test.Fatal("incorrect username allowed deletion")
	}
	key(tcell.KeyEscape, 0)
	key(tcell.KeyRune, 'd')
	typeText("alice")
	key(tcell.KeyEnter, 0)
	key(tcell.KeyEnter, 0) // Cancel remains the default even after confirmation.
	if ui.pages.HasPage("dialog") || len(ui.users) != 3 {
		test.Fatal("default confirmation action deleted the account")
	}
	key(tcell.KeyRune, '/')
	typeText("alice")
	key(tcell.KeyEnter, 0)
	key(tcell.KeyRune, 'd')
	typeText("alice")
	key(tcell.KeyTab, 0)
	key(tcell.KeyTab, 0)
	key(tcell.KeyEnter, 0)
	if ui.pages.HasPage("dialog") || ui.pages.HasPage("message") || len(ui.users) != 2 || ui.selected() != nil || ui.search.GetText() != "alice" {
		test.Fatalf("deletion did not refresh the filtered list: %s", draw())
	}
	if !strings.Contains(draw(), labels["deleted"]) {
		test.Fatalf("deletion status not translated: %s", draw())
	}
	if row, err := app.Store.One(test.Context(), "SELECT count(*) AS count FROM auth_user WHERE id=1"); err != nil || row.N("count") != 0 {
		test.Fatalf("confirmed deletion left the account: %v, %v", row, err)
	}
	key(tcell.KeyRune, 'd') // The filtered list is now empty.
	key(tcell.KeyRune, '/')
	key(tcell.KeyEscape, 0)
	if user := ui.selected(); user == nil || user.Username != "bob" {
		test.Fatalf("cannot select another account after deletion: %+v", user)
	}
}

func TestTUICatalogueMessages(test *testing.T) {
	source, err := parser.ParseFile(gotoken.NewFileSet(), "users_tui.go", nil, 0)
	if err != nil {
		test.Fatal(err)
	}
	translations, err := newTranslations(assets)
	if err != nil {
		test.Fatal(err)
	}
	data := map[string]any{"Username": "Élodie [red]", "Path": "/tmp/music.sqlite3", "URL": "https://example.com/invite/?q=token", "Error": "test error", "Count": 1, "Total": 2}
	ast.Inspect(source, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (method.Sel.Name != "text" && method.Sel.Name != "localize") {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != gotoken.STRING {
			return true
		}
		id, err := strconv.Unquote(literal.Value)
		if err != nil {
			test.Fatal(err)
		}
		counts := []any{nil}
		if method.Sel.Name == "localize" && len(call.Args) == 3 {
			if argument, ok := call.Args[2].(*ast.Ident); !ok || argument.Name != "nil" {
				counts = []any{0, 1, 2}
			}
		}
		for _, code := range []string{"en", "fr"} {
			for _, count := range counts {
				text, matched, err := translations.forLanguage(code).localizer.LocalizeWithTag(&i18n.LocalizeConfig{MessageID: id, TemplateData: data, PluralCount: count})
				if err != nil || matched.String() != code || text == "" || strings.Contains(text, "<no value>") {
					test.Errorf("%s message %q, count %v: %q (%s), err=%v", code, id, count, text, matched, err)
				}
			}
		}
		return true
	})
}

func TestTUIValidationTranslationsAndUserCounts(test *testing.T) {
	app := testApp(test)
	app.Store.Config.LanguageCode = "fr-FR"
	ui, err := newUsersTUI(test.Context(), app.Store)
	if err != nil {
		test.Fatal(err)
	}
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{errUsernameRequired, "le nom d’utilisateur est obligatoire"},
		{errUsernameTooLong, "le nom d’utilisateur ne doit pas dépasser 150 caractères"},
		{errUserChanged, "le compte a changé ; actualisez la liste avant de le supprimer"},
		{sql.ErrNoRows, "Ce compte n’existe plus. Actualisez la liste des utilisateurs."},
		{duplicateUsernameError("Élodie [red]"), "le nom d’utilisateur \"Élodie [red]\" existe déjà"},
		{errors.New("SQL detail"), "Échec de l’opération : SQL detail"},
	} {
		if got := ui.errorText(scenario.err); got != scenario.want {
			test.Errorf("error %v: %q, want %q", scenario.err, got, scenario.want)
		}
	}
	if err := ui.refresh(); err != nil {
		test.Fatal(err)
	}
	ui.search.SetText("alice")
	if got := ui.status.GetText(false); got != " 1 sur 2 utilisateurs. Appuyez sur n pour créer un compte." {
		test.Fatal("filtered plural count:", got)
	}
	ui.users = ui.users[:1]
	ui.renderUsers(0)
	if got := ui.status.GetText(false); got != " 1 sur 1 utilisateur. Appuyez sur n pour créer un compte." {
		test.Fatal("singular count:", got)
	}
}
