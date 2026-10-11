package reinhardt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type delayedAudioBody struct {
	ctx     context.Context
	data    *strings.Reader
	delayed bool
	closed  bool
}

func (b *delayedAudioBody) Read(p []byte) (int, error) {
	if !b.delayed {
		b.delayed = true
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.data.Read(p)
}

func (b *delayedAudioBody) Close() error { b.closed = true; return nil }

func TestDelegatedAudioOutlivesAPITimeout(t *testing.T) {
	a := testApp(t)
	a.S.C["DELEGATE_DOWNLOAD_PREFIX"] = "https://audio.example/library"
	a.Client.Timeout = 5 * time.Millisecond
	var body *delayedAudioBody
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Range") != "bytes=3-6" {
			return nil, fmt.Errorf("wrong streaming headers: %v", r.Header)
		}
		body = &delayedAudioBody{ctx: r.Context(), data: strings.NewReader("defg")}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Length": {"4"}, "Content-Range": {"bytes 3-6/26"}}, Body: body}, nil
	})
	a.Client.Transport = transport
	a.AudioClient.Transport = transport
	r := httptest.NewRequest("GET", "/stream/1/", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	r.Header.Set("Range", "bytes=3-6")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 206)
	if w.Body.String() != "defg" || body == nil || !body.closed || w.Header().Get("Content-Length") != "4" {
		t.Fatal("audio body was truncated by the API timeout", w.Body.String())
	}
}

func TestDelegatedAudioCancellationClosesUpstream(t *testing.T) {
	a := testApp(t)
	a.S.C["DELEGATE_DOWNLOAD_PREFIX"] = "https://audio.example/library"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var body *delayedAudioBody
	a.AudioClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body = &delayedAudioBody{ctx: r.Context(), data: strings.NewReader("audio")}
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {"5"}}, Body: body}, nil
	})
	r := httptest.NewRequest("GET", "/stream/1/", nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if body == nil || !body.closed || w.Body.Len() != 0 {
		t.Fatal("cancelled stream did not stop and close its upstream")
	}
}
