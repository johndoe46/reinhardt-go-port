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

func (a *App) login(w http.ResponseWriter, r *http.Request, u Row) error {
	next := r.FormValue("next")
	ctx := TemplateContext{"form": formHTML(next, a.translate("email"), a.S.C["LANGUAGE_CODE"])}
	if r.Method == "POST" {
		email := strings.TrimSpace(r.FormValue("email"))
		if parsed, e := mail.ParseAddress(email); e != nil || parsed.Address != email {
			message := "Enter a valid email address."
			if email == "" {
				message = "This field is required."
			}
			if strings.HasPrefix(a.S.C["LANGUAGE_CODE"], "fr") {
				message = "Saisissez une adresse de courriel valide."
				if email == "" {
					message = "Ce champ est obligatoire."
				}
			}
			ctx["form"] = template.HTML(`<div class="alert alert-danger">` + message + `</div>` + string(formHTML(next, a.translate("email"), a.S.C["LANGUAGE_CODE"])))
			return a.render(w, r, u, "registration/login.html", ctx, 200)
		}
		user, e := a.S.One(`SELECT * FROM auth_user WHERE lower(email)=lower(?) OR lower(username)=lower(?) LIMIT 1`, email, email)
		if e == nil {
			prefs, e := a.S.Preferences(user.N("id"))
			if e != nil {
				return e
			}
			access := prefs.S("access_token")
			if access == "" {
				access = token()
				if _, e = a.S.DB.Exec("UPDATE reinhardt_preferences SET access_token=? WHERE id=?", access, prefs.N("id")); e != nil {
					return e
				}
			}
			link := a.absolute(r, a.S.URL("invite")+"?"+inviteQuery(access, next))
			recipient := user.S("email")
			if recipient == "" {
				recipient = email
			}
			subject := "Your Reinhardt login link"
			body := "Use this link to log in: " + link
			if strings.HasPrefix(a.S.C["LANGUAGE_CODE"], "fr") {
				subject = "Votre lien de connexion Reinhardt"
				body = "Utilisez ce lien pour vous connecter : " + link
			}
			if e = sendMail(a.S.C, recipient, subject, body); e != nil {
				return e
			}
		} else if e != sql.ErrNoRows {
			return e
		}
		// Give unknown addresses the same confirmation so the login page does not
		// disclose whether an account exists.
		ctx["email_sent"] = true
	}
	return a.render(w, r, u, "registration/login.html", ctx, 200)
}
func sendMail(c Config, to, subject, body string) error {
	from, e := mail.ParseAddress(c["DEFAULT_FROM_EMAIL"])
	if e != nil {
		return e
	}
	recipient, e := mail.ParseAddress(to)
	if e != nil {
		return e
	}
	addr := net.JoinHostPort(c["EMAIL_HOST"], c["EMAIL_PORT"])
	var conn net.Conn
	if c.Bool("EMAIL_USE_SSL") {
		conn, e = tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{ServerName: c["EMAIL_HOST"], MinVersion: tls.VersionTLS12})
	} else {
		conn, e = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if e != nil {
		return e
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var client *smtp.Client
	if c.Bool("EMAIL_USE_TLS") && !c.Bool("EMAIL_USE_SSL") {
		client, e = smtp.NewClientStartTLS(conn, &tls.Config{ServerName: c["EMAIL_HOST"], MinVersion: tls.VersionTLS12})
		if e != nil {
			return e
		}
	} else {
		client = smtp.NewClient(conn)
	}
	defer client.Close()
	if c["EMAIL_HOST_USER"] != "" {
		if e = client.Auth(sasl.NewPlainClient("", c["EMAIL_HOST_USER"], c["EMAIL_HOST_PASSWORD"])); e != nil {
			return e
		}
	}
	if e = client.Mail(from.Address, nil); e != nil {
		return e
	}
	if e = client.Rcpt(recipient.Address, nil); e != nil {
		return e
	}
	writer, e := client.Data()
	if e != nil {
		return e
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.String(), recipient.String(), base64.StdEncoding.EncodeToString([]byte(subject)), body)
	if _, e = io.WriteString(writer, message); e != nil {
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	return client.Quit()
}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (a *App) request(ctx context.Context, method, endpoint string, headers map[string]string, body io.Reader) ([]byte, int, error) {
	// This helper buffers small API/image responses under a body timeout. Audio
	// must use the streaming client instead, preserving ranges and long transfers.
	r, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return nil, 0, e
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, e := a.Client.Do(r)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if e != nil {
		return nil, resp.StatusCode, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("remote service HTTP %d: %.1000s", resp.StatusCode, b)
	}
	return b, resp.StatusCode, nil
}
func (a *App) syncJama(ctx context.Context, id int64) error {
	c := a.S.C
	if c["JAMA_ENDPOINT"] == "" || c["JAMA_KEY"] == "" || c["JAMA_PROJECT"] == "" {
		return fmt.Errorf("JAMA_ENDPOINT, JAMA_KEY and JAMA_PROJECT are required")
	}
	m, e := a.S.Track(id)
	if e != nil {
		return e
	}
	hash, e := hashFile(m.S("full_path"))
	if e != nil {
		return e
	}
	project, e := strconv.Atoi(c["JAMA_PROJECT"])
	if e != nil {
		return e
	}
	data, _ := json.Marshal(Row{"method": "upload_infos", "params": []any{hash, project}, "id": 1})
	b, _, e := a.request(ctx, "POST", c["JAMA_ENDPOINT"], map[string]string{"X-Api-Key": c["JAMA_KEY"], "Content-Type": "application/json"}, bytes.NewReader(data))
	if e != nil {
		return e
	}
	var info struct {
		Error  any
		Result struct {
			Status          string
			AvailableChunks []string `json:"available_chunks"`
		}
	}
	if e = json.Unmarshal(b, &info); e != nil {
		return e
	}
	if info.Error != nil {
		return fmt.Errorf("Jama: %v", info.Error)
	}
	if info.Result.Status != "available" {
		f, e := os.Open(m.S("full_path"))
		if e != nil {
			return e
		}
		defer f.Close()
		stat, e := f.Stat()
		if e != nil {
			return e
		}
		const chunkSize = 1024 * 1024
		chunks := (stat.Size() + chunkSize - 1) / chunkSize
		available := map[int64]bool{}
		for _, name := range info.Result.AvailableChunks {
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			_, n, ok := strings.Cut(stem, "-")
			if ok {
				i, _ := strconv.ParseInt(n, 10, 64)
				available[i] = true
			}
		}
		for i := int64(0); i < chunks; i++ {
			if available[i] {
				continue
			}
			size := int64(chunkSize)
			if rem := stat.Size() - i*chunkSize; rem < size {
				size = rem
			}
			headers := map[string]string{"X-Project": c["JAMA_PROJECT"], "X-Api-Key": c["JAMA_KEY"], "X-file-chunk": fmt.Sprintf("%d/%d", i, chunks), "X-file-hash": hash, "X-file-name": base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(filepath.Base(m.S("full_path")))))}
			_, status, e := a.request(ctx, "POST", strings.TrimRight(c["JAMA_ENDPOINT"], "/")+"/upload/partial/", headers, io.NewSectionReader(f, i*chunkSize, size))
			if e != nil {
				return e
			}
			if status == 200 {
				break
			}
			if status != 202 {
				return fmt.Errorf("unexpected Jama upload status %d", status)
			}
		}
	}
	_, e = a.S.DB.Exec("UPDATE reinhardt_musicfile SET hash=? WHERE id=?", hash, id)
	return e
}

