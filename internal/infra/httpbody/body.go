// Package httpbody composes replayable HTTP bodies without joining media into a second buffer.
package httpbody

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
)

type segment struct {
	data []byte
	text string
}

// Body owns the small framing fragments and references read-only media until the request finishes.
type Body struct {
	parts       []segment
	size        int64
	ContentType string
}

func (b *Body) addBytes(data []byte) {
	b.parts = append(b.parts, segment{data: data})
	b.size += int64(len(data))
}

func (b *Body) addString(text string) {
	b.parts = append(b.parts, segment{text: text})
	b.size += int64(len(text))
}

func (b *Body) open() (io.ReadCloser, error) {
	readers := make([]io.Reader, 0, len(b.parts))
	for _, part := range b.parts {
		if part.data != nil {
			readers = append(readers, bytes.NewReader(part.data))
		} else {
			readers = append(readers, strings.NewReader(part.text))
		}
	}
	return io.NopCloser(io.MultiReader(readers...)), nil
}

// NewRequest restores the length and replay behavior net/http otherwise infers only for a single buffer.
func (b *Body) NewRequest(ctx context.Context, method, url string) (*http.Request, error) {
	if b == nil {
		return http.NewRequestWithContext(ctx, method, url, nil)
	}
	reader, _ := b.open()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.ContentLength = b.size
	req.GetBody = b.open
	req.Header.Set("Content-Type", b.ContentType)
	return req, nil
}
