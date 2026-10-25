-- Initialize new databases and add missing schema objects to existing ones
-- without recreating tables or discarding their data.
CREATE TABLE IF NOT EXISTS auth_user (
 id INTEGER PRIMARY KEY AUTOINCREMENT, password TEXT NOT NULL DEFAULT '!', last_login DATETIME,
 is_superuser BOOL NOT NULL DEFAULT 0, username TEXT NOT NULL UNIQUE,
 first_name TEXT NOT NULL DEFAULT '', last_name TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '',
 is_staff BOOL NOT NULL DEFAULT 0, is_active BOOL NOT NULL DEFAULT 1, date_joined DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS reinhardt_musicdirectory (
 id INTEGER PRIMARY KEY AUTOINCREMENT, local_path TEXT NOT NULL UNIQUE,
 created_at DATETIME NOT NULL, modified_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS reinhardt_musicfile (
 id INTEGER PRIMARY KEY AUTOINCREMENT, music_directory_id INTEGER NOT NULL REFERENCES reinhardt_musicdirectory(id) ON DELETE CASCADE,
 relative_path TEXT NOT NULL UNIQUE, artist TEXT NOT NULL, title TEXT NOT NULL, album TEXT NOT NULL,
 genre TEXT NOT NULL, year TEXT NOT NULL DEFAULT '', comment TEXT NOT NULL, duration INTEGER NOT NULL DEFAULT 0,
 track_number INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, modified_at DATETIME NOT NULL,
 missing BOOL NOT NULL DEFAULT 0, hash TEXT, UNIQUE(music_directory_id, relative_path)
);
-- Artwork references are separate from library metadata so existing databases
-- can adopt the shared cache without rewriting their music-file table.
CREATE TABLE IF NOT EXISTS go_track_artwork (
 track_id INTEGER PRIMARY KEY REFERENCES reinhardt_musicfile(id) ON DELETE CASCADE ON UPDATE CASCADE,
 revision INTEGER NOT NULL DEFAULT 0,
 artwork_hash TEXT CHECK(artwork_hash IS NULL OR (length(artwork_hash)=64 AND artwork_hash NOT GLOB '*[^0-9a-f]*'))
);
CREATE INDEX IF NOT EXISTS go_track_artwork_hash ON go_track_artwork(artwork_hash);
-- Keep a revision even when the reference is invalidated: an older request
-- must not publish its artwork after a metadata edit or an upload.
CREATE TRIGGER IF NOT EXISTS go_track_artwork_update
AFTER UPDATE OF modified_at,artist,title,album,relative_path,music_directory_id ON reinhardt_musicfile
WHEN OLD.modified_at IS NOT NEW.modified_at OR OLD.artist IS NOT NEW.artist
 OR OLD.title IS NOT NEW.title OR OLD.album IS NOT NEW.album
 OR OLD.relative_path IS NOT NEW.relative_path OR OLD.music_directory_id IS NOT NEW.music_directory_id BEGIN
 INSERT INTO go_track_artwork(track_id,revision) VALUES(NEW.id,1)
 ON CONFLICT(track_id) DO UPDATE SET revision=revision+1,artwork_hash=NULL;
END;
CREATE TRIGGER IF NOT EXISTS go_track_artwork_directory_update
AFTER UPDATE OF local_path ON reinhardt_musicdirectory
WHEN OLD.local_path IS NOT NEW.local_path BEGIN
 INSERT INTO go_track_artwork(track_id,revision)
 SELECT id,1 FROM reinhardt_musicfile WHERE music_directory_id=NEW.id
 ON CONFLICT(track_id) DO UPDATE SET revision=revision+1,artwork_hash=NULL;
END;
CREATE TABLE IF NOT EXISTS reinhardt_tag (
 id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
 label TEXT NOT NULL, created_at DATETIME NOT NULL, modified_at DATETIME NOT NULL,
 is_playlist BOOL NOT NULL DEFAULT 0, is_shared BOOL NOT NULL DEFAULT 0, UNIQUE(owner_id, label)
);
CREATE TABLE IF NOT EXISTS reinhardt_musicfiletag (
 id INTEGER PRIMARY KEY AUTOINCREMENT, tag_id INTEGER NOT NULL REFERENCES reinhardt_tag(id) ON DELETE CASCADE,
 music_file_id INTEGER NOT NULL REFERENCES reinhardt_musicfile(id) ON DELETE CASCADE,
 rank INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, modified_at DATETIME NOT NULL, UNIQUE(tag_id, music_file_id)
);
CREATE TABLE IF NOT EXISTS reinhardt_preferences (
 id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL UNIQUE REFERENCES auth_user(id) ON DELETE CASCADE,
 autofeed_queue BOOL NOT NULL DEFAULT 1, autofeed_similar BOOL NOT NULL DEFAULT 1, repeat_queue BOOL NOT NULL DEFAULT 1,
 show_oscilloscope BOOL NOT NULL DEFAULT 1, current_position INTEGER NOT NULL DEFAULT 0, access_token TEXT UNIQUE,
 language_code TEXT, show_track_grid BOOL NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS reinhardt_youtuberipfailure (
 id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT NOT NULL, artist TEXT NOT NULL, title TEXT NOT NULL,
 album TEXT NOT NULL DEFAULT '', genre TEXT NOT NULL DEFAULT '', year TEXT NOT NULL DEFAULT '', comment TEXT NOT NULL DEFAULT '',
 form_fingerprint TEXT NOT NULL UNIQUE, failure_message TEXT NOT NULL DEFAULT '', retry_count INTEGER NOT NULL DEFAULT 0,
 last_retried_at DATETIME, created_at DATETIME NOT NULL, modified_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS go_session (token TEXT PRIMARY KEY, owner_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS go_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- Shared seed stations have no owner. Account edits create personal copies;
-- hiding a shared station never changes another account's catalogue.
CREATE TABLE IF NOT EXISTS go_radio_station (
 id INTEGER PRIMARY KEY, owner_id INTEGER REFERENCES auth_user(id) ON DELETE CASCADE,
 name TEXT NOT NULL, country TEXT NOT NULL, url TEXT NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 modified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS go_radio_station_owner_country ON go_radio_station(owner_id,country,name);
CREATE TABLE IF NOT EXISTS go_radio_hidden (
 owner_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
 station_id INTEGER NOT NULL REFERENCES go_radio_station(id) ON DELETE CASCADE,
 PRIMARY KEY(owner_id,station_id)
);
-- Radio favorites are account-specific and independent of music-file tags.
CREATE TABLE IF NOT EXISTS go_radio_favorite (
 owner_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
 station_id INTEGER NOT NULL REFERENCES go_radio_station(id) ON DELETE CASCADE,
 PRIMARY KEY(owner_id,station_id)
);
CREATE INDEX IF NOT EXISTS go_radio_favorite_station ON go_radio_favorite(station_id);
-- Presence of a row selects live radio; absence selects the existing queue.
CREATE TABLE IF NOT EXISTS go_radio_playback (
 owner_id INTEGER PRIMARY KEY REFERENCES auth_user(id) ON DELETE CASCADE,
 station_id INTEGER NOT NULL REFERENCES go_radio_station(id) ON DELETE CASCADE
);
CREATE TRIGGER IF NOT EXISTS go_radio_leave_on_queue_selection
AFTER UPDATE OF current_position ON reinhardt_preferences BEGIN
 DELETE FROM go_radio_playback WHERE owner_id=NEW.owner_id;
END;
-- Cache revisions belong in SQLite, not just HTTP handlers: workers, CLI commands,
-- and external processes can all write the library. Trigger increments roll back
-- with their originating transaction, so aborted edits do not invalidate caches.
CREATE TABLE IF NOT EXISTS go_suggestions_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO go_suggestions_revision(id) VALUES(1);
CREATE TRIGGER IF NOT EXISTS go_suggestions_insert AFTER INSERT ON reinhardt_musicfile BEGIN
 UPDATE go_suggestions_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_suggestions_delete AFTER DELETE ON reinhardt_musicfile BEGIN
 UPDATE go_suggestions_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_suggestions_update AFTER UPDATE OF artist,album ON reinhardt_musicfile
WHEN OLD.artist IS NOT NEW.artist OR OLD.album IS NOT NEW.album BEGIN
 UPDATE go_suggestions_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TABLE IF NOT EXISTS go_job (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, payload TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, run_at INTEGER NOT NULL, locked_at INTEGER, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS go_home_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO go_home_revision(id) VALUES(1);
CREATE TRIGGER IF NOT EXISTS go_home_insert AFTER INSERT ON reinhardt_musicfile BEGIN
 UPDATE go_home_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_home_delete AFTER DELETE ON reinhardt_musicfile BEGIN
 UPDATE go_home_revision SET revision=revision+1 WHERE id=1;
END;
-- The home cache retains complete rows, so even metadata edits must invalidate it.
CREATE TRIGGER IF NOT EXISTS go_home_update AFTER UPDATE ON reinhardt_musicfile BEGIN
 UPDATE go_home_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_home_directory_update AFTER UPDATE OF local_path ON reinhardt_musicdirectory
WHEN OLD.local_path IS NOT NEW.local_path BEGIN
 UPDATE go_home_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TABLE IF NOT EXISTS go_autofeed_library_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO go_autofeed_library_revision(id) VALUES(1);
CREATE TABLE IF NOT EXISTS go_autofeed_tag_revision (tag_id INTEGER PRIMARY KEY REFERENCES reinhardt_tag(id) ON DELETE CASCADE ON UPDATE CASCADE DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL);
CREATE TRIGGER IF NOT EXISTS go_autofeed_library_insert AFTER INSERT ON reinhardt_musicfile BEGIN
 UPDATE go_autofeed_library_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_autofeed_library_delete AFTER DELETE ON reinhardt_musicfile BEGIN
 UPDATE go_autofeed_library_revision SET revision=revision+1 WHERE id=1;
END;
-- ID pools and the normalized artist map depend on these columns. Ordinary title,
-- album, and duration edits should not force another eligible-library scan.
CREATE TRIGGER IF NOT EXISTS go_autofeed_library_update AFTER UPDATE OF id,artist,missing ON reinhardt_musicfile
WHEN OLD.id IS NOT NEW.id OR OLD.artist IS NOT NEW.artist OR OLD.missing IS NOT NEW.missing BEGIN
 UPDATE go_autofeed_library_revision SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS go_autofeed_tag_insert AFTER INSERT ON reinhardt_musicfiletag BEGIN
 INSERT INTO go_autofeed_tag_revision(tag_id,revision) VALUES(NEW.tag_id,1)
 ON CONFLICT(tag_id) DO UPDATE SET revision=revision+1;
END;
-- Cascading tag deletion can remove the parent before this trigger runs. Avoid
-- recreating a revision row whose foreign-key parent no longer exists.
CREATE TRIGGER IF NOT EXISTS go_autofeed_tag_delete AFTER DELETE ON reinhardt_musicfiletag
WHEN EXISTS(SELECT 1 FROM reinhardt_tag WHERE id=OLD.tag_id) BEGIN
 INSERT INTO go_autofeed_tag_revision(tag_id,revision) VALUES(OLD.tag_id,1)
 ON CONFLICT(tag_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER IF NOT EXISTS go_autofeed_tag_update AFTER UPDATE OF tag_id,music_file_id ON reinhardt_musicfiletag
WHEN OLD.tag_id IS NOT NEW.tag_id OR OLD.music_file_id IS NOT NEW.music_file_id BEGIN
 INSERT INTO go_autofeed_tag_revision(tag_id,revision) SELECT OLD.tag_id,1
 WHERE EXISTS(SELECT 1 FROM reinhardt_tag WHERE id=OLD.tag_id)
 ON CONFLICT(tag_id) DO UPDATE SET revision=revision+1;
 INSERT INTO go_autofeed_tag_revision(tag_id,revision) VALUES(NEW.tag_id,1)
 ON CONFLICT(tag_id) DO UPDATE SET revision=revision+1;
END;
CREATE INDEX IF NOT EXISTS musicfile_latest_idx ON reinhardt_musicfile(missing, created_at DESC);
CREATE INDEX IF NOT EXISTS musicfile_artist_idx ON reinhardt_musicfile(artist, missing, album, track_number);
CREATE INDEX IF NOT EXISTS musicfile_album_idx ON reinhardt_musicfile(album, missing, track_number);
CREATE INDEX IF NOT EXISTS musicfile_genre_idx ON reinhardt_musicfile(genre, missing, artist, album, track_number);
CREATE INDEX IF NOT EXISTS musicfiletag_rank_idx ON reinhardt_musicfiletag(tag_id, rank);
CREATE VIRTUAL TABLE IF NOT EXISTS music_index USING fts5(id, data, artist, title, album, genre, comment);
CREATE TRIGGER IF NOT EXISTS go_music_insert AFTER INSERT ON reinhardt_musicfile WHEN NEW.missing=0 BEGIN
 INSERT INTO music_index(id,data,artist,title,album,genre,comment) VALUES(NEW.id,trim(NEW.artist||' '||NEW.title||' '||NEW.album||' '||NEW.genre||' '||NEW.comment),NEW.artist,NEW.title,NEW.album,NEW.genre,NEW.comment);
END;
CREATE TRIGGER IF NOT EXISTS go_music_update AFTER UPDATE OF artist,title,album,genre,comment,missing ON reinhardt_musicfile BEGIN
 DELETE FROM music_index WHERE id=OLD.id;
 INSERT INTO music_index(id,data,artist,title,album,genre,comment) SELECT NEW.id,trim(NEW.artist||' '||NEW.title||' '||NEW.album||' '||NEW.genre||' '||NEW.comment),NEW.artist,NEW.title,NEW.album,NEW.genre,NEW.comment WHERE NEW.missing=0;
END;
CREATE TRIGGER IF NOT EXISTS go_music_delete AFTER DELETE ON reinhardt_musicfile BEGIN
 DELETE FROM music_index WHERE id=OLD.id;
END;
