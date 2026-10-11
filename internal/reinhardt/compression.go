package reinhardt

import (
	"compress/gzip"
	"io"
	"sync"
)

var gzipWriters = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

func writeGzip(dst io.Writer, data []byte) error {
	writer := gzipWriters.Get().(*gzip.Writer)
	writer.Reset(dst)
	defer func() {
		// Detach the response buffer even after an error so the pool only retains
		// compressor state. Reset also clears errors before the next response.
		writer.Reset(io.Discard)
		gzipWriters.Put(writer)
	}()
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return writer.Close()
}
