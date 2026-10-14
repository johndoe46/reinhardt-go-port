package reinhardt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type usersTUI struct {
	ctx        context.Context
	store      *Store
	app        *tview.Application
	pages      *tview.Pages
	table      *tview.Table
	search     *tview.InputField
	status     *tview.TextView
	users      []*User
	translator *Translator
}

func newUsersTUI(ctx context.Context, store *Store) (*usersTUI, error) {
	translations, err := newTranslations(assets)
	if err != nil {
		return nil, err
	}
	ui := &usersTUI{
		ctx: ctx, store: store, app: tview.NewApplication(), pages: tview.NewPages(),
		table:      tview.NewTable().SetFixed(1, 0).SetSelectable(true, false),
		search:     tview.NewInputField(),
		status:     tview.NewTextView(),
		translator: translations.forLanguage(store.Config.LanguageCode),
	}
	ui.search.SetLabel(" " + ui.text("Search") + ": ")
	ui.table.SetBorder(true).SetTitle(" " + tview.Escape(ui.text("Users")) + " ")
	ui.table.SetSelectedFunc(func(int, int) { ui.editSelected() })
	ui.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ui.app.Stop()
			return nil
		}
		if event.Key() == tcell.KeyTab || event.Rune() == '/' {
			ui.app.SetFocus(ui.search)
			return nil
		}
		switch event.Rune() {
		case 'q':
			ui.app.Stop()
		case 'n':
			ui.edit(User{IsActive: true})
		case 'e':
			ui.editSelected()
		case 'l':
			if user := ui.selected(); user != nil {
				ui.showLink(user, false)
			}
		case 'r':
			ui.confirmReset()
		case 'd':
			ui.confirmDelete()
		case 'f':
			if err := ui.refresh(); err != nil {
				ui.message(ui.errorText(err))
			}
		default:
			return event
		}
		return nil
	})
	ui.search.SetChangedFunc(func(string) { ui.renderUsers(0) })
	ui.search.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEscape {
			ui.search.SetText("")
		}
		ui.app.SetFocus(ui.table)
	})
	header := tview.NewTextView().SetText(ui.text("Reinhardt user management") + "\n" + ui.localize("Database: %(Path)s", map[string]any{"Path": store.Config.DatabasePath}, nil))
	help := tview.NewTextView().SetText(" " + ui.text("↑/↓ select  Enter/e edit  n new  l login link  r reset link") + "\n " + ui.text("/ search  f refresh  d delete account  q/Esc quit  Ctrl-C quit"))
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 2, 0, false).
		AddItem(ui.search, 1, 0, false).
		AddItem(ui.table, 0, 1, true).
		AddItem(ui.status, 1, 0, false).
		AddItem(help, 2, 0, false)
	ui.pages.AddPage("users", main, true, true)
	ui.app.SetRoot(ui.pages, true).SetFocus(ui.table)
	return ui, nil
}

func (ui *usersTUI) text(id string) string { return ui.translator.Text(id) }

func (ui *usersTUI) localize(id string, data map[string]any, count any) string {
	text, err := ui.translator.Message(id, data, count)
	if err != nil {
		return ui.text(id)
	}
	return text
}

func (ui *usersTUI) errorText(err error) string {
	var duplicate duplicateUsernameError
	switch {
	case errors.Is(err, errUsernameRequired):
		return ui.text("username is required")
	case errors.Is(err, errUsernameTooLong):
		return ui.text("username must be at most 150 characters")
	case errors.Is(err, errUserChanged):
		return ui.text("account has changed; refresh the user list before deleting it")
	case errors.Is(err, sql.ErrNoRows):
		return ui.text("Account no longer exists. Refresh the user list.")
	case errors.As(err, &duplicate):
		return ui.localize("username %(Username)s already exists", map[string]any{"Username": fmt.Sprintf("%q", string(duplicate))}, nil)
	default:
		return ui.localize("Operation failed: %(Error)s", map[string]any{"Error": err.Error()}, nil)
	}
}

func (ui *usersTUI) run() error {
	if err := ui.refresh(); err != nil {
		return errors.New(ui.errorText(err))
	}
	if err := ui.app.Run(); err != nil {
		return errors.New(ui.errorText(err))
	}
	return nil
}

