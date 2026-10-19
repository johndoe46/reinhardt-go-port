package reinhardt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

// Row holds dynamic SQL projections or template/API data. Application domain
// records use the typed models in models.go.
type Row map[string]any

func (row Row) String() string {
	if labelValue, ok := row["label"]; ok {
		return fmt.Sprint(labelValue)
	}
	if artistValue, ok := row["artist"]; ok {
		return fmt.Sprint(artistValue) + " - " + fmt.Sprint(row["title"])
	}
	return fmt.Sprint(row["username"])
}
func (row Row) S(key string) string {
	if row[key] == nil {
		return ""
	}
	return fmt.Sprint(row[key])
}

// N reads a numeric projection field, defaulting missing or invalid values to
// zero. Call number directly when conversion errors must be reported.
func (row Row) N(key string) int64 {
	value, err := number(row[key])
	if err != nil {
		return 0
	}
	return value
}
func (row Row) B(key string) bool {
	if booleanValue, ok := row[key].(bool); ok {
		return booleanValue
	}
	return row.N(key) != 0
}

// number converts integer-valued inputs without formatting native numbers as
// strings. Nil represents an absent value; other invalid inputs return an error.
func number(value any) (int64, error) {
	switch value := value.(type) {
	case nil:
		return 0, nil
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case string:
		return strconv.ParseInt(value, 10, 64)
	case json.Number:
		return value.Int64()
	}
	// Reflection also handles named numeric types used in template arguments.
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		integer := reflected.Uint()
		if integer <= math.MaxInt64 {
			return int64(integer), nil
		}
		return 0, fmt.Errorf("integer of type %T exceeds int64 range", value)
	case reflect.Float32, reflect.Float64:
		integer := reflected.Float()
		// The upper bound is exclusive: float64(MaxInt64) rounds up to 2^63.
		if !math.IsNaN(integer) && integer >= -0x1p63 && integer < 0x1p63 && math.Trunc(integer) == integer {
			return int64(integer), nil
		}
		return 0, fmt.Errorf("value of type %T is not an integer within int64 range", value)
	case reflect.String:
		return strconv.ParseInt(reflected.String(), 10, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to an integer", value)
}
func token() string {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(randomBytes)
}
func now() string              { return time.Now().UTC().Format("2006-01-02 15:04:05.000000") }
func label(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
func slug(text string) string {
	var builder strings.Builder
	for _, character := range norm.NFKD.String(strings.ToLower(text)) {
		if character < 128 && (unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_') {
			builder.WriteRune(character)
		} else if unicode.IsSpace(character) || character == '-' {
			builder.WriteByte('-')
		}
	}
	text = builder.String()
	for strings.Contains(text, "--") {
		text = strings.ReplaceAll(text, "--", "-")
	}
	return strings.Trim(text, "-_")
}

type Store struct {
	DB     *sql.DB
	Config *Config
}

var errNoMusicDirectory = errors.New("no music directory is registered")

func (store *Store) DefaultMusicDirectory(ctx context.Context) (*MusicDirectory, error) {
	dir, err := store.musicDirectory(ctx, "SELECT * FROM reinhardt_musicdirectory ORDER BY id LIMIT 1")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w in database %q; run 'reinhardt addmusicdir /path/to/music' using this database", errNoMusicDirectory, store.Config.DatabasePath)
	}
	if err != nil {
		return nil, fmt.Errorf("select music directory in database %q: %w", store.Config.DatabasePath, err)
	}
	return dir, nil
}

func openStore(ctx context.Context, config *Config) (*Store, error) {
	path := config.DatabasePath
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	dsn := path
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// Put connection-local pragmas in the DSN so replacement connections also
	// enforce foreign keys and wait for locks held by a worker or external process.
	db, err := sql.Open("sqlite", dsn+sep+"_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// :memory: databases belong to individual connections. The single connection
	// also serializes this process's SQL, but multi-statement changes still need a
	// transaction. Close result sets before another query: otherwise that query
	// waits for the connection still held by the first set of rows.
	db.SetMaxOpenConns(1)
	// The bootstrap is additive: existing tables and rows stay in place;
	// missing tables, indexes, and cache revisions are added automatically.
	schema, _ := assets.ReadFile("schema.sql")
	if _, err = db.ExecContext(ctx, string(schema)); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateLanguagePreference(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate language preference: %w", err)
	}
	store := &Store{db, config}
	if err = store.seedRadioStations(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize radio stations: %w", err)
	}
	if config.SecretKey == "" {
		// Persist the fallback key so shared-playlist hashes survive restarts.
		// INSERT OR IGNORE lets simultaneous startup processes agree on one key.
		var cSecret string
		_, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO go_settings(key,value) VALUES('secret_key',?)", token())
		if err == nil {
			err = db.QueryRowContext(ctx, "SELECT value FROM go_settings WHERE key='secret_key'").Scan(&cSecret)
		}
		if err != nil {
			db.Close()
			return nil, err
		}
		config.SecretKey = cSecret
	}
	return store, nil
}

// Upgrade existing preferences and move language choices from the initial Go
// implementation. The write lock serializes upgrades by server/worker processes.
func migrateLanguagePreference(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// A cancelled migration must still roll back before returning its
		// connection to the pool. Discard it if cleanup cannot complete.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanupCtx, "ROLLBACK"); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var name string
	err = conn.QueryRowContext(ctx, "SELECT name FROM pragma_table_info('reinhardt_preferences') WHERE name='language_code'").Scan(&name)
	if err == sql.ErrNoRows {
		_, err = conn.ExecContext(ctx, "ALTER TABLE reinhardt_preferences ADD COLUMN language_code TEXT")
	}
	if err != nil {
		return err
	}
	var legacyTable bool
	if err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='go_user_language')").Scan(&legacyTable); err != nil {
		return err
	}
	if legacyTable {
		// Explicit defaults also support existing tables whose required columns
		// have application-level defaults rather than SQLite defaults.
		if _, err = conn.ExecContext(ctx, `INSERT INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,current_position,access_token,language_code)
SELECT owner_id,1,1,1,1,0,NULL,language_code FROM go_user_language WHERE 1
ON CONFLICT(owner_id) DO UPDATE SET language_code=excluded.language_code
WHERE reinhardt_preferences.language_code IS NULL OR reinhardt_preferences.language_code=''`); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "DROP TABLE go_user_language"); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	committed = err == nil
	return err
}

