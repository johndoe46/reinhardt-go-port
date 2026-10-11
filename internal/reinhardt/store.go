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

func (r Row) String() string {
	if v, ok := r["label"]; ok {
		return fmt.Sprint(v)
	}
	if v, ok := r["artist"]; ok {
		return fmt.Sprint(v) + " - " + fmt.Sprint(r["title"])
	}
	return fmt.Sprint(r["username"])
}
func (r Row) S(k string) string {
	if r[k] == nil {
		return ""
	}
	return fmt.Sprint(r[k])
}
func (r Row) N(k string) int64 { return number(r[k]) }
func (r Row) B(k string) bool {
	if b, ok := r[k].(bool); ok {
		return b
	}
	return r.N(k) != 0
}
func number(v any) int64 { n, _ := strconv.ParseInt(fmt.Sprint(v), 10, 64); return n }
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func now() string           { return time.Now().UTC().Format("2006-01-02 15:04:05.000000") }
func label(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
func slug(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(s)) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '-' {
			b.WriteByte('-')
		}
	}
	s = b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-_")
}

type Store struct {
	DB *sql.DB
	C  Config
}

var errNoMusicDirectory = errors.New("no music directory is registered")

func (s *Store) DefaultMusicDirectory() (Row, error) {
	dir, err := s.One("SELECT * FROM reinhardt_musicdirectory ORDER BY id LIMIT 1")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w in database %q; run 'reinhardt addmusicdir /path/to/music' using this database", errNoMusicDirectory, s.C["DATABASE_PATH"])
	}
	if err != nil {
		return nil, fmt.Errorf("select music directory in database %q: %w", s.C["DATABASE_PATH"], err)
	}
	return dir, nil
}

