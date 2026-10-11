package reinhardt

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func decodeGzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func TestGzipResponsesPreserveHTML(t *testing.T) {
	a := testApp(t)
	for _, fragment := range []bool{false, true} {
		plain := dropdownRequest(a, fragment, false)
		requireCode(t, plain, 200)
		for range 3 {
			compressed := dropdownRequest(a, fragment, true)
			requireCode(t, compressed, 200)
			if compressed.Header().Get("Content-Encoding") != "gzip" || compressed.Header().Get("Content-Type") != plain.Header().Get("Content-Type") {
				t.Fatal("compressed response headers changed")
			}
			decoded, err := decodeGzip(compressed.Body.Bytes())
			if err != nil || !bytes.Equal(decoded, plain.Body.Bytes()) {
				t.Fatal("compressed HTML differs from uncompressed HTML", err)
			}
		}
	}
}

func TestGzipConcurrentIndependentStreams(t *testing.T) {
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for response := range 8 {
				body := []byte(strings.Repeat(fmt.Sprintf("response %d/%d: <html>&</html>\n", worker, response), 300+response))
				var compressed bytes.Buffer
				if err := writeGzip(&compressed, body); err != nil {
					errors <- err
					return
				}
				decoded, err := decodeGzip(compressed.Bytes())
				if err != nil || !bytes.Equal(decoded, body) {
					errors <- fmt.Errorf("corrupt response %d/%d: %v", worker, response, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

type failedGzipDestination struct {
	err error
}

func (w failedGzipDestination) Write([]byte) (int, error) { return 0, w.err }

func TestGzipRecoveryAfterWriteFailure(t *testing.T) {
	wantErr := errors.New("destination failed")
	if err := writeGzip(failedGzipDestination{wantErr}, []byte("broken response")); !errors.Is(err, wantErr) {
		t.Fatal("compression did not propagate the write failure", err)
	}
	body := []byte(strings.Repeat("next response", 100))
	var compressed bytes.Buffer
	if err := writeGzip(&compressed, body); err != nil {
		t.Fatal("compression failed after a previous error", err)
	}
	decoded, err := decodeGzip(compressed.Bytes())
	if err != nil || !bytes.Equal(decoded, body) {
		t.Fatal("response after write failure was corrupted", err)
	}
}