func scanRows(rows *sql.Rows) ([]Row, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []Row{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for columnIndex := range values {
			ptrs[columnIndex] = &values[columnIndex]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := Row{}
		for columnIndex, columnName := range cols {
			columnValue := values[columnIndex]
			if timestamp, ok := columnValue.(time.Time); ok {
				// The SQLite driver decodes DATETIME columns; templates and the API
				// expect ISO 8601 UTC strings with a numeric timezone offset.
				format := "2006-01-02T15:04:05"
				if timestamp.Nanosecond() != 0 {
					format += ".000000"
				}
				columnValue = timestamp.UTC().Format(format) + "+00:00"
			}
			row[columnName] = columnValue
		}
		row["pk"] = row["id"]
		result = append(result, row)
	}
	return result, rows.Err()
}
func (store *Store) Query(ctx context.Context, query string, args ...any) ([]Row, error) {
	rows, err := store.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}
func (store *Store) One(ctx context.Context, query string, args ...any) (Row, error) {
	rows, err := store.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}
func (store *Store) Preferences(ctx context.Context, owner int64) (*Preferences, error) {
	// Even ignored AUTOINCREMENT inserts can write SQLite's sequence. Read first
	// so rendering an initialized account does not acquire a write transaction.
	prefs, err := store.One(ctx, "SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
	if err != sql.ErrNoRows {
		return preferencesFromRow(prefs), err
	}
	_, err = store.DB.ExecContext(ctx, `INSERT OR IGNORE INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,current_position,access_token) VALUES(?,1,1,1,1,0,NULL)`, owner)
	if err != nil {
		return nil, err
	}
	row, err := store.One(ctx, "SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
	return preferencesFromRow(row), err
}

func (store *Store) SetUserLanguage(ctx context.Context, owner int64, code string) error {
	if _, err := store.Preferences(ctx, owner); err != nil {
		return err
	}
	var preference any
	if code != "" {
		preference = code
	}
	_, err := store.DB.ExecContext(ctx, "UPDATE reinhardt_preferences SET language_code=? WHERE owner_id=?", preference, owner)
	return err
}

const tracksSQL = `SELECT m.*,d.local_path FROM reinhardt_musicfile m JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id `

func (store *Store) Tracks(ctx context.Context, clause string, args ...any) ([]*Track, error) {
	rows, err := store.Query(ctx, tracksSQL+clause, args...)
	if err != nil {
		return nil, err
	}
	tracks := tracksFromRows(rows)
	for _, track := range tracks {
		store.decorateTrack(track)
	}
	return tracks, nil
}
func (store *Store) Track(ctx context.Context, id any) (*Track, error) {
	rs, err := store.Tracks(ctx, "WHERE m.id=?", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, sql.ErrNoRows
	}
	return rs[0], nil
}
func (store *Store) decorateTrack(track *Track) {
	track.FullPath = filepath.Join(track.LocalPath, track.RelativePath)
	name := []rune(track.Artist + " - " + track.Title)
	if len(name) > 55 {
		name = name[:55]
	}
	track.CleanFileName = slug(string(name)+" - "+fmt.Sprint(track.ID)) + filepath.Ext(track.RelativePath)
	track.DownloadURL = store.Config.Path("download/" + fmt.Sprint(track.ID) + "/" + track.CleanFileName)
	if store.Config.DelegateDownloadPrefix != "" {
		track.DownloadURL = strings.TrimRight(store.Config.DelegateDownloadPrefix, "/") + "/" + track.RelativePath
	}
	track.PlaybackURL = store.Config.Path("stream/" + fmt.Sprint(track.ID) + "/")
}
func (store *Store) Tags(ctx context.Context, owner int64, reserved bool) ([]*Tag, error) {
	clause := ""
	if !reserved {
		clause = " AND t.label NOT IN ('queue','favorites')"
	}
	rs, err := store.Query(ctx, `SELECT t.*,COUNT(a.id) track_count,COALESCE(SUM(m.duration),0) total_duration FROM reinhardt_tag t LEFT JOIN reinhardt_musicfiletag a ON a.tag_id=t.id LEFT JOIN reinhardt_musicfile m ON m.id=a.music_file_id WHERE t.owner_id=?`+clause+` GROUP BY t.id ORDER BY t.label`, owner)
	tags := make([]*Tag, 0, len(rs))
	for _, row := range rs {
		tag := tagFromRow(row)
		store.decorateTag(tag)
		tags = append(tags, tag)
	}
	return tags, err
}
func (store *Store) decorateTag(tag *Tag) {
	digest := sha256.Sum256([]byte(fmt.Sprint(tag.ID) + "-" + store.Config.SecretKey))
	tag.ShareHash = hex.EncodeToString(digest[:])
	tag.ShareURL = store.Config.Path("shared/" + fmt.Sprint(tag.ID) + "/" + tag.ShareHash + "/")
	tag.Duration = tag.TotalDuration
}
func (store *Store) Tag(ctx context.Context, id any, owner int64) (*Tag, error) {
	tag, err := store.tag(ctx, "SELECT * FROM reinhardt_tag WHERE id=? AND owner_id=?", id, owner)
	if err == nil {
		store.decorateTag(tag)
	}
	return tag, err
}
func (store *Store) TagTracks(ctx context.Context, id any) ([]*Track, error) {
	return store.Tracks(ctx, "JOIN reinhardt_musicfiletag a ON a.music_file_id=m.id WHERE a.tag_id=? AND m.missing=0 ORDER BY a.rank,a.id", id)
}
func (store *Store) Queue(ctx context.Context, owner int64) ([]*QueueItem, error) {
	tracks, err := store.Query(ctx, `SELECT m.*,d.local_path,
a.id AS queue_id,a.tag_id AS queue_tag_id,a.rank AS queue_rank,
a.created_at AS queue_created_at,a.modified_at AS queue_modified_at
FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
JOIN reinhardt_musicfile m ON m.id=a.music_file_id
JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id
WHERE t.owner_id=? AND t.label='queue' ORDER BY a.rank,a.id`, owner)
	if err != nil {
		return nil, err
	}
	items := make([]*QueueItem, 0, len(tracks))
	for _, row := range tracks {
		track := trackFromRow(row)
		store.decorateTrack(track)
		items = append(items, &QueueItem{
			ID: row.N("queue_id"), TagID: row.N("queue_tag_id"),
			MusicFileID: track.ID, Rank: row.N("queue_rank"),
			CreatedAt: row.S("queue_created_at"), ModifiedAt: row.S("queue_modified_at"),
			MusicFile: track,
		})
	}
	return items, nil
}

// ShuffleQueue changes ranks in place so the saved playback association stays valid.
func (store *Store) ShuffleQueue(ctx context.Context, owner int64) error {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id FROM reinhardt_musicfiletag a
JOIN reinhardt_tag t ON t.id=a.tag_id
WHERE t.owner_id=? AND t.label='queue' ORDER BY random()`, owner)
	if err != nil {
		return err
	}
	var items []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		items = append(items, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for index, id := range items {
		if _, err = tx.ExecContext(ctx, "UPDATE reinhardt_musicfiletag SET rank=? WHERE id=?", index+1, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Playback needs at most the current item and one selected item. Keep these
// lookups separate from Queue, which decorates every track for queue pages.
func (store *Store) queueItem(ctx context.Context, owner int64, predicate string, args ...any) (*QueueItem, error) {
	params := append([]any{owner}, args...)
	return store.queueAssociation(ctx, `SELECT a.* FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
WHERE t.owner_id=? AND t.label='queue' AND `+predicate+` ORDER BY a.rank,a.id LIMIT 1`, params...)
}
func (store *Store) AddTag(ctx context.Context, owner int64, name string) (int64, error) {
	name = label(name)
	if name == "" {
		return 0, fmt.Errorf("invalid tag name")
	}
	// Existing tags need no write transaction (even an ignored insert advances
	// SQLite's AUTOINCREMENT sequence). Preserve first-use creation below.
	tag, err := store.One(ctx, "SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	if err != sql.ErrNoRows {
		return tag.N("id"), err
	}
	// Another caller may have created the tag since the lookup.
	_, err = store.DB.ExecContext(ctx, `INSERT OR IGNORE INTO reinhardt_tag(owner_id,label,created_at,modified_at,is_playlist,is_shared) VALUES(?,?,?,?,0,0)`, owner, name, now(), now())
	if err != nil {
		return 0, err
	}
	tag, err = store.One(ctx, "SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	return tag.N("id"), err
}
func (store *Store) AddTracks(ctx context.Context, owner int64, name string, ids []int64) (int, error) {
	id, err := store.AddTag(ctx, owner, name)
	if err != nil {
		return 0, err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rank int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rank),0) FROM reinhardt_musicfiletag WHERE tag_id=?", id).Scan(&rank); err != nil {
		return 0, err
	}
	count := 0
	for _, track := range ids {
		// Bulk additions retain existing memberships in place. Advancing rank
		// only on insertion also avoids gaps caused by duplicates in the input.
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?)`, id, track, rank+1, now(), now())
		if err != nil {
			return 0, err
		}
		affectedRows, _ := res.RowsAffected()
		if affectedRows > 0 {
			rank++
			count++
		}
	}
	return count, tx.Commit()
}

func (store *Store) AddTrack(ctx context.Context, owner int64, name string, track int64) (*QueueItem, error) {
	tag, err := store.AddTag(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var rank sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT MAX(rank) FROM reinhardt_musicfiletag WHERE tag_id=?", tag).Scan(&rank); err != nil {
		return nil, err
	}
	next := int64(0)
	if rank.Valid {
		next = rank.Int64 + 1
	}
	// Single-track additions intentionally move an existing membership to the
	// end while preserving its id; autofeed relies on this to replay queued songs.
	_, err = tx.ExecContext(ctx, `INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?) ON CONFLICT(tag_id,music_file_id) DO UPDATE SET rank=excluded.rank,modified_at=excluded.modified_at`, tag, track, next, now(), now())
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return store.queueAssociation(ctx, "SELECT * FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=?", tag, track)
}
func rowIDs(rows []Row) []int64 {
	out := []int64{}
	for _, row := range rows {
		out = append(out, row.N("id"))
	}
	return out
}
func ftsEscape(query string) string {
	// SQL parameters do not escape FTS syntax. Quote each word separately so
	// punctuation and words such as OR remain search terms instead of operators.
	words := strings.Fields(query)
	for wordIndex, word := range words {
		words[wordIndex] = "\"" + strings.ReplaceAll(word, "\"", "\"\"") + "\""
	}
	return strings.Join(words, " ")
}
func (store *Store) Search(ctx context.Context, query string) ([]*Track, error) {
	if strings.TrimSpace(query) == "" {
		return []*Track{}, nil
	}
	return store.Tracks(ctx, "JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0 ORDER BY f.rank LIMIT 1000", ftsEscape(query))
}
func (store *Store) ReindexFTS(ctx context.Context) error {
	_, err := store.DB.ExecContext(ctx, `DELETE FROM music_index; INSERT INTO music_index(id,data,artist,title,album,genre,comment) SELECT id,trim(artist||' '||title||' '||album||' '||genre||' '||comment),artist,title,album,genre,comment FROM reinhardt_musicfile WHERE missing=0;`)
	return err
}

func tracksFromRows(rows []Row) []*Track {
	tracks := make([]*Track, 0, len(rows))
	for _, row := range rows {
		tracks = append(tracks, trackFromRow(row))
	}
	return tracks
}
func ids(tracks []*Track) []int64 {
	result := make([]int64, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, track.ID)
	}
	return result
}
func (store *Store) musicDirectory(ctx context.Context, query string, args ...any) (*MusicDirectory, error) {
	row, err := store.One(ctx, query, args...)
	return musicDirectoryFromRow(row), err
}
func (store *Store) user(ctx context.Context, query string, args ...any) (*User, error) {
	row, err := store.One(ctx, query, args...)
	return userFromRow(row), err
}
func (store *Store) tag(ctx context.Context, query string, args ...any) (*Tag, error) {
	row, err := store.One(ctx, query, args...)
	return tagFromRow(row), err
}
func (store *Store) queueAssociation(ctx context.Context, query string, args ...any) (*QueueItem, error) {
	row, err := store.One(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &QueueItem{
		ID: row.N("id"), TagID: row.N("tag_id"),
		MusicFileID: row.N("music_file_id"), Rank: row.N("rank"),
		CreatedAt: row.S("created_at"), ModifiedAt: row.S("modified_at"),
	}, nil
}
