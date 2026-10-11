package reinhardt

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

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
func (row Row) N(key string) int64 { return number(row[key]) }
func (row Row) B(key string) bool {
	if booleanValue, ok := row[key].(bool); ok {
		return booleanValue
	}
	return row.N(key) != 0
}
func number(value any) int64 {
	integerValue, _ := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	return integerValue
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
	Config Config
}

var errNoMusicDirectory = errors.New("no music directory is registered")

func (store *Store) DefaultMusicDirectory() (Row, error) {
	dir, err := store.One("SELECT * FROM reinhardt_musicdirectory ORDER BY id LIMIT 1")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w in database %q; run 'reinhardt addmusicdir /path/to/music' using this database", errNoMusicDirectory, store.Config["DATABASE_PATH"])
	}
	if err != nil {
		return nil, fmt.Errorf("select music directory in database %q: %w", store.Config["DATABASE_PATH"], err)
	}
	return dir, nil
}

func openStore(config Config) (*Store, error) {
	path := config["DATABASE_PATH"]
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
	// enforce foreign keys and wait for locks held by a worker or Django process.
	db, err := sql.Open("sqlite", dsn+sep+"_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// :memory: databases belong to individual connections. The single connection
	// also serializes this process's SQL, but multi-statement changes still need a
	// transaction. Close result sets before another query: otherwise that query
	// waits for the connection still held by the first set of rows.
	db.SetMaxOpenConns(1)
	// The bootstrap is additive: existing Django tables and rows stay in place;
	// Go-specific sessions, jobs, settings, and cache revisions live alongside them.
	schema, _ := assets.ReadFile("schema.sql")
	if _, err = db.Exec(string(schema)); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{db, config}
	if config["SECRET_KEY"] == "" {
		// Persist the fallback key so shared-playlist hashes survive restarts.
		// INSERT OR IGNORE lets simultaneous startup processes agree on one key.
		var cSecret string
		_, err = db.Exec("INSERT OR IGNORE INTO go_settings(key,value) VALUES('secret_key',?)", token())
		if err == nil {
			err = db.QueryRow("SELECT value FROM go_settings WHERE key='secret_key'").Scan(&cSecret)
		}
		if err != nil {
			db.Close()
			return nil, err
		}
		config["SECRET_KEY"] = cSecret
	}
	return store, nil
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
				// expect Django-style UTC strings, not Go's default time formatting.
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
func (store *Store) Query(query string, args ...any) ([]Row, error) {
	rows, err := store.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}
func (store *Store) One(query string, args ...any) (Row, error) {
	rows, err := store.Query(query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}
func (store *Store) Preferences(owner int64) (Row, error) {
	// Even ignored AUTOINCREMENT inserts can write SQLite's sequence. Read first
	// so rendering an initialized account does not acquire a write transaction.
	prefs, err := store.One("SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
	if err != sql.ErrNoRows {
		return prefs, err
	}
	_, err = store.DB.Exec(`INSERT OR IGNORE INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,current_position,access_token) VALUES(?,1,1,1,1,0,NULL)`, owner)
	if err != nil {
		return nil, err
	}
	return store.One("SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
}

const tracksSQL = `SELECT m.*,d.local_path FROM reinhardt_musicfile m JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id `

func (store *Store) Tracks(clause string, args ...any) ([]Row, error) {
	rows, err := store.Query(tracksSQL+clause, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		store.decorateTrack(row)
	}
	return rows, nil
}
func (store *Store) Track(id any) (Row, error) {
	rs, err := store.Tracks("WHERE m.id=?", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, sql.ErrNoRows
	}
	return rs[0], nil
}
func (store *Store) decorateTrack(track Row) {
	track["full_path"] = filepath.Join(track.S("local_path"), track.S("relative_path"))
	name := []rune(track.S("artist") + " - " + track.S("title"))
	if len(name) > 55 {
		name = name[:55]
	}
	track["clean_file_name"] = slug(string(name)+" - "+track.S("id")) + filepath.Ext(track.S("relative_path"))
	track["download_url"] = store.Config.Path("download/" + track.S("id") + "/" + track.S("clean_file_name"))
	if store.Config["DELEGATE_DOWNLOAD_PREFIX"] != "" {
		track["download_url"] = strings.TrimRight(store.Config["DELEGATE_DOWNLOAD_PREFIX"], "/") + "/" + track.S("relative_path")
	}
	track["playback_url"] = store.Config.Path("stream/" + track.S("id") + "/")
}
func (store *Store) Tags(owner int64, reserved bool) ([]Row, error) {
	clause := ""
	if !reserved {
		clause = " AND t.label NOT IN ('queue','favorites')"
	}
	rs, err := store.Query(`SELECT t.*,COUNT(a.id) track_count,COALESCE(SUM(m.duration),0) total_duration FROM reinhardt_tag t LEFT JOIN reinhardt_musicfiletag a ON a.tag_id=t.id LEFT JOIN reinhardt_musicfile m ON m.id=a.music_file_id WHERE t.owner_id=?`+clause+` GROUP BY t.id ORDER BY t.label`, owner)
	for _, row := range rs {
		store.decorateTag(row)
	}
	return rs, err
}
func (store *Store) decorateTag(tag Row) {
	digest := sha256.Sum256([]byte(tag.S("id") + "-" + store.Config["SECRET_KEY"]))
	tag["share_hash"] = hex.EncodeToString(digest[:])
	tag["share_url"] = store.Config.Path("shared/" + tag.S("id") + "/" + tag.S("share_hash") + "/")
	tag["duration"] = tag["total_duration"]
}
func (store *Store) Tag(id any, owner int64) (Row, error) {
	tag, err := store.One("SELECT * FROM reinhardt_tag WHERE id=? AND owner_id=?", id, owner)
	if err == nil {
		store.decorateTag(tag)
	}
	return tag, err
}
func (store *Store) TagTracks(id any) ([]Row, error) {
	return store.Tracks("JOIN reinhardt_musicfiletag a ON a.music_file_id=m.id WHERE a.tag_id=? AND m.missing=0 ORDER BY a.rank,a.id", id)
}
func (store *Store) Queue(owner int64) ([]Row, error) {
	tracks, err := store.Query(`SELECT m.*,d.local_path,
a.id AS queue_id,a.tag_id AS queue_tag_id,a.rank AS queue_rank,
a.created_at AS queue_created_at,a.modified_at AS queue_modified_at
FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
JOIN reinhardt_musicfile m ON m.id=a.music_file_id
JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id
WHERE t.owner_id=? AND t.label='queue' ORDER BY a.rank,a.id`, owner)
	if err != nil {
		return nil, err
	}
	items := make([]Row, 0, len(tracks))
	for _, track := range tracks {
		// Both records have an id. Keep the track's id in the nested row and the
		// association's id on the item: current_position refers to the latter.
		item := Row{"music_file_id": track["id"], "music_file": track}
		for _, key := range []string{"id", "tag_id", "rank", "created_at", "modified_at"} {
			item[key] = track["queue_"+key]
			delete(track, "queue_"+key)
		}
		item["pk"] = item["id"]
		store.decorateTrack(track)
		items = append(items, item)
	}
	return items, nil
}

// Playback needs at most the current item and one selected item. Keep these
// lookups separate from Queue, which decorates every track for queue pages.
func (store *Store) queueItem(owner int64, predicate string, args ...any) (Row, error) {
	params := append([]any{owner}, args...)
	return store.One(`SELECT a.* FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
WHERE t.owner_id=? AND t.label='queue' AND `+predicate+` ORDER BY a.rank,a.id LIMIT 1`, params...)
}
func (store *Store) AddTag(owner int64, name string) (int64, error) {
	name = label(name)
	if name == "" {
		return 0, fmt.Errorf("invalid tag name")
	}
	// Existing tags need no write transaction (even an ignored insert advances
	// SQLite's AUTOINCREMENT sequence). Preserve first-use creation below.
	tag, err := store.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	if err != sql.ErrNoRows {
		return tag.N("id"), err
	}
	// Another caller may have created the tag since the lookup.
	_, err = store.DB.Exec(`INSERT OR IGNORE INTO reinhardt_tag(owner_id,label,created_at,modified_at,is_playlist,is_shared) VALUES(?,?,?,?,0,0)`, owner, name, now(), now())
	if err != nil {
		return 0, err
	}
	tag, err = store.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	return tag.N("id"), err
}
func (store *Store) AddTracks(owner int64, name string, ids []int64) (int, error) {
	id, err := store.AddTag(owner, name)
	if err != nil {
		return 0, err
	}
	tx, err := store.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rank int64
	if err = tx.QueryRow("SELECT COALESCE(MAX(rank),0) FROM reinhardt_musicfiletag WHERE tag_id=?", id).Scan(&rank); err != nil {
		return 0, err
	}
	count := 0
	for _, track := range ids {
		// Bulk additions retain existing memberships in place. Advancing rank
		// only on insertion also avoids gaps caused by duplicates in the input.
		res, err := tx.Exec(`INSERT OR IGNORE INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?)`, id, track, rank+1, now(), now())
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

func (store *Store) AddTrack(owner int64, name string, track int64) (Row, error) {
	tag, err := store.AddTag(owner, name)
	if err != nil {
		return nil, err
	}
	tx, err := store.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var rank sql.NullInt64
	if err = tx.QueryRow("SELECT MAX(rank) FROM reinhardt_musicfiletag WHERE tag_id=?", tag).Scan(&rank); err != nil {
		return nil, err
	}
	next := int64(0)
	if rank.Valid {
		next = rank.Int64 + 1
	}
	// Single-track additions intentionally move an existing membership to the
	// end while preserving its id; autofeed relies on this to replay queued songs.
	_, err = tx.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?) ON CONFLICT(tag_id,music_file_id) DO UPDATE SET rank=excluded.rank,modified_at=excluded.modified_at`, tag, track, next, now(), now())
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return store.One("SELECT * FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=?", tag, track)
}
func ids(rows []Row) []int64 {
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
func (store *Store) Search(query string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		return []Row{}, nil
	}
	return store.Tracks("JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0 ORDER BY f.rank LIMIT 1000", ftsEscape(query))
}
func (store *Store) ReindexFTS() error {
	_, err := store.DB.Exec(`DELETE FROM music_index; INSERT INTO music_index(id,data,artist,title,album,genre,comment) SELECT id,trim(artist||' '||title||' '||album||' '||genre||' '||comment),artist,title,album,genre,comment FROM reinhardt_musicfile WHERE missing=0;`)
	return err
}