func openStore(c Config) (*Store, error) {
	path := c["DATABASE_PATH"]
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
	s := &Store{db, c}
	if c["SECRET_KEY"] == "" {
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
		c["SECRET_KEY"] = cSecret
	}
	return s, nil
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
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		for i, k := range cols {
			v := values[i]
			if t, ok := v.(time.Time); ok {
				// The SQLite driver decodes DATETIME columns; templates and the API
				// expect Django-style UTC strings, not Go's default time formatting.
				format := "2006-01-02T15:04:05"
				if t.Nanosecond() != 0 {
					format += ".000000"
				}
				v = t.UTC().Format(format) + "+00:00"
			}
			r[k] = v
		}
		r["pk"] = r["id"]
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *Store) Query(query string, args ...any) ([]Row, error) {
	rows, e := s.DB.Query(query, args...)
	if e != nil {
		return nil, e
	}
	return scanRows(rows)
}
func (s *Store) One(query string, args ...any) (Row, error) {
	rows, e := s.Query(query, args...)
	if e != nil {
		return nil, e
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}
func (s *Store) Preferences(owner int64) (Row, error) {
	// Even ignored AUTOINCREMENT inserts can write SQLite's sequence. Read first
	// so rendering an initialized account does not acquire a write transaction.
	prefs, e := s.One("SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
	if e != sql.ErrNoRows {
		return prefs, e
	}
	_, e = s.DB.Exec(`INSERT OR IGNORE INTO reinhardt_preferences(owner_id,autofeed_queue,autofeed_similar,repeat_queue,show_oscilloscope,current_position,access_token) VALUES(?,1,1,1,1,0,NULL)`, owner)
	if e != nil {
		return nil, e
	}
	return s.One("SELECT * FROM reinhardt_preferences WHERE owner_id=?", owner)
}

const tracksSQL = `SELECT m.*,d.local_path FROM reinhardt_musicfile m JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id `

func (s *Store) Tracks(clause string, args ...any) ([]Row, error) {
	rows, e := s.Query(tracksSQL+clause, args...)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		s.decorateTrack(r)
	}
	return rows, nil
}
func (s *Store) Track(id any) (Row, error) {
	rs, e := s.Tracks("WHERE m.id=?", id)
	if e != nil {
		return nil, e
	}
	if len(rs) == 0 {
		return nil, sql.ErrNoRows
	}
	return rs[0], nil
}
func (s *Store) decorateTrack(r Row) {
	r["full_path"] = filepath.Join(r.S("local_path"), r.S("relative_path"))
	name := []rune(r.S("artist") + " - " + r.S("title"))
	if len(name) > 55 {
		name = name[:55]
	}
	r["clean_file_name"] = slug(string(name)+" - "+r.S("id")) + filepath.Ext(r.S("relative_path"))
	r["download_url"] = s.C.Path("download/" + r.S("id") + "/" + r.S("clean_file_name"))
	if s.C["DELEGATE_DOWNLOAD_PREFIX"] != "" {
		r["download_url"] = strings.TrimRight(s.C["DELEGATE_DOWNLOAD_PREFIX"], "/") + "/" + r.S("relative_path")
	}
	r["playback_url"] = s.C.Path("stream/" + r.S("id") + "/")
}
func (s *Store) Tags(owner int64, reserved bool) ([]Row, error) {
	clause := ""
	if !reserved {
		clause = " AND t.label NOT IN ('queue','favorites')"
	}
	rs, e := s.Query(`SELECT t.*,COUNT(a.id) track_count,COALESCE(SUM(m.duration),0) total_duration FROM reinhardt_tag t LEFT JOIN reinhardt_musicfiletag a ON a.tag_id=t.id LEFT JOIN reinhardt_musicfile m ON m.id=a.music_file_id WHERE t.owner_id=?`+clause+` GROUP BY t.id ORDER BY t.label`, owner)
	for _, r := range rs {
		s.decorateTag(r)
	}
	return rs, e
}
func (s *Store) decorateTag(r Row) {
	h := sha256.Sum256([]byte(r.S("id") + "-" + s.C["SECRET_KEY"]))
	r["share_hash"] = hex.EncodeToString(h[:])
	r["share_url"] = s.C.Path("shared/" + r.S("id") + "/" + r.S("share_hash") + "/")
	r["duration"] = r["total_duration"]
}
func (s *Store) Tag(id any, owner int64) (Row, error) {
	r, e := s.One("SELECT * FROM reinhardt_tag WHERE id=? AND owner_id=?", id, owner)
	if e == nil {
		s.decorateTag(r)
	}
	return r, e
}
func (s *Store) TagTracks(id any) ([]Row, error) {
	return s.Tracks("JOIN reinhardt_musicfiletag a ON a.music_file_id=m.id WHERE a.tag_id=? AND m.missing=0 ORDER BY a.rank,a.id", id)
}
func (s *Store) Queue(owner int64) ([]Row, error) {
	tracks, e := s.Query(`SELECT m.*,d.local_path,
a.id AS queue_id,a.tag_id AS queue_tag_id,a.rank AS queue_rank,
a.created_at AS queue_created_at,a.modified_at AS queue_modified_at
FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
JOIN reinhardt_musicfile m ON m.id=a.music_file_id
JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id
WHERE t.owner_id=? AND t.label='queue' ORDER BY a.rank,a.id`, owner)
	if e != nil {
		return nil, e
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
		s.decorateTrack(track)
		items = append(items, item)
	}
	return items, nil
}

// Playback needs at most the current item and one selected item. Keep these
// lookups separate from Queue, which decorates every track for queue pages.
func (s *Store) queueItem(owner int64, predicate string, args ...any) (Row, error) {
	params := append([]any{owner}, args...)
	return s.One(`SELECT a.* FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
WHERE t.owner_id=? AND t.label='queue' AND `+predicate+` ORDER BY a.rank,a.id LIMIT 1`, params...)
}
func (s *Store) AddTag(owner int64, name string) (int64, error) {
	name = label(name)
	if name == "" {
		return 0, fmt.Errorf("invalid tag name")
	}
	// Existing tags need no write transaction (even an ignored insert advances
	// SQLite's AUTOINCREMENT sequence). Preserve first-use creation below.
	r, e := s.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	if e != sql.ErrNoRows {
		return r.N("id"), e
	}
	// Another caller may have created the tag since the lookup.
	_, e = s.DB.Exec(`INSERT OR IGNORE INTO reinhardt_tag(owner_id,label,created_at,modified_at,is_playlist,is_shared) VALUES(?,?,?,?,0,0)`, owner, name, now(), now())
	if e != nil {
		return 0, e
	}
	r, e = s.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
	return r.N("id"), e
}
func (s *Store) AddTracks(owner int64, name string, ids []int64) (int, error) {
	id, e := s.AddTag(owner, name)
	if e != nil {
		return 0, e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var rank int64
	if e = tx.QueryRow("SELECT COALESCE(MAX(rank),0) FROM reinhardt_musicfiletag WHERE tag_id=?", id).Scan(&rank); e != nil {
		return 0, e
	}
	count := 0
	for _, track := range ids {
		// Bulk additions retain existing memberships in place. Advancing rank
		// only on insertion also avoids gaps caused by duplicates in the input.
		res, err := tx.Exec(`INSERT OR IGNORE INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?)`, id, track, rank+1, now(), now())
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			rank++
			count++
		}
	}
	return count, tx.Commit()
}

func (s *Store) AddTrack(owner int64, name string, track int64) (Row, error) {
	tag, e := s.AddTag(owner, name)
	if e != nil {
		return nil, e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var rank sql.NullInt64
	if e = tx.QueryRow("SELECT MAX(rank) FROM reinhardt_musicfiletag WHERE tag_id=?", tag).Scan(&rank); e != nil {
		return nil, e
	}
	next := int64(0)
	if rank.Valid {
		next = rank.Int64 + 1
	}
	// Single-track additions intentionally move an existing membership to the
	// end while preserving its id; autofeed relies on this to replay queued songs.
	_, e = tx.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at) VALUES(?,?,?,?,?) ON CONFLICT(tag_id,music_file_id) DO UPDATE SET rank=excluded.rank,modified_at=excluded.modified_at`, tag, track, next, now(), now())
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return s.One("SELECT * FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=?", tag, track)
}
func ids(rows []Row) []int64 {
	out := []int64{}
	for _, r := range rows {
		out = append(out, r.N("id"))
	}
	return out
}
func ftsEscape(q string) string {
	// SQL parameters do not escape FTS syntax. Quote each word separately so
	// punctuation and words such as OR remain search terms instead of operators.
	words := strings.Fields(q)
	for i, w := range words {
		words[i] = "\"" + strings.ReplaceAll(w, "\"", "\"\"") + "\""
	}
	return strings.Join(words, " ")
}
func (s *Store) Search(q string) ([]Row, error) {
	if strings.TrimSpace(q) == "" {
		return []Row{}, nil
	}
	return s.Tracks("JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0 ORDER BY f.rank LIMIT 1000", ftsEscape(q))
}
func (s *Store) ReindexFTS() error {
	_, e := s.DB.Exec(`DELETE FROM music_index; INSERT INTO music_index(id,data,artist,title,album,genre,comment) SELECT id,trim(artist||' '||title||' '||album||' '||genre||' '||comment),artist,title,album,genre,comment FROM reinhardt_musicfile WHERE missing=0;`)
	return e
}
