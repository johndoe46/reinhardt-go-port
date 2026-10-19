package reinhardt

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func radioWireClient(test *testing.T, response string, certificate *tls.Certificate) *http.Client {
	test.Helper()
	client := newRadioClient()
	test.Cleanup(client.CloseIdleConnections)
	transport := client.Transport.(*http.Transport)
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		left, right := net.Pipe()
		test.Cleanup(func() { left.Close(); right.Close() })
		go func() {
			defer right.Close()
			var conn net.Conn = right
			if certificate != nil {
				conn = tls.Server(right, &tls.Config{Certificates: []tls.Certificate{*certificate}})
			}
			if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
				return
			}
			_, _ = io.WriteString(conn, response)
			if secure, ok := conn.(*tls.Conn); ok {
				_ = secure.CloseWrite()
			}
		}()
		return left, nil
	}
	transport.DialContext = radioDialContext(lookup, dial)
	return client
}

func TestRadioRelayTransferEncodingCompatibility(test *testing.T) {
	const audio = "\xff\xfbMP3\x00Transfer-Encoding: identity\r\nunchanged"
	app := testApp(test)
	id := mustRadioStation(test, app, 1, "RTBF", "Belgium", "http://radio.example/live")
	for _, fixture := range []struct {
		name, headers, body string
		wantCode            int
	}{
		{"identity", "Transfer-Encoding: identity\r\n", audio, 200},
		{"case and spacing", "tRaNsFeR-EnCoDiNg:\tIDENTITY \t\r\n", audio, 200},
		{"content length", fmt.Sprintf("Transfer-Encoding: identity\r\nContent-Length: %d\r\n", len(audio)), audio + "extra", 200},
		{"long header", "X-Pad: " + strings.Repeat("x", 4096-len("X-Pad: ")) + "\r\nTransfer-Encoding: identity\r\n", audio, 200},
		{"oversized headers", "X-Pad: " + strings.Repeat("x", maxRadioResponseHeaders) + "\r\nTransfer-Encoding: identity\r\n", audio, 502},
		{"chunked", "Transfer-Encoding: chunked\r\n", fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(audio), audio), 200},
		{"unsupported", "Transfer-Encoding: gzip\r\n", audio, 502},
		{"combined", "Transfer-Encoding: identity, chunked\r\n", audio, 502},
		{"duplicate", "Transfer-Encoding: identity\r\nTransfer-Encoding: identity\r\n", audio, 502},
		{"folded", "Transfer-Encoding: identity\r\n chunked\r\n", audio, 502},
	} {
		test.Run(fixture.name, func(test *testing.T) {
			response := "HTTP/1.1 200 OK\r\nContent-Type: audio/mpeg\r\nConnection: close\r\n" + fixture.headers + "\r\n" + fixture.body
			app.RadioClient = radioWireClient(test, response, nil)
			result := call(app, "GET", fmt.Sprintf("/radio/%d/audio/", id), 1, nil, false)
			requireCode(test, result, fixture.wantCode)
			if fixture.wantCode == 200 && result.Body.String() != audio {
				test.Fatal("relay modified the MP3 audio")
			}
		})
	}
}

func TestRadioHTTPSCompatibilityPreservesCertificateVerification(test *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		test.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"radio.example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		test.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	trusted, err := x509.ParseCertificate(der)
	if err != nil {
		test.Fatal(err)
	}
	for _, fixture := range []struct {
		name, host, status, headers string
		trust, success              bool
	}{
		{"identity over TLS", "radio.example", "HTTP/1.1 200 OK", "Transfer-Encoding: identity\r\n", true, true},
		{"ICY over TLS", "radio.example", "ICY 200 OK", "", true, true},
		{"untrusted certificate", "radio.example", "HTTP/1.1 200 OK", "", false, false},
		{"incorrect hostname", "other.example", "HTTP/1.1 200 OK", "", true, false},
	} {
		test.Run(fixture.name, func(test *testing.T) {
			client := radioWireClient(test, fixture.status+"\r\nContent-Type: audio/mpeg\r\nConnection: close\r\n"+fixture.headers+"\r\nMP3 audio", &certificate)
			roots := x509.NewCertPool()
			if fixture.trust {
				roots.AddCert(trusted)
			}
			client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
			request, _ := http.NewRequestWithContext(test.Context(), "GET", "https://"+fixture.host+"/live", nil)
			response, err := client.Do(request)
			if !fixture.success {
				if err == nil {
					response.Body.Close()
					test.Fatal("accepted an invalid server certificate")
				}
				if !strings.Contains(err.Error(), "certificate") {
					test.Fatal("failed before certificate verification", err)
				}
				return
			}
			if err != nil {
				test.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "MP3 audio" || response.StatusCode != 200 {
				test.Fatal("TLS relay did not preserve the stream", response.StatusCode, string(body), err)
			}
		})
	}
}
