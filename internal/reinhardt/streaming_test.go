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

func (body *delayedAudioBody) Read(data []byte) (int, error) {
	if !body.delayed {
		body.delayed = true
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-body.ctx.Done():
			return 0, body.ctx.Err()
		}
	}
	return body.data.Read(data)
}

func (body *delayedAudioBody) Close() error { body.closed = true; return nil }

func TestDelegatedAudioOutlivesAPITimeout(test *testing.T) {
	app := testApp(test)
	app.Store.Config.DelegateDownloadPrefix = "https://audio.example/library"
	app.Client.Timeout = 5 * time.Millisecond
	var body *delayedAudioBody
	transport := transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("Range") != "bytes=3-6" {
			return nil, fmt.Errorf("wrong streaming headers: %v", request.Header)
		}
		body = &delayedAudioBody{ctx: request.Context(), data: strings.NewReader("defg")}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Length": {"4"}, "Content-Range": {"bytes 3-6/26"}}, Body: body}, nil
	})
	app.Client.Transport = transport
	app.AudioClient.Transport = transport
	request := httptest.NewRequest("GET", "/stream/1/", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.Header.Set("Range", "bytes=3-6")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 206)
	if response.Body.String() != "defg" || body == nil || !body.closed || response.Header().Get("Content-Length") != "4" {
		test.Fatal("audio body was truncated by the API timeout", response.Body.String())
	}
}

func TestDelegatedAudioCancellationClosesUpstream(test *testing.T) {
	app := testApp(test)
	app.Store.Config.DelegateDownloadPrefix = "https://audio.example/library"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var body *delayedAudioBody
	app.AudioClient.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		body = &delayedAudioBody{ctx: request.Context(), data: strings.NewReader("audio")}
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {"5"}}, Body: body}, nil
	})
	request := httptest.NewRequest("GET", "/stream/1/", nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if body == nil || !body.closed || response.Body.Len() != 0 {
		test.Fatal("cancelled stream did not stop and close its upstream")
	}
}
