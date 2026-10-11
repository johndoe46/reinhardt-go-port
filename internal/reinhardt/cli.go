package reinhardt

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// Execute runs the integrated CLI using the process arguments.
func Execute() error {
	return newCLI().Execute()
}

func newCLI() *cobra.Command {
	var envFile, database string
	var s *Store
	var c Config
	root := &cobra.Command{Use: "reinhardt", Short: "Reinhardt music library and streaming server", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVar(&envFile, "env-file", ".env", "dotenv file (process environment takes precedence)")
	root.PersistentFlags().StringVar(&database, "database", "", "SQLite database path (overrides DATABASE_PATH)")
	load := func(cmd *cobra.Command, args []string) error {
		var err error
		c, err = loadConfig(envFile, database)
		return err
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := load(cmd, args); err != nil {
			return err
		}
		var e error
		s, e = openStore(c)
		return e
	}
	root.PersistentPostRunE = func(cmd *cobra.Command, args []string) error {
		if s != nil {
			return s.DB.Close()
		}
		return nil
	}
	add := func(use, short string, count int, run func(*cobra.Command, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count), RunE: run}
		root.AddCommand(c)
		return c
	}
	var showSecrets bool
	config := add("config", "Print effective application configuration (credentials redacted)", 0, func(cmd *cobra.Command, args []string) error {
		return printConfig(cmd.OutOrStdout(), c, showSecrets)
	})
	// Inspect configuration without initializing or migrating the database.
	config.PersistentPreRunE = load
	config.Flags().BoolVar(&showSecrets, "show-secrets", false, "print configured credentials without redaction")
	var addr string
	var noWorker bool
	serve := &cobra.Command{Use: "serve [address]", Aliases: []string{"runserver"}, Short: "Serve the web app and background jobs", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			addr = args[0]
		}
		if addr == "" {
			addr = s.C["LISTEN_ADDR"]
		}
		app, e := newApp(s)
		if e != nil {
			return e
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// A server-wide WriteTimeout would also cut off long audio responses.
		// Bound headers and idle connections while allowing active streams to finish.
		server := &http.Server{Addr: addr, Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
		workerDone := make(chan struct{})
		if !noWorker {
			go func() {
				defer close(workerDone)
				if e := app.Work(ctx, false); e != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "Worker:", e)
					stop()
				}
			}()
		} else {
			close(workerDone)
		}
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				server.Close()
			}
		}()
		fmt.Fprintf(cmd.OutOrStdout(), "Reinhardt listening on %s%s\n", addr, s.C.Path(""))
		e = server.ListenAndServe()
		stop()
		// Successful CLI completion closes SQLite in the post-run hook. Wait for
		// the worker and HTTP shutdown to stop using it before that cleanup can run.
		<-workerDone
		<-shutdownDone
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	}}
	serve.Flags().StringVar(&addr, "listen", "", "listen address (default LISTEN_ADDR)")
	serve.Flags().BoolVar(&noWorker, "no-worker", false, "run the HTTP server without background jobs")
	root.AddCommand(serve)
	var once bool
	worker := add("worker", "Process durable jobs and periodic imports", 0, func(cmd *cobra.Command, args []string) error {
		app, e := newApp(s)
		if e != nil {
			return e
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return app.Work(ctx, once)
	})
	worker.Aliases = []string{"run_huey"}
	worker.Flags().BoolVar(&once, "once", false, "process ready jobs and exit")
	add("migrate", "Initialize SQLite schema and rebuild the search index", 0, func(cmd *cobra.Command, args []string) error { return s.ReindexFTS() })
	add("check", "Check the database and compile all embedded templates", 0, func(cmd *cobra.Command, args []string) error {
		if _, e := newTemplates(); e != nil {
			return e
		}
		r, e := s.One("PRAGMA integrity_check")
		if e != nil {
			return e
		}
		if r.S("integrity_check") != "ok" {
			return fmt.Errorf("database integrity: %v", r)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Database and templates OK")
		return nil
	})
	add("addmusicdir <music_dir>", "Register a music directory", 1, func(cmd *cobra.Command, args []string) error {
		path, e := filepath.Abs(filepath.Clean(args[0]))
		if e != nil {
			return e
		}
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		if !info.IsDir() {
			return fmt.Errorf("music_dir must be a directory")
		}
		_, e = s.DB.Exec("INSERT OR IGNORE INTO reinhardt_musicdirectory(local_path,created_at,modified_at) VALUES(?,?,?)", path, now(), now())
		return e
	})
	add("indexmusic", "Index MP3/M4A metadata and mark missing files", 0, func(cmd *cobra.Command, args []string) error { return s.Index(cmd.OutOrStdout()) })
	add("gathermusic", "Import files and ZIP archives from MUSIC_IMPORT_DIR", 0, func(cmd *cobra.Command, args []string) error { return s.Gather(cmd.Context()) })
	var email string
	var superuser bool
	access := add("giveaccess <username>", "Create an account and print its persistent login link", 1, func(cmd *cobra.Command, args []string) error {
		username := strings.ToLower(strings.TrimSpace(args[0]))
		if username == "" {
			return fmt.Errorf("username is required")
		}
		if len(username) > 150 {
			return fmt.Errorf("username must be at most 150 characters")
		}
		_, e := s.DB.Exec(`INSERT OR IGNORE INTO auth_user(password,last_login,is_superuser,username,first_name,last_name,email,is_staff,is_active,date_joined) VALUES('!',NULL,?,?, '','',?, ?,1,?)`, superuser, username, email, superuser, now())
		if e != nil {
			return e
		}
		u, e := s.One("SELECT * FROM auth_user WHERE username=?", username)
		if e != nil {
			return e
		}
		if superuser {
			if _, e = s.DB.Exec("UPDATE auth_user SET is_superuser=1,is_staff=1 WHERE id=?", u.N("id")); e != nil {
				return e
			}
		}
		if email != "" {
			if _, e = s.DB.Exec("UPDATE auth_user SET email=? WHERE id=?", email, u.N("id")); e != nil {
				return e
			}
		}
		prefs, e := s.Preferences(u.N("id"))
		if e != nil {
			return e
		}
		access := prefs.S("access_token")
		if access == "" {
			access = token()
			if _, e = s.DB.Exec("UPDATE reinhardt_preferences SET access_token=? WHERE id=?", access, prefs.N("id")); e != nil {
				return e
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), s.C["ACCESS_URL_PREFIX"]+s.URL("invite")+"?q="+access)
		return nil
	})
	access.Flags().StringVar(&email, "email", "", "account email address")
	access.Flags().BoolVar(&superuser, "superuser", false, "grant administrative permissions")
	add("listaccess", "Print existing account login links", 0, func(cmd *cobra.Command, args []string) error {
		rows, e := s.Query("SELECT u.username,p.access_token FROM reinhardt_preferences p JOIN auth_user u ON u.id=p.owner_id ORDER BY u.username")
		if e != nil {
			return e
		}
		for _, r := range rows {
			fmt.Fprintln(cmd.OutOrStdout(), r.S("username"), s.C["ACCESS_URL_PREFIX"]+s.URL("invite")+"?q="+r.S("access_token"))
		}
		return nil
	})
	add("listtags", "List tags and their owners", 0, func(cmd *cobra.Command, args []string) error {
		rows, e := s.Query("SELECT t.*,u.username FROM reinhardt_tag t JOIN auth_user u ON u.id=t.owner_id ORDER BY u.username,t.label")
		if e != nil {
			return e
		}
		for _, r := range rows {
			fmt.Fprintf(cmd.OutOrStdout(), "%3d\t%s (%s)\n", r.N("id"), r.S("label"), r.S("username"))
		}
		return nil
	})
	add("fetchtagfiles <tag_id> <destination>", "Copy a playlist's files in ranked order", 2, func(cmd *cobra.Command, args []string) error {
		info, e := os.Stat(args[1])
		if e != nil {
			return e
		}
		if !info.IsDir() {
			return fmt.Errorf("destination must be a directory")
		}
		if _, e = s.One("SELECT id FROM reinhardt_tag WHERE id=?", args[0]); e != nil {
			return e
		}
		tracks, e := s.TagTracks(args[0])
		if e != nil {
			return e
		}
		for i, m := range tracks {
			dest := filepath.Join(args[1], fmt.Sprintf("%03d-%s", i+1, filepath.Base(m.S("full_path"))))
			f, e := os.Open(m.S("full_path"))
			if e != nil {
				return e
			}
			e = atomicFile(dest, f, true)
			f.Close()
			if e != nil {
				return e
			}
			fmt.Fprintln(cmd.OutOrStdout(), dest)
		}
		return nil
	})
	add("tagsexport <csv_file_output>", "Export indexed metadata as CSV", 1, func(cmd *cobra.Command, args []string) error {
		tracks, e := s.Tracks("ORDER BY m.artist,m.album,m.title,m.track_number")
		if e != nil {
			return e
		}
		f, e := os.Create(args[0])
		if e != nil {
			return e
		}
		defer f.Close()
		writer := csv.NewWriter(f)
		writer.Write([]string{"path", "artist", "title", "album", "genre"})
		for _, m := range tracks {
			writer.Write([]string{m.S("full_path"), m.S("artist"), m.S("title"), m.S("album"), m.S("genre")})
		}
		writer.Flush()
		return writer.Error()
	})
	add("populateyears", "Populate only the year column from audio metadata", 0, func(cmd *cobra.Command, args []string) error {
		tracks, e := s.Tracks("")
		if e != nil {
			return e
		}
		count := 0
		for _, m := range tracks {
			metadata, e := s.Metadata(m.S("full_path"))
			if e != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %s: %v\n", m.S("full_path"), e)
				continue
			}
			if _, e = s.DB.Exec("UPDATE reinhardt_musicfile SET year=? WHERE id=?", metadata.S("year"), m.N("id")); e != nil {
				return e
			}
			count++
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated year for %d music files.\n", count)
		return nil
	})
	add("dedup", "Print duplicate music paths by content without deleting files", 0, func(cmd *cobra.Command, args []string) error {
		dirs, e := s.Query("SELECT local_path FROM reinhardt_musicdirectory ORDER BY id")
		if e != nil {
			return e
		}
		hashes := map[string]bool{}
		for _, d := range dirs {
			e = walkMusic(d.S("local_path"), func(path string) error {
				f, e := os.Open(path)
				if e != nil {
					return e
				}
				hash := md5.New()
				_, e = io.Copy(hash, f)
				f.Close()
				if e != nil {
					return e
				}
				key := hex.EncodeToString(hash.Sum(nil))
				if hashes[key] {
					fmt.Fprintln(cmd.OutOrStdout(), path)
				}
				hashes[key] = true
				return nil
			})
			if e != nil {
				return e
			}
		}
		return nil
	})
	add("jamasync", "Upload every indexed track to the configured Jama project", 0, func(cmd *cobra.Command, args []string) error {
		a, e := newApp(s)
		if e != nil {
			return e
		}
		rows, e := s.Query("SELECT id FROM reinhardt_musicfile ORDER BY id DESC")
		if e != nil {
			return e
		}
		for _, m := range rows {
			if e = a.syncJama(cmd.Context(), m.N("id")); e != nil {
				return e
			}
		}
		return nil
	})
	add("dbxrefreshtoken", "Exchange a Dropbox authorization code for a refresh token", 0, func(cmd *cobra.Command, args []string) error {
		a, e := newApp(s)
		if e != nil {
			return e
		}
		if s.C["DBX_KEY"] == "" || s.C["DBX_SECRET"] == "" {
			return fmt.Errorf("DBX_KEY and DBX_SECRET are required")
		}
		values := map[string]string{"client_id": s.C["DBX_KEY"], "response_type": "code", "token_access_type": "offline"}
		parts := []string{}
		for k, v := range values {
			parts = append(parts, k+"="+urlQueryEscape(v))
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Open https://www.dropbox.com/oauth2/authorize?"+strings.Join(parts, "&"))
		fmt.Fprint(cmd.OutOrStdout(), "Authorization code: ")
		scanner := bufio.NewScanner(cmd.InOrStdin())
		if !scanner.Scan() {
			return fmt.Errorf("authorization code is required")
		}
		data, e := a.dropboxToken(cmd.Context(), "authorization_code", strings.TrimSpace(scanner.Text()))
		if e != nil {
			return e
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Access token:", data["access_token"])
		fmt.Fprintln(cmd.OutOrStdout(), "Refresh token:", data["refresh_token"])
		fmt.Fprintln(cmd.OutOrStdout(), "Account ID:", data["account_id"])
		return nil
	})
	var album, genre, year, comment string
	ytrip := add("ytrip <url> <artist> <title>", "Download and import a YouTube track", 3, func(cmd *cobra.Command, args []string) error {
		a, e := newApp(s)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
		defer cancel()
		return a.youtubeRip(ctx, Row{"url": args[0], "artist": args[1], "title": args[2], "album": album, "genre": genre, "year": year, "comment": comment})
	})
	ytrip.Flags().StringVarP(&album, "album", "a", "", "album name")
	ytrip.Flags().StringVarP(&genre, "genre", "g", "", "genre")
	ytrip.Flags().StringVarP(&year, "year", "y", "", "release year")
	ytrip.Flags().StringVar(&comment, "comment", "", "comment")
	add("jobs", "List queued and failed background jobs", 0, func(cmd *cobra.Command, args []string) error {
		rows, e := s.Query("SELECT * FROM go_job ORDER BY id")
		if e != nil {
			return e
		}
		for _, r := range rows {
			fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\tattempts=%d\t%s\n", r.N("id"), r.S("kind"), r.N("attempts"), r.S("error"))
		}
		return nil
	})
	return root
}

func urlQueryEscape(v string) string { return url.QueryEscape(v) }
