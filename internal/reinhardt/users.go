package reinhardt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	errUsernameRequired = errors.New("username is required")
	errUsernameTooLong  = errors.New("username must be at most 150 characters")
	errUserChanged      = errors.New("account has changed; refresh the user list before deleting it")
)

type duplicateUsernameError string

func (username duplicateUsernameError) Error() string {
	return fmt.Sprintf("username %q already exists", string(username))
}

func normalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "", errUsernameRequired
	}
	if utf8.RuneCountInString(username) > 150 {
		return "", errUsernameTooLong
	}
	return username, nil
}

// saveManagedUser keeps account changes and session revocation atomic. An ID of
// zero creates a new account; updates preserve its preferences and login link.
func (store *Store) saveManagedUser(ctx context.Context, user User) (int64, error) {
	username, err := normalizeUsername(user.Username)
	if err != nil {
		return 0, err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var duplicate bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM auth_user WHERE username=? AND id<>?)", username, user.ID).Scan(&duplicate); err != nil {
		return 0, err
	}
	if duplicate {
		return 0, duplicateUsernameError(username)
	}
	email := strings.TrimSpace(user.Email)
	if user.ID == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO auth_user(password,last_login,username,email,first_name,last_name,is_superuser,is_staff,is_active,date_joined) VALUES('!',NULL,?,?,?,?,?,?,?,?)`, username, email, user.FirstName, user.LastName, user.IsSuperuser, user.IsSuperuser, user.IsActive, now())
		if err != nil {
			return 0, err
		}
		user.ID, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,show_track_grid,current_position,access_token) VALUES(?,1,1,1,1,1,0,?)`, user.ID, token()); err != nil {
			return 0, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE auth_user SET username=?,email=?,first_name=?,last_name=?,is_superuser=?,is_staff=?,is_active=? WHERE id=?`, username, email, user.FirstName, user.LastName, user.IsSuperuser, user.IsSuperuser, user.IsActive, user.ID)
		if err != nil {
			return 0, err
		}
		if count, err := result.RowsAffected(); err != nil {
			return 0, err
		} else if count == 0 {
			return 0, sql.ErrNoRows
		}
	}
	if !user.IsActive {
		if _, err = tx.ExecContext(ctx, "DELETE FROM go_session WHERE owner_id=?", user.ID); err != nil {
			return 0, err
		}
	}
	return user.ID, tx.Commit()
}

// deleteManagedUser explicitly removes owned rows because Django-created
// databases rely on application-level cascading. Match the confirmed username
// as well as the ID so a stale selection cannot delete a different account.
func (store *Store) deleteManagedUser(ctx context.Context, owner int64, username string) error {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentUsername string
	if err = tx.QueryRowContext(ctx, "SELECT username FROM auth_user WHERE id=?", owner).Scan(&currentUsername); err != nil {
		return err
	}
	if currentUsername != username {
		return errUserChanged
	}
	for _, query := range []string{
		"DELETE FROM reinhardt_musicfiletag WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=?)",
		"DELETE FROM go_autofeed_tag_revision WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=?)",
		"DELETE FROM reinhardt_tag WHERE owner_id=?",
		"DELETE FROM reinhardt_preferences WHERE owner_id=?",
		"DELETE FROM go_session WHERE owner_id=?",
	} {
		if _, err = tx.ExecContext(ctx, query, owner); err != nil {
			return err
		}
	}
	// These Django tables are absent from new installations, but their foreign
	// keys may still refer to accounts in imported databases.
	for _, table := range []string{"auth_user_groups", "auth_user_user_permissions", "django_admin_log"} {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists); err != nil {
			return err
		}
		if exists {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id=?", owner); err != nil {
				return err
			}
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM auth_user WHERE id=?", owner)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// userAccessURL initializes missing preferences without changing an existing
// token. Resetting a link also signs out sessions established with the old link.
func (store *Store) userAccessURL(ctx context.Context, owner int64, reset bool) (string, error) {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var access string
	err = tx.QueryRowContext(ctx, `INSERT INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,show_track_grid,current_position,access_token) VALUES(?,1,1,1,1,1,0,?)
ON CONFLICT(owner_id) DO UPDATE SET access_token=CASE WHEN access_token IS NULL OR access_token='' OR ? THEN excluded.access_token ELSE access_token END
RETURNING access_token`, owner, token(), reset).Scan(&access)
	if err != nil {
		return "", err
	}
	if reset {
		if _, err = tx.ExecContext(ctx, "DELETE FROM go_session WHERE owner_id=?", owner); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return store.Config.AccessURLPrefix + store.URL("invite") + "?q=" + access, nil
}