func (a *App) dropboxToken(ctx context.Context, grant, code string) (map[string]any, error) {
	c := a.S.C
	form := url.Values{"grant_type": {grant}, "client_id": {c["DBX_KEY"]}, "client_secret": {c["DBX_SECRET"]}}
	if grant == "refresh_token" {
		form.Set("refresh_token", code)
	} else {
		form.Set("code", code)
	}
	b, _, e := a.request(ctx, "POST", "https://api.dropboxapi.com/oauth2/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, strings.NewReader(form.Encode()))
	if e != nil {
		return nil, e
	}
	var data map[string]any
	e = json.Unmarshal(b, &data)
	return data, e
}
func (a *App) syncDropbox(ctx context.Context, id int64) error {
	m, e := a.S.Track(id)
	if e != nil {
		return e
	}
	data, e := a.dropboxToken(ctx, "refresh_token", a.S.C["DBX_TOKEN"])
	if e != nil {
		return e
	}
	access, _ := data["access_token"].(string)
	if access == "" {
		return fmt.Errorf("Dropbox did not return an access token")
	}
	f, e := os.Open(m.S("full_path"))
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	call := func(method string, args any, reader io.Reader) ([]byte, error) {
		b, _ := json.Marshal(args)
		out, _, e := a.request(ctx, "POST", "https://content.dropboxapi.com/2/files/"+method, map[string]string{"Authorization": "Bearer " + access, "Content-Type": "application/octet-stream", "Dropbox-API-Arg": string(b)}, reader)
		return out, e
	}
	b, e := call("upload_session/start", Row{"close": false}, nil)
	if e != nil {
		return e
	}
	var session struct {
		ID string `json:"session_id"`
	}
	if e = json.Unmarshal(b, &session); e != nil {
		return e
	}
	if session.ID == "" {
		return fmt.Errorf("Dropbox did not return a session ID")
	}
	const chunkSize int64 = 4 * 1024 * 1024
	offset := int64(0)
	for info.Size()-offset > chunkSize {
		if _, e = call("upload_session/append_v2", Row{"cursor": Row{"session_id": session.ID, "offset": offset}, "close": false}, io.NewSectionReader(f, offset, chunkSize)); e != nil {
			return e
		}
		offset += chunkSize
	}
	_, e = call("upload_session/finish", Row{"cursor": Row{"session_id": session.ID, "offset": offset}, "commit": Row{"path": "/music_collection/" + filepath.ToSlash(m.S("relative_path")), "mode": "overwrite", "autorename": false, "mute": true}}, io.NewSectionReader(f, offset, info.Size()-offset))
	return e
}

func (a *App) similarArtists(ctx context.Context, artist string) []string {
	// Artist pages use this too, independently of autofeed preferences. Playback
	// must check its preference before calling; do not put that gate in this helper.
	result := []string{}
	if strings.TrimSpace(artist) == "" {
		return result
	}
	similar := a.cachedSimilarNames(ctx, artist)
	if len(similar) == 0 {
		return result
	}
	names, e := a.libraryArtistNames(ctx)
	if e != nil {
		return result
	}
	seen := map[string]bool{}
	for _, name := range similar {
		key := slug(a.S.C.sanitize(name, "artist"))
		if name, ok := names[key]; ok && !seen[name] && !strings.EqualFold(name, artist) {
			result = append(result, name)
			seen[name] = true
		}
	}
	return result
}
