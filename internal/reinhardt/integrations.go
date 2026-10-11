package reinhardt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func (app *App) login(responseWriter http.ResponseWriter, request *http.Request, user Row) error {
	next := request.FormValue("next")
	ctx := TemplateContext{"form": formHTML(next, app.translate("email"), app.Store.Config["LANGUAGE_CODE"])}
	if request.Method == "POST" {
		email := strings.TrimSpace(request.FormValue("email"))
		if parsed, err := mail.ParseAddress(email); err != nil || parsed.Address != email {
			message := "Enter a valid email address."
			if email == "" {
				message = "This field is required."
			}
			if strings.HasPrefix(app.Store.Config["LANGUAGE_CODE"], "fr") {
				message = "Saisissez une adresse de courriel valide."
				if email == "" {
					message = "Ce champ est obligatoire."
				}
			}
			ctx["form"] = template.HTML(`<div class="alert alert-danger">` + message + `</div>` + string(formHTML(next, app.translate("email"), app.Store.Config["LANGUAGE_CODE"])))
			return app.render(responseWriter, request, user, "registration/login.html", ctx, 200)
		}
		user, err := app.Store.One(`SELECT * FROM auth_user WHERE lower(email)=lower(?) OR lower(username)=lower(?) LIMIT 1`, email, email)
		if err == nil {
			prefs, err := app.Store.Preferences(user.N("id"))
			if err != nil {
				return err
			}
			access := prefs.S("access_token")
			if access == "" {
				access = token()
				if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET access_token=? WHERE id=?", access, prefs.N("id")); err != nil {
					return err
				}
			}
			link := app.absolute(request, app.Store.URL("invite")+"?"+inviteQuery(access, next))
			recipient := user.S("email")
			if recipient == "" {
				recipient = email
			}
			subject := "Your Reinhardt login link"
			body := "Use this link to log in: " + link
			if strings.HasPrefix(app.Store.Config["LANGUAGE_CODE"], "fr") {
				subject = "Votre lien de connexion Reinhardt"
				body = "Utilisez ce lien pour vous connecter : " + link
			}
			if err = sendMail(app.Store.Config, recipient, subject, body); err != nil {
				return err
			}
		} else if err != sql.ErrNoRows {
			return err
		}
		// Give unknown addresses the same confirmation so the login page does not
		// disclose whether an account exists.
		ctx["email_sent"] = true
	}
	return app.render(responseWriter, request, user, "registration/login.html", ctx, 200)
}
func sendMail(config Config, to, subject, body string) error {
	from, err := mail.ParseAddress(config["DEFAULT_FROM_EMAIL"])
	if err != nil {
		return err
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(config["EMAIL_HOST"], config["EMAIL_PORT"])
	var conn net.Conn
	if config.Bool("EMAIL_USE_SSL") {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{ServerName: config["EMAIL_HOST"], MinVersion: tls.VersionTLS12})
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var client *smtp.Client
	if config.Bool("EMAIL_USE_TLS") && !config.Bool("EMAIL_USE_SSL") {
		client, err = smtp.NewClientStartTLS(conn, &tls.Config{ServerName: config["EMAIL_HOST"], MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
	} else {
		client = smtp.NewClient(conn)
	}
	defer client.Close()
	if config["EMAIL_HOST_USER"] != "" {
		if err = client.Auth(sasl.NewPlainClient("", config["EMAIL_HOST_USER"], config["EMAIL_HOST_PASSWORD"])); err != nil {
			return err
		}
	}
	if err = client.Mail(from.Address, nil); err != nil {
		return err
	}
	if err = client.Rcpt(recipient.Address, nil); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.String(), recipient.String(), base64.StdEncoding.EncodeToString([]byte(subject)), body)
	if _, err = io.WriteString(writer, message); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
func (app *App) request(ctx context.Context, method, endpoint string, headers map[string]string, body io.Reader) ([]byte, int, error) {
	// This helper buffers small API/image responses under a body timeout. Audio
	// must use the streaming client instead, preserving ranges and long transfers.
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	for headerName, headerValue := range headers {
		request.Header.Set(headerName, headerValue)
	}
	resp, err := app.Client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("remote service HTTP %d: %.1000s", resp.StatusCode, responseBody)
	}
	return responseBody, resp.StatusCode, nil
}
func (app *App) syncJama(ctx context.Context, id int64) error {
	config := app.Store.Config
	if config["JAMA_ENDPOINT"] == "" || config["JAMA_KEY"] == "" || config["JAMA_PROJECT"] == "" {
		return fmt.Errorf("JAMA_ENDPOINT, JAMA_KEY and JAMA_PROJECT are required")
	}
	track, err := app.Store.Track(id)
	if err != nil {
		return err
	}
	hash, err := hashFile(track.S("full_path"))
	if err != nil {
		return err
	}
	project, err := strconv.Atoi(config["JAMA_PROJECT"])
	if err != nil {
		return err
	}
	data, _ := json.Marshal(Row{"method": "upload_infos", "params": []any{hash, project}, "id": 1})
	responseBody, _, err := app.request(ctx, "POST", config["JAMA_ENDPOINT"], map[string]string{"X-Api-Key": config["JAMA_KEY"], "Content-Type": "application/json"}, bytes.NewReader(data))
	if err != nil {
		return err
	}
	var info struct {
		Error  any
		Result struct {
			Status          string
			AvailableChunks []string `json:"available_chunks"`
		}
	}
	if err = json.Unmarshal(responseBody, &info); err != nil {
		return err
	}
	if info.Error != nil {
		return fmt.Errorf("Jama: %v", info.Error)
	}
	if info.Result.Status != "available" {
		file, err := os.Open(track.S("full_path"))
		if err != nil {
			return err
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil {
			return err
		}
		const chunkSize = 1024 * 1024
		chunks := (stat.Size() + chunkSize - 1) / chunkSize
		available := map[int64]bool{}
		for _, name := range info.Result.AvailableChunks {
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			_, chunkNumberText, ok := strings.Cut(stem, "-")
			if ok {
				chunkNumber, _ := strconv.ParseInt(chunkNumberText, 10, 64)
				available[chunkNumber] = true
			}
		}
		for chunkIndex := int64(0); chunkIndex < chunks; chunkIndex++ {
			if available[chunkIndex] {
				continue
			}
			size := int64(chunkSize)
			if rem := stat.Size() - chunkIndex*chunkSize; rem < size {
				size = rem
			}
			headers := map[string]string{"X-Project": config["JAMA_PROJECT"], "X-Api-Key": config["JAMA_KEY"], "X-file-chunk": fmt.Sprintf("%d/%d", chunkIndex, chunks), "X-file-hash": hash, "X-file-name": base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(filepath.Base(track.S("full_path")))))}
			_, status, err := app.request(ctx, "POST", strings.TrimRight(config["JAMA_ENDPOINT"], "/")+"/upload/partial/", headers, io.NewSectionReader(file, chunkIndex*chunkSize, size))
			if err != nil {
				return err
			}
			if status == 200 {
				break
			}
			if status != 202 {
				return fmt.Errorf("unexpected Jama upload status %d", status)
			}
		}
	}
	_, err = app.Store.DB.Exec("UPDATE reinhardt_musicfile SET hash=? WHERE id=?", hash, id)
	return err
}

func (app *App) dropboxToken(ctx context.Context, grant, code string) (map[string]any, error) {
	config := app.Store.Config
	form := url.Values{"grant_type": {grant}, "client_id": {config["DBX_KEY"]}, "client_secret": {config["DBX_SECRET"]}}
	if grant == "refresh_token" {
		form.Set("refresh_token", code)
	} else {
		form.Set("code", code)
	}
	responseBody, _, err := app.request(ctx, "POST", "https://api.dropboxapi.com/oauth2/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	var data map[string]any
	err = json.Unmarshal(responseBody, &data)
	return data, err
}
func (app *App) syncDropbox(ctx context.Context, id int64) error {
	track, err := app.Store.Track(id)
	if err != nil {
		return err
	}
	data, err := app.dropboxToken(ctx, "refresh_token", app.Store.Config["DBX_TOKEN"])
	if err != nil {
		return err
	}
	access, _ := data["access_token"].(string)
	if access == "" {
		return fmt.Errorf("Dropbox did not return an access token")
	}
	file, err := os.Open(track.S("full_path"))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	call := func(method string, args any, reader io.Reader) ([]byte, error) {
		argumentsJSON, _ := json.Marshal(args)
		out, _, err := app.request(ctx, "POST", "https://content.dropboxapi.com/2/files/"+method, map[string]string{"Authorization": "Bearer " + access, "Content-Type": "application/octet-stream", "Dropbox-API-Arg": string(argumentsJSON)}, reader)
		return out, err
	}
	responseBody, err := call("upload_session/start", Row{"close": false}, nil)
	if err != nil {
		return err
	}
	var session struct {
		ID string `json:"session_id"`
	}
	if err = json.Unmarshal(responseBody, &session); err != nil {
		return err
	}
	if session.ID == "" {
		return fmt.Errorf("Dropbox did not return a session ID")
	}
	const chunkSize int64 = 4 * 1024 * 1024
	offset := int64(0)
	for info.Size()-offset > chunkSize {
		if _, err = call("upload_session/append_v2", Row{"cursor": Row{"session_id": session.ID, "offset": offset}, "close": false}, io.NewSectionReader(file, offset, chunkSize)); err != nil {
			return err
		}
		offset += chunkSize
	}
	_, err = call("upload_session/finish", Row{"cursor": Row{"session_id": session.ID, "offset": offset}, "commit": Row{"path": "/music_collection/" + filepath.ToSlash(track.S("relative_path")), "mode": "overwrite", "autorename": false, "mute": true}}, io.NewSectionReader(file, offset, info.Size()-offset))
	return err
}

func (app *App) similarArtists(ctx context.Context, artist string) []string {
	// Artist pages use this too, independently of autofeed preferences. Playback
	// must check its preference before calling; do not put that gate in this helper.
	result := []string{}
	if strings.TrimSpace(artist) == "" {
		return result
	}
	similar := app.cachedSimilarNames(ctx, artist)
	if len(similar) == 0 {
		return result
	}
	names, err := app.libraryArtistNames(ctx)
	if err != nil {
		return result
	}
	seen := map[string]bool{}
	for _, name := range similar {
		key := slug(app.Store.Config.sanitize(name, "artist"))
		if name, ok := names[key]; ok && !seen[name] && !strings.EqualFold(name, artist) {
			result = append(result, name)
			seen[name] = true
		}
	}
	return result
}