func (ui *usersTUI) selected() *User {
	row, _ := ui.table.GetSelection()
	if row < 1 || row >= ui.table.GetRowCount() {
		return nil
	}
	user, _ := ui.table.GetCell(row, 0).GetReference().(*User)
	return user
}

func (ui *usersTUI) refresh() error {
	var selectedID int64
	if user := ui.selected(); user != nil {
		selectedID = user.ID
	}
	rows, err := ui.store.Query(ui.ctx, "SELECT * FROM auth_user ORDER BY username COLLATE NOCASE,id")
	if err != nil {
		return err
	}
	ui.users = make([]*User, 0, len(rows))
	for _, row := range rows {
		ui.users = append(ui.users, userFromRow(row))
	}
	ui.renderUsers(selectedID)
	return nil
}

func (ui *usersTUI) renderUsers(selectedID int64) {
	ui.table.Clear()
	for column, title := range []string{ui.text("Username"), ui.text("Email"), ui.text("Active"), ui.text("Administrator")} {
		ui.table.SetCell(0, column, tview.NewTableCell(tview.Escape(title)).SetSelectable(false).SetTextColor(tcell.ColorYellow))
	}
	filter := strings.ToLower(ui.search.GetText())
	selectedRow, row := 1, 1
	for _, user := range ui.users {
		if !strings.Contains(strings.ToLower(user.Username+" "+user.Email), filter) {
			continue
		}
		active, admin := ui.text("no"), ui.text("no")
		if user.IsActive {
			active = ui.text("yes")
		}
		if user.IsSuperuser {
			admin = ui.text("yes")
		}
		for column, value := range []string{user.Username, user.Email, active, admin} {
			ui.table.SetCell(row, column, tview.NewTableCell(tview.Escape(value)).SetReference(user).SetExpansion(1))
		}
		if user.ID == selectedID {
			selectedRow = row
		}
		row++
	}
	ui.table.Select(selectedRow, 0)
	ui.status.SetText(" " + ui.localize("%(Count)d of %(Total)d users. Press n to create an account.", map[string]any{"Count": row - 1, "Total": len(ui.users)}, len(ui.users)))
}

func (ui *usersTUI) editSelected() {
	if user := ui.selected(); user != nil {
		ui.edit(*user)
	}
}

func (ui *usersTUI) edit(user User) {
	title := ui.text("New user")
	if user.ID != 0 {
		title = ui.localize("Edit %(Username)s", map[string]any{"Username": user.Username}, nil)
	}
	form := tview.NewForm().
		AddInputField(ui.text("Username"), user.Username, 36, nil, func(value string) { user.Username = value }).
		AddInputField(ui.text("Email"), user.Email, 36, nil, func(value string) { user.Email = value }).
		AddInputField(ui.text("First name"), user.FirstName, 36, nil, func(value string) { user.FirstName = value }).
		AddInputField(ui.text("Last name"), user.LastName, 36, nil, func(value string) { user.LastName = value }).
		AddCheckbox(ui.text("Active"), user.IsActive, func(value bool) { user.IsActive = value }).
		AddCheckbox(ui.text("Administrator"), user.IsSuperuser, func(value bool) { user.IsSuperuser = value })
	form.SetBorder(true).SetTitle(" " + tview.Escape(title) + " ")
	form.AddButton(ui.text("Save"), func() {
		id, err := ui.store.saveManagedUser(ui.ctx, user)
		if err != nil {
			ui.message(ui.errorText(err))
			return
		}
		ui.closeDialog()
		// Clear filtering so a newly created or renamed user remains visible.
		ui.search.SetText("")
		if err := ui.refresh(); err != nil {
			ui.message(ui.errorText(err))
			return
		}
		ui.renderUsers(id)
		ui.status.SetText(" " + ui.text("Account saved. Press l to view its login link."))
	})
	form.AddButton(ui.text("Cancel"), ui.closeDialog)
	form.SetCancelFunc(ui.closeDialog)
	note := tview.NewTextView().SetText(" " + ui.text("Tab/Shift-Tab move · Space toggles · Esc cancels") + "\n " + ui.text("Inactive accounts cannot sign in; saving signs them out."))
	dialog := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).AddItem(note, 2, 0, false)
	ui.pages.AddPage("dialog", centerUsersDialog(dialog, 66, 23), true, true)
	ui.app.SetFocus(form)
}

