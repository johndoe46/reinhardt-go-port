package reinhardt

import "fmt"

// Domain records use typed fields. Row is reserved for SQL projections and
// template/API data at the application boundary.

// Track holds indexed audio metadata and its derived library URLs.
type Track struct {
	ID               int64
	MusicDirectoryID int64
	RelativePath     string
	Artist           string
	Title            string
	Album            string
	Genre            string
	Year             string
	Comment          string
	Duration         int64
	TrackNumber      int64
	CreatedAt        string
	ModifiedAt       string
	Missing          bool
	Hash             *string
	LocalPath        string
	FullPath         string
	CleanFileName    string
	DownloadURL      string
	PlaybackURL      string
}

// User holds account details and the current request's authentication state.
type User struct {
	ID              int64
	Username        string
	Email           string
	FirstName       string
	LastName        string
	IsSuperuser     bool
	IsStaff         bool
	IsActive        bool
	IsAuthenticated bool
	LanguageCode    string
	LastLogin       *string
	DateJoined      string
}

// Tag identifies a playlist or reserved collection and its display totals.
type Tag struct {
	ID            int64
	OwnerID       int64
	Label         string
	CreatedAt     string
	ModifiedAt    string
	IsPlaylist    bool
	IsShared      bool
	TrackCount    int64
	TotalDuration int64
	Duration      int64
	ShareHash     string
	ShareURL      string
}

// Preferences holds a user's playback settings and optional language choice.
type Preferences struct {
	ID               int64
	OwnerID          int64
	AutofeedQueue    bool
	AutofeedSimilar  bool
	RepeatQueue      bool
	ShowOscilloscope bool
	CurrentPosition  int64
	AccessToken      string
	LanguageCode     *string
}

// MusicDirectory is a registered root for library audio files.
type MusicDirectory struct {
	ID         int64
	LocalPath  string
	CreatedAt  string
	ModifiedAt string
}

// QueueItem keeps playlist membership identity separate from track identity.
type QueueItem struct {
	ID          int64
	TagID       int64
	MusicFileID int64
	Rank        int64
	CreatedAt   string
	ModifiedAt  string
	MusicFile   *Track
}

func trackFromRow(row Row) *Track {
	if row == nil {
		return nil
	}
	return &Track{
		ID:               row.N("id"),
		MusicDirectoryID: row.N("music_directory_id"),
		RelativePath:     row.S("relative_path"),
		Artist:           row.S("artist"),
		Title:            row.S("title"),
		Album:            row.S("album"),
		Genre:            row.S("genre"),
		Year:             row.S("year"),
		Comment:          row.S("comment"),
		Duration:         row.N("duration"),
		TrackNumber:      row.N("track_number"),
		CreatedAt:        row.S("created_at"),
		ModifiedAt:       row.S("modified_at"),
		Missing:          row.B("missing"),
		Hash:             nullableString(row["hash"]),
		LocalPath:        row.S("local_path"),
		FullPath:         row.S("full_path"),
		CleanFileName:    row.S("clean_file_name"),
		DownloadURL:      row.S("download_url"),
		PlaybackURL:      row.S("playback_url"),
	}
}

func userFromRow(row Row) *User {
	if row == nil {
		return nil
	}
	return &User{
		ID:              row.N("id"),
		Username:        row.S("username"),
		Email:           row.S("email"),
		FirstName:       row.S("first_name"),
		LastName:        row.S("last_name"),
		IsSuperuser:     row.B("is_superuser"),
		IsStaff:         row.B("is_staff"),
		IsActive:        row.B("is_active"),
		IsAuthenticated: row.B("is_authenticated"),
		LanguageCode:    row.S("language_code"),
		LastLogin:       nullableString(row["last_login"]),
		DateJoined:      row.S("date_joined"),
	}
}

func tagFromRow(row Row) *Tag {
	if row == nil {
		return nil
	}
	return &Tag{
		ID:            row.N("id"),
		OwnerID:       row.N("owner_id"),
		Label:         row.S("label"),
		CreatedAt:     row.S("created_at"),
		ModifiedAt:    row.S("modified_at"),
		IsPlaylist:    row.B("is_playlist"),
		IsShared:      row.B("is_shared"),
		TrackCount:    row.N("track_count"),
		TotalDuration: row.N("total_duration"),
		Duration:      row.N("duration"),
		ShareHash:     row.S("share_hash"),
		ShareURL:      row.S("share_url"),
	}
}

func preferencesFromRow(row Row) *Preferences {
	if row == nil {
		return nil
	}
	return &Preferences{
		ID:               row.N("id"),
		OwnerID:          row.N("owner_id"),
		AutofeedQueue:    row.B("autofeed_queue"),
		AutofeedSimilar:  row.B("autofeed_similar"),
		RepeatQueue:      row.B("repeat_queue"),
		ShowOscilloscope: row.B("show_oscilloscope"),
		CurrentPosition:  row.N("current_position"),
		AccessToken:      row.S("access_token"),
		LanguageCode:     nullableString(row["language_code"]),
	}
}

