package reinhardt

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"errors"
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
	"golang.org/x/term"
)

// Execute runs the integrated CLI using the process arguments.
func Execute() error {
	command, closeStore := newCLI()
	return executeCLI(command, closeStore)
}

// executeCLI owns cleanup for the entire execution, including failed commands
// and hooks. Cobra only runs post-run hooks when the command succeeds.
func executeCLI(command *cobra.Command, closeStore func() error) (err error) {
	defer func() {
		if closeErr := closeStore(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return command.Execute()
}

func newCLI() (*cobra.Command, func() error) {
	var envFile, database string
	var store *Store
	var appConfig *Config
	root := &cobra.Command{Use: "reinhardt", Short: "Reinhardt music library and streaming server", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVar(&envFile, "env-file", ".env", "dotenv file (process environment takes precedence)")
	root.PersistentFlags().StringVar(&database, "database", "", "SQLite database path (overrides DATABASE_PATH)")
	load := func(cmd *cobra.Command, args []string) error {
		var err error
		appConfig, err = loadConfig(envFile, database)
		return err
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := load(cmd, args); err != nil {
			return err
		}
		var err error
		store, err = openStore(cmd.Context(), appConfig)
		return err
	}
	closeStore := func() error {
		if store != nil {
			return store.DB.Close()
		}
		return nil
	}
	add := func(use, short string, count int, run func(*cobra.Command, []string) error) *cobra.Command {
		command := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count), RunE: run}
		root.AddCommand(command)
		return command
	}
	var showSecrets bool
	config := add("config", "Print effective application configuration (credentials redacted)", 0, func(cmd *cobra.Command, args []string) error {
		return printConfig(cmd.OutOrStdout(), appConfig, showSecrets)
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
			addr = store.Config.ListenAddr
		}
		app, err := newApp(store)
		if err != nil {
			return err
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
				if err := app.Work(ctx, false); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "Worker:", err)
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
		fmt.Fprintf(cmd.OutOrStdout(), "Reinhardt listening on %s%s\n", addr, store.Config.Path(""))
		err = server.ListenAndServe()
		stop()
		// CLI execution closes SQLite on return. Wait for the worker and HTTP
		// shutdown to stop using it before that cleanup can run.
		<-workerDone
		<-shutdownDone
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}}
	serve.Flags().StringVar(&addr, "listen", "", "listen address (default LISTEN_ADDR)")
	serve.Flags().BoolVar(&noWorker, "no-worker", false, "run the HTTP server without background jobs")
	root.AddCommand(serve)
	tui := add("tui", "Manage users in an interactive terminal interface", 0, func(cmd *cobra.Command, args []string) error {
		ui, err := newUsersTUI(cmd.Context(), store)
		if err != nil {
			return err
		}
		return ui.run()
	})
	tui.Long = "Manage users in a full-screen terminal interface: create, edit, or permanently delete accounts, change active and administrative status, and view or reset login links."
	tui.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			return fmt.Errorf("tui requires an interactive terminal on stdin and stdout")
		}
		return root.PersistentPreRunE(cmd, args)
	}
	var once bool
	worker := add("worker", "Process durable jobs and periodic imports", 0, func(cmd *cobra.Command, args []string) error {
		app, err := newApp(store)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return app.Work(ctx, once)
	})
	worker.Flags().BoolVar(&once, "once", false, "process ready jobs and exit")
	add("migrate", "Initialize SQLite schema and rebuild the search index", 0, func(cmd *cobra.Command, args []string) error { return store.ReindexFTS(cmd.Context()) })
	add("check", "Check the database and compile all embedded templates", 0, func(cmd *cobra.Command, args []string) error {
		if _, err := newTemplates(); err != nil {
			return err
		}
		integrityResult, err := store.One(cmd.Context(), "PRAGMA integrity_check")
		if err != nil {
			return err
		}
		if integrityResult.S("integrity_check") != "ok" {
			return fmt.Errorf("database integrity: %v", integrityResult)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Database and templates OK")
		return nil
	})
	add("addmusicdir <music_dir>", "Register a music directory", 1, func(cmd *cobra.Command, args []string) error {
		path, err := filepath.Abs(filepath.Clean(args[0]))
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("music_dir must be a directory")
		}
		_, err = store.DB.ExecContext(cmd.Context(), "INSERT OR IGNORE INTO reinhardt_musicdirectory(local_path,created_at,modified_at) VALUES(?,?,?)", path, now(), now())
		return err
	})
	add("indexmusic", "Index MP3/M4A metadata and mark missing files", 0, func(cmd *cobra.Command, args []string) error { return store.Index(cmd.Context(), cmd.OutOrStdout()) })
	add("gathermusic", "Import files and ZIP archives from MUSIC_IMPORT_DIR", 0, func(cmd *cobra.Command, args []string) error { return store.Gather(cmd.Context()) })
	var email string
	var superuser bool
	access := add("giveaccess <username>", "Create an account and print its persistent login link", 1, func(cmd *cobra.Command, args []string) error {
		username, err := normalizeUsername(args[0])
		if err != nil {
			return err
		}
		_, err = store.DB.ExecContext(cmd.Context(), `INSERT OR IGNORE INTO auth_user(password,last_login,is_superuser,username,first_name,last_name,email,is_staff,is_active,date_joined) VALUES('!',NULL,?,?, '','',?, ?,1,?)`, superuser, username, email, superuser, now())
		if err != nil {
			return err
		}
		user, err := store.user(cmd.Context(), "SELECT * FROM auth_user WHERE username=?", username)
		if err != nil {
			return err
		}
		if superuser {
			if _, err = store.DB.ExecContext(cmd.Context(), "UPDATE auth_user SET is_superuser=1,is_staff=1 WHERE id=?", user.ID); err != nil {
				return err
			}
		}
		if email != "" {
			if _, err = store.DB.ExecContext(cmd.Context(), "UPDATE auth_user SET email=? WHERE id=?", email, user.ID); err != nil {
				return err
			}
		}
		link, err := store.userAccessURL(cmd.Context(), user.ID, false)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), link)
		return nil
	})
	access.Flags().StringVar(&email, "email", "", "account email address")
	access.Flags().BoolVar(&superuser, "superuser", false, "grant administrative permissions")
	add("listaccess", "Print existing account login links", 0, func(cmd *cobra.Command, args []string) error {
		rows, err := store.Query(cmd.Context(), "SELECT u.username,p.access_token FROM reinhardt_preferences p JOIN auth_user u ON u.id=p.owner_id ORDER BY u.username")
		if err != nil {
			return err
		}
		for _, user := range rows {
			fmt.Fprintln(cmd.OutOrStdout(), user.S("username"), store.Config.AccessURLPrefix+store.URL("invite")+"?q="+user.S("access_token"))
		}
		return nil
	})
	add("listtags", "List tags and their owners", 0, func(cmd *cobra.Command, args []string) error {
		rows, err := store.Query(cmd.Context(), "SELECT t.*,u.username FROM reinhardt_tag t JOIN auth_user u ON u.id=t.owner_id ORDER BY u.username,t.label")
		if err != nil {
			return err
		}
		for _, tag := range rows {
			fmt.Fprintf(cmd.OutOrStdout(), "%3d\t%s (%s)\n", tag.N("id"), tag.S("label"), tag.S("username"))
		}
		return nil
	})
	add("fetchtagfiles <tag_id> <destination>", "Copy a playlist's files in ranked order", 2, func(cmd *cobra.Command, args []string) error {
		info, err := os.Stat(args[1])
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("destination must be a directory")
		}
		if _, err = store.One(cmd.Context(), "SELECT id FROM reinhardt_tag WHERE id=?", args[0]); err != nil {
			return err
		}
		tracks, err := store.TagTracks(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		for index, track := range tracks {
			dest := filepath.Join(args[1], fmt.Sprintf("%03d-%s", index+1, filepath.Base(track.FullPath)))
			file, err := os.Open(track.FullPath)
			if err != nil {
				return err
			}
			err = atomicFile(dest, file, true)
			file.Close()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), dest)
		}
		return nil
	})
	add("tagsexport <csv_file_output>", "Export indexed metadata as CSV", 1, func(cmd *cobra.Command, args []string) error {
		tracks, err := store.Tracks(cmd.Context(), "ORDER BY m.artist,m.album,m.title,m.track_number")
		if err != nil {
			return err
		}
		outputFile, err := os.Create(args[0])
		if err != nil {
			return err
		}
		defer outputFile.Close()
		writer := csv.NewWriter(outputFile)
		writer.Write([]string{"path", "artist", "title", "album", "genre"})
		for _, track := range tracks {
			writer.Write([]string{track.FullPath, track.Artist, track.Title, track.Album, track.Genre})
		}
		writer.Flush()
		return writer.Error()
	})
	add("populateyears", "Populate only the year column from audio metadata", 0, func(cmd *cobra.Command, args []string) error {
		tracks, err := store.Tracks(cmd.Context(), "")
		if err != nil {
			return err
		}
		count := 0
		for _, track := range tracks {
			metadata, err := store.Metadata(track.FullPath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %s: %v\n", track.FullPath, err)
				continue
			}
			if _, err = store.DB.ExecContext(cmd.Context(), "UPDATE reinhardt_musicfile SET year=? WHERE id=?", metadata.Year, track.ID); err != nil {
				return err
			}
			count++
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated year for %d music files.\n", count)
		return nil
	})
	add("dedup", "Print duplicate music paths by content without deleting files", 0, func(cmd *cobra.Command, args []string) error {
		dirs, err := store.Query(cmd.Context(), "SELECT local_path FROM reinhardt_musicdirectory ORDER BY id")
		if err != nil {
			return err
		}
		hashes := map[string]bool{}
		for _, musicDirectory := range dirs {
			err = walkMusic(musicDirectory.S("local_path"), func(path string) error {
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				hash := md5.New()
				_, err = io.Copy(hash, file)
				file.Close()
				if err != nil {
					return err
				}
				key := hex.EncodeToString(hash.Sum(nil))
				if hashes[key] {
					fmt.Fprintln(cmd.OutOrStdout(), path)
				}
				hashes[key] = true
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	add("jamasync", "Upload every indexed track to the configured Jama project", 0, func(cmd *cobra.Command, args []string) error {
		app, err := newApp(store)
		if err != nil {
			return err
		}
		rows, err := store.Query(cmd.Context(), "SELECT id FROM reinhardt_musicfile ORDER BY id DESC")
		if err != nil {
			return err
		}
		for _, track := range rows {
			if err = app.syncJama(cmd.Context(), track.N("id")); err != nil {
				return err
			}
		}
		return nil
	})
	add("dbxrefreshtoken", "Exchange a Dropbox authorization code for a refresh token", 0, func(cmd *cobra.Command, args []string) error {
		app, err := newApp(store)
		if err != nil {
			return err
		}
		if store.Config.DropboxKey == "" || store.Config.DropboxSecret == "" {
			return fmt.Errorf("DBX_KEY and DBX_SECRET are required")
		}
		values := url.Values{
			"client_id":         {store.Config.DropboxKey},
			"response_type":     {"code"},
			"token_access_type": {"offline"},
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Open https://www.dropbox.com/oauth2/authorize?"+values.Encode())
		fmt.Fprint(cmd.OutOrStdout(), "Authorization code: ")
		scanner := bufio.NewScanner(cmd.InOrStdin())
		if !scanner.Scan() {
			return fmt.Errorf("authorization code is required")
		}
		data, err := app.dropboxToken(cmd.Context(), "authorization_code", strings.TrimSpace(scanner.Text()))
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Access token:", data["access_token"])
		fmt.Fprintln(cmd.OutOrStdout(), "Refresh token:", data["refresh_token"])
		fmt.Fprintln(cmd.OutOrStdout(), "Account ID:", data["account_id"])
		return nil
	})
	var album, genre, year, comment string
	ytrip := add("ytrip <url> <artist> <title>", "Download and import a YouTube track", 3, func(cmd *cobra.Command, args []string) error {
		app, err := newApp(store)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
		defer cancel()
		return app.youtubeRip(ctx, Row{"url": args[0], "artist": args[1], "title": args[2], "album": album, "genre": genre, "year": year, "comment": comment})
	})
	ytrip.Flags().StringVarP(&album, "album", "a", "", "album name")
	ytrip.Flags().StringVarP(&genre, "genre", "g", "", "genre")
	ytrip.Flags().StringVarP(&year, "year", "y", "", "release year")
	ytrip.Flags().StringVar(&comment, "comment", "", "comment")
	add("jobs", "List queued and failed background jobs", 0, func(cmd *cobra.Command, args []string) error {
		rows, err := store.Query(cmd.Context(), "SELECT * FROM go_job ORDER BY id")
		if err != nil {
			return err
		}
		for _, job := range rows {
			fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\tattempts=%d\t%s\n", job.N("id"), job.S("kind"), job.N("attempts"), job.S("error"))
		}
		return nil
	})
	return root, closeStore
}
