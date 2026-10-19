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
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	return io.ReadAll(gzipReader)
}

func TestGzipResponsesPreserveHTML(test *testing.T) {
	app := testApp(test)
	for _, fragment := range []bool{false, true} {
		plain := dropdownRequest(app, fragment, false)
		requireCode(test, plain, 200)
		for range 3 {
			compressed := dropdownRequest(app, fragment, true)
			requireCode(test, compressed, 200)
			if compressed.Header().Get("Content-Encoding") != "gzip" || compressed.Header().Get("Content-Type") != plain.Header().Get("Content-Type") {
				test.Fatal("compressed response headers changed")
			}
			decoded, err := decodeGzip(compressed.Body.Bytes())
			if err != nil || !bytes.Equal(decoded, plain.Body.Bytes()) {
				test.Fatal("compressed HTML differs from uncompressed HTML", err)
			}
		}
	}
}

func TestGzipConcurrentIndependentStreams(test *testing.T) {
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
		test.Error(err)
	}
}

type failedGzipDestination struct {
	err error
}

func (destination failedGzipDestination) Write([]byte) (int, error) { return 0, destination.err }

func TestGzipRecoveryAfterWriteFailure(test *testing.T) {
	wantErr := errors.New("destination failed")
	if err := writeGzip(failedGzipDestination{wantErr}, []byte("broken response")); !errors.Is(err, wantErr) {
		test.Fatal("compression did not propagate the write failure", err)
	}
	body := []byte(strings.Repeat("next response", 100))
	var compressed bytes.Buffer
	if err := writeGzip(&compressed, body); err != nil {
		test.Fatal("compression failed after a previous error", err)
	}
	decoded, err := decodeGzip(compressed.Bytes())
	if err != nil || !bytes.Equal(decoded, body) {
		test.Fatal("response after write failure was corrupted", err)
	}
}
