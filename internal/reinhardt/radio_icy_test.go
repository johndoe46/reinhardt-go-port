package reinhardt

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestRadioRelayLegacyICYResponsePreservesSemicolonAndAudio(test *testing.T) {
	for _, fixture := range []struct {
		name       string
		status     string
		fragmented bool
		wantCode   int
	}{
		{"shoutcast", "ICY 200 OK", false, 200},
		{"fragmented shoutcast", "ICY 200 OK", true, 200},
		{"standard HTTP", "HTTP/1.1 200 OK", false, 200},
		{"upstream rejection", "ICY 403 Forbidden", false, 502},
		{"invalid protocol", "ICYS 200 OK", false, 502},
	} {
		test.Run(fixture.name, func(test *testing.T) {
			app := testApp(test)
			const streamURL = "http://79.111.119.111:9057/;"
			const audio = "\xff\xf1\x00audio\x00ICY 200 OK\r\nunchanged"
			form := url.Values{"name": {"SHOUTcast"}, "country": {"France"}, "url": {streamURL}}
			requireCode(test, call(app, "POST", "/radio/new/", 1, form, true), 302)
			station, err := app.Store.One(test.Context(), "SELECT id,url FROM go_radio_station WHERE owner_id=1")
			if err != nil || station.S("url") != streamURL {
				test.Fatal("station form changed the semicolon URL", station, err)
			}
			client := newRadioClient()
			defer client.CloseIdleConnections()
			transport := client.Transport.(*http.Transport)
			result := make(chan error, 1)
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("79.111.119.111")}, nil
			}
			dial := func(_ context.Context, _, address string) (net.Conn, error) {
				if address != "79.111.119.111:9057" {
					return nil, fmt.Errorf("unexpected dial address %s", address)
				}
				left, right := net.Pipe()
				test.Cleanup(func() { left.Close(); right.Close() })
				go func() {
					defer right.Close()
					request, err := http.ReadRequest(bufio.NewReader(right))
					if err != nil {
						result <- err
						return
					}
					if request.RequestURI != "/;" || request.Header.Get("Icy-Metadata") != "0" {
						result <- fmt.Errorf("unexpected stream request: %s, headers %v", request.RequestURI, request.Header)
						return
					}
					response := fixture.status + "\r\nContent-Type: audio/aacp\r\nConnection: close\r\n\r\n" + audio
					if fixture.fragmented {
						for _, fragment := range []string{"I", "CY", " ", response[4:]} {
							if _, err = right.Write([]byte(fragment)); err != nil {
								break
							}
						}
					} else {
						_, err = right.Write([]byte(response))
					}
					result <- err
				}()
				return left, nil
			}
			transport.DialContext = radioDialContext(lookup, dial)
			app.RadioClient = client
			response := call(app, "GET", fmt.Sprintf("/radio/%d/audio/", station.N("id")), 1, nil, false)
			requireCode(test, response, fixture.wantCode)
			if fixture.wantCode == 200 {
				if response.Body.String() != audio || response.Header().Get("Content-Type") != "audio/aacp" {
					test.Fatal("relay changed the AAC audio or content type")
				}
			}
			select {
			case err := <-result:
				if err != nil && fixture.wantCode == 200 {
					test.Fatal(err)
				}
			case <-test.Context().Done():
				test.Fatal("upstream did not finish")
			}
		})
	}
}

func TestRadioLegacyICYOnlyNormalizesInitialStatusLine(test *testing.T) {
	// TLS records and non-HTTP data must pass through byte-for-byte so the
	// standard transport can perform TLS verification or reject invalid input.
	for _, data := range []string{"\x16\x03\x03\x00\x06ICY ", "HTTP/1.1 200 OK\r\n\r\nICY 200 OK", "ICYS invalid", "I", ""} {
		test.Run(fmt.Sprintf("prefix=%x", data[:min(4, len(data))]), func(test *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			go func() {
				defer right.Close()
				_, _ = right.Write([]byte(data))
			}()
			conn := &radioConn{Conn: left}
			var output strings.Builder
			buffer := make([]byte, 3)
			for {
				count, err := conn.Read(buffer)
				output.Write(buffer[:count])
				if err != nil {
					break
				}
			}
			if output.String() != data {
				test.Fatal("socket adapter changed non-ICY data")
			}
		})
	}
}