func musicDirectoryFromRow(row Row) *MusicDirectory {
	if row == nil {
		return nil
	}
	return &MusicDirectory{
		ID:         row.N("id"),
		LocalPath:  row.S("local_path"),
		CreatedAt:  row.S("created_at"),
		ModifiedAt: row.S("modified_at"),
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableString(value any) *string {
	if value == nil {
		return nil
	}
	text := fmt.Sprint(value)
	return &text
}

func (record *Track) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":                 record.ID,
		"id":                 record.ID,
		"music_directory_id": record.MusicDirectoryID,
		"relative_path":      record.RelativePath,
		"artist":             record.Artist,
		"title":              record.Title,
		"album":              record.Album,
		"genre":              record.Genre,
		"year":               record.Year,
		"comment":            record.Comment,
		"duration":           record.Duration,
		"track_number":       record.TrackNumber,
		"created_at":         record.CreatedAt,
		"modified_at":        record.ModifiedAt,
		"missing":            record.Missing,
		"hash":               record.Hash,
		"local_path":         record.LocalPath,
		"full_path":          record.FullPath,
		"clean_file_name":    record.CleanFileName,
		"download_url":       record.DownloadURL,
		"playback_url":       record.PlaybackURL,
	}
}

func (record *User) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":               record.ID,
		"id":               record.ID,
		"username":         record.Username,
		"email":            record.Email,
		"first_name":       record.FirstName,
		"last_name":        record.LastName,
		"is_superuser":     record.IsSuperuser,
		"is_staff":         record.IsStaff,
		"is_active":        record.IsActive,
		"is_authenticated": record.IsAuthenticated,
		"language_code":    record.LanguageCode,
		"last_login":       record.LastLogin,
		"date_joined":      record.DateJoined,
	}
}

func (record *Tag) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":             record.ID,
		"id":             record.ID,
		"owner_id":       record.OwnerID,
		"label":          record.Label,
		"created_at":     record.CreatedAt,
		"modified_at":    record.ModifiedAt,
		"is_playlist":    record.IsPlaylist,
		"is_shared":      record.IsShared,
		"track_count":    record.TrackCount,
		"total_duration": record.TotalDuration,
		"duration":       record.Duration,
		"share_hash":     record.ShareHash,
		"share_url":      record.ShareURL,
	}
}

func (record *Preferences) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":                record.ID,
		"id":                record.ID,
		"owner_id":          record.OwnerID,
		"autofeed_queue":    record.AutofeedQueue,
		"autofeed_similar":  record.AutofeedSimilar,
		"repeat_queue":      record.RepeatQueue,
		"show_oscilloscope": record.ShowOscilloscope,
		"current_position":  record.CurrentPosition,
		"access_token":      record.AccessToken,
		"language_code":     record.LanguageCode,
	}
}

func (record *MusicDirectory) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":          record.ID,
		"id":          record.ID,
		"local_path":  record.LocalPath,
		"created_at":  record.CreatedAt,
		"modified_at": record.ModifiedAt,
	}
}

func (record *QueueItem) templateRow() Row {
	if record == nil {
		return nil
	}
	return Row{
		"pk":            record.ID,
		"id":            record.ID,
		"tag_id":        record.TagID,
		"music_file_id": record.MusicFileID,
		"rank":          record.Rank,
		"created_at":    record.CreatedAt,
		"modified_at":   record.ModifiedAt,
		"music_file":    record.MusicFile,
	}
}

func templateDomainRows[T interface{ templateRow() Row }](records []T) []Row {
	rows := make([]Row, 0, len(records))
	for _, record := range records {
		row := record.templateRow()
		for key, value := range row {
			row[key] = templateDomainValue(value)
		}
		rows = append(rows, row)
	}
	return rows
}

// Convert domain records only when rendering legacy template field names.
func templateDomainValue(value any) any {
	switch value := value.(type) {
	case interface{ templateRow() Row }:
		return templateDomainValue(value.templateRow())
	case []*Track:
		return templateDomainRows(value)
	case []*User:
		return templateDomainRows(value)
	case []*Tag:
		return templateDomainRows(value)
	case []*Preferences:
		return templateDomainRows(value)
	case []*MusicDirectory:
		return templateDomainRows(value)
	case []*QueueItem:
		return templateDomainRows(value)

	case Row:
		if value == nil {
			return Row(nil)
		}
		row := make(Row, len(value))
		for key, item := range value {
			row[key] = templateDomainValue(item)
		}
		return row
	case TemplateContext:
		result := make(TemplateContext, len(value))
		for key, item := range value {
			result[key] = templateDomainValue(item)
		}
		return result
	case []Row:
		rows := make([]Row, 0, len(value))
		for _, row := range value {
			rows = append(rows, templateDomainValue(row).(Row))
		}
		return rows
	}
	return value
}
