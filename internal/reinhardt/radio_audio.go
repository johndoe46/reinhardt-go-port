package reinhardt

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var radioBlockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
	// Restrict IPv6 to public global unicast; translation prefixes can encode
	// otherwise blocked IPv4 destinations.
}

func publicRadioIP(address netip.Addr) bool {
	address = address.Unmap()
	if address.Zone() != "" || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, prefix := range radioBlockedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func parseRadioURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || parsed == nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" {
		return nil, fmt.Errorf("invalid radio URL")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, fmt.Errorf("invalid radio port")
		}
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if address, err := netip.ParseAddr(host); err == nil {
		if !publicRadioIP(address) {
			return nil, fmt.Errorf("radio destination is not public")
		}
	} else if !strings.Contains(host, ".") || strings.ContainsAny(host, "%:/\\") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") {
		return nil, fmt.Errorf("radio destination is not public")
	}
	return parsed, nil
}

type radioLookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type radioDialFunc func(context.Context, string, string) (net.Conn, error)

const maxRadioResponseHeaders = 64 << 10

// Resolve once and dial only validated numeric addresses. Validating a hostname
// before a second DNS lookup would permit DNS rebinding to private addresses.
func radioDialContext(lookup radioLookupFunc, dial radioDialFunc) radioDialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("radio destination has no addresses")
		}
		for _, address := range addresses {
			if !publicRadioIP(address) {
				return nil, fmt.Errorf("radio destination is not public")
			}
		}
		for _, address := range addresses {
			var conn net.Conn
			conn, err = dial(ctx, network, net.JoinHostPort(address.String(), port))
			if err == nil {
				return &radioConn{Conn: conn}, nil
			}
		}
		return nil, err
	}
}

// Refresh the read deadline for each network read, rather than limiting the
// lifetime of a healthy live stream. A dead upstream eventually frees resources.
type radioConn struct {
	net.Conn
	reader io.Reader
}

func (conn *radioConn) Read(data []byte) (int, error) {
	if conn.reader == nil {
		reader := bufio.NewReader(radioDeadlineReader{conn.Conn})
		if prefix, _ := reader.Peek(4); string(prefix) == "ICY " || string(prefix) == "HTTP" {
			headers, err := radioResponseHeaders(reader)
			if err != nil {
				return 0, err
			}
			conn.reader = io.MultiReader(strings.NewReader(headers), reader)
		} else {
			// TLS records and non-HTTP data pass through unchanged.
			conn.reader = reader
		}
	}
	return conn.reader.Read(data)
}

func radioResponseHeaders(reader *bufio.Reader) (string, error) {
	var headers strings.Builder
	fragmentedLine := false
	for {
		line, err := reader.ReadSlice('\n')
		if headers.Len()+len(line) > maxRadioResponseHeaders {
			return "", fmt.Errorf("radio response headers too large")
		}
		headers.Write(line)
		if err == bufio.ErrBufferFull {
			fragmentedLine = true
			continue
		}
		if err != nil {
			return "", err
		}
		if !fragmentedLine && (string(line) == "\r\n" || string(line) == "\n") {
			break
		}
		fragmentedLine = false
	}
	lines := strings.SplitAfter(headers.String(), "\n")
	// Older SHOUTcast servers use ICY instead of an HTTP version.
	if strings.HasPrefix(lines[0], "ICY ") {
		lines[0] = "HTTP/1.0 " + strings.TrimPrefix(lines[0], "ICY ")
	}
	transferIndex, transferCount := -1, 0
	for index, line := range lines[1:] {
		name, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(name, "Transfer-Encoding") {
			transferCount++
			value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
			if strings.EqualFold(strings.Trim(value, " \t"), "identity") {
				transferIndex = index + 1
			}
		}
	}
	// RTBF/QuantumCast sends this obsolete no-op encoding. Remove only a
	// single, unfolded identity header; Go still validates all other framing.
	if transferCount == 1 && transferIndex >= 0 && !strings.HasPrefix(lines[transferIndex+1], " ") && !strings.HasPrefix(lines[transferIndex+1], "\t") {
		lines[transferIndex] = ""
	}
	return strings.Join(lines, ""), nil
}

type radioDeadlineReader struct{ conn net.Conn }

func (reader radioDeadlineReader) Read(data []byte) (int, error) {
	if err := reader.conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return 0, err
	}
	return reader.conn.Read(data)
}

func newRadioClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: radioDialContext(net.DefaultResolver.LookupNetIP, dialer.DialContext),
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 10, MaxIdleConnsPerHost: 2,
		DisableCompression: true,
		// Each response starts on a fresh connection so the legacy status
		// adapter never examines audio or a reused connection's later response.
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: maxRadioResponseHeaders,
		// HTTP/1 gives every stream an independent connection and read deadline.
		ForceAttemptHTTP2: false,
	}
	// Normalize HTTPS headers after a verified TLS handshake, as well as
	// plaintext HTTP headers. Never relax certificate or hostname checks.
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := transport.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			conn.Close()
			return nil, err
		}
		config := &tls.Config{}
		if transport.TLSClientConfig != nil {
			config = transport.TLSClientConfig.Clone()
		}
		if config.ServerName == "" {
			config.ServerName = host
		}
		config.NextProtos = []string{"http/1.1"}
		tlsConn := tls.Client(conn, config)
		handshakeContext, cancel := context.WithTimeout(ctx, transport.TLSHandshakeTimeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(handshakeContext); err != nil {
			conn.Close()
			return nil, err
		}
		return &radioConn{Conn: tlsConn}, nil
	}
	return &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many radio redirects")
		}
		_, err := parseRadioURL(request.URL.String())
		return err
	}}
}

func (app *App) radioAudio(responseWriter http.ResponseWriter, request *http.Request, station *RadioStation) error {
	if _, err := parseRadioURL(station.URL); err != nil {
		return httpError{502, "Radio station unavailable."}
	}
	upstream, err := http.NewRequestWithContext(request.Context(), request.Method, station.URL, nil)
	if err != nil {
		return err
	}
	// Some stations reject Go's generic client identity (for example SomaFM).
	upstream.Header.Set("User-Agent", "Reinhardt/1.0")
	upstream.Header.Set("Accept-Encoding", "identity")
	// Do not request ICY metadata: interleaved metadata is not audio. Title
	// discovery can be added later using a separate, bounded metadata reader.
	upstream.Header.Set("Icy-MetaData", "0")
	resp, err := app.RadioClient.Do(upstream)
	if err != nil {
		if request.Context().Err() != nil {
			return nil
		}
		return httpError{502, "Radio station unavailable."}
	}
	defer resp.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "audio/mpeg", "audio/mp3", "audio/aac", "audio/aacp", "audio/ogg", "application/ogg", "audio/flac", "audio/x-flac", "audio/wav", "audio/x-wav", "audio/webm", "audio/mp4":
	default:
		return httpError{502, "This station does not provide a supported direct audio stream."}
	}
	if resp.StatusCode != http.StatusOK || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return httpError{502, "Radio station unavailable."}
	}
	if rawInterval := resp.Header.Get("Icy-Metaint"); rawInterval != "" {
		// Some servers explicitly send zero when no metadata is interleaved.
		// Only positive intervals introduce metadata bytes into the audio body.
		interval, err := strconv.ParseUint(strings.TrimSpace(rawInterval), 10, 64)
		if err != nil || interval != 0 {
			return httpError{502, "Radio station unavailable."}
		}
	}
	responseWriter.Header().Set("Content-Type", mediaType)
	responseWriter.Header().Set("Cache-Control", "private, no-store")
	responseWriter.Header().Set("Accept-Ranges", "none")
	responseWriter.Header().Set("X-Accel-Buffering", "no")
	responseWriter.Header().Set("X-Content-Type-Options", "nosniff")
	responseWriter.WriteHeader(http.StatusOK)
	if request.Method == "HEAD" {
		return nil
	}
	controller := http.NewResponseController(responseWriter)
	buffer := make([]byte, 32*1024)
	for {
		if request.Context().Err() != nil {
			return nil
		}
		count, readErr := resp.Body.Read(buffer)
		if count > 0 {
			// A listener that stops reading must not hold an upstream forever.
			_ = controller.SetWriteDeadline(time.Now().Add(45 * time.Second))
			if _, err = responseWriter.Write(buffer[:count]); err != nil {
				return nil
			}
			if err = controller.Flush(); err != nil && err != http.ErrNotSupported {
				return nil
			}
		}
		if readErr != nil {
			if readErr != io.EOF && request.Context().Err() == nil {
				log.Printf("radio station %d: %v", station.ID, readErr)
			}
			return nil
		}
	}
}