func (ui *usersTUI) closeDialog() {
	ui.pages.RemovePage("dialog")
	ui.app.SetFocus(ui.table)
}

func (ui *usersTUI) message(text string) {
	focus := ui.app.GetFocus()
	closeMessage := func() {
		ui.pages.RemovePage("message")
		ui.app.SetFocus(focus)
	}
	modal := tview.NewModal().SetText(tview.Escape(text)).AddButtons([]string{ui.text("Close")})
	modal.SetDoneFunc(func(int, string) { closeMessage() })
	modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closeMessage()
			return nil
		}
		return event
	})
	ui.pages.AddPage("message", modal, true, true)
	ui.app.SetFocus(modal)
}

func (ui *usersTUI) showLink(user *User, reset bool) {
	link, err := ui.store.userAccessURL(ui.ctx, user.ID, reset)
	if err != nil {
		ui.message(ui.errorText(err))
		return
	}
	text := ui.localize("Login link for %(Username)s:\n\n%(URL)s\n\nShare this link privately.", map[string]any{"Username": user.Username, "URL": link}, nil)
	if reset {
		text += "\n" + ui.text("The previous link and all existing sessions have been revoked.")
	}
	if !user.IsActive {
		text += "\n" + ui.text("This account is inactive. Enable Active before using the link.")
	}
	ui.message(text)
}

func (ui *usersTUI) confirmReset() {
	user := ui.selected()
	if user == nil {
		return
	}
	modal := tview.NewModal().
		SetText(tview.Escape(ui.localize("Reset the login link for %(Username)s?\n\nThe previous link will stop working and all current sessions will be signed out.", map[string]any{"Username": user.Username}, nil))).
		AddButtons([]string{ui.text("Cancel"), ui.text("Reset link")})
	modal.SetDoneFunc(func(index int, _ string) {
		ui.closeDialog()
		if index == 1 {
			ui.showLink(user, true)
		}
	})
	modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ui.closeDialog()
			return nil
		}
		return event
	})
	ui.pages.AddPage("dialog", modal, true, true)
	ui.app.SetFocus(modal)
}

func (ui *usersTUI) confirmDelete() {
	selected := ui.selected()
	if selected == nil {
		return
	}
	user := *selected
	confirmation := tview.NewInputField().SetLabel(ui.text("Username")).SetFieldWidth(36)
	form := tview.NewForm().AddFormItem(confirmation).
		AddButton(ui.text("Cancel"), ui.closeDialog).
		AddButton(ui.text("Delete account"), func() {
			if confirmation.GetText() != user.Username {
				return
			}
			if err := ui.store.deleteManagedUser(ui.ctx, user.ID, user.Username); err != nil {
				ui.message(ui.errorText(err))
				return
			}
			ui.closeDialog()
			if err := ui.refresh(); err != nil {
				ui.message(ui.localize("Account deleted, but refreshing the user list failed: %(Error)s", map[string]any{"Error": ui.errorText(err)}, nil))
				return
			}
			ui.status.SetText(" " + ui.localize("Deleted account %(Username)s.", map[string]any{"Username": user.Username}, nil))
		})
	form.SetBorder(true).SetTitle(" " + tview.Escape(ui.text("Confirm permanent deletion")) + " ").SetBorderColor(tcell.ColorRed)
	form.SetCancelFunc(ui.closeDialog)
	deleteButton := form.GetButton(1).SetDisabled(true)
	confirmation.SetChangedFunc(func(value string) { deleteButton.SetDisabled(value != user.Username) })
	warning := tview.NewTextView().SetText(ui.localize("Permanently delete %(Username)s?", map[string]any{"Username": user.Username}, nil) + "\n\n" + ui.text("This removes the account, login link, sessions, preferences, queue, favorites, and playlists. This cannot be undone.") + "\n\n" + ui.text("Type the exact username below to confirm."))
	dialog := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(warning, 8, 0, false).AddItem(form, 0, 1, true)
	ui.pages.AddPage("dialog", centerUsersDialog(dialog, 66, 16), true, true)
	ui.app.SetFocus(form)
}

func centerUsersDialog(dialog tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(dialog, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}
