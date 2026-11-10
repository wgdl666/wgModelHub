package httpbody

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMultipartPreservesFieldsFilesAndReplay(t *testing.T) {
	image := []byte{0, 255, 1, 2}
	body, err := Multipart([]Part{Field("image", "YWJj"), File("image", "a.png", "image/png", image), File("image", "b.jpg", "image/jpeg", image)})
	if err != nil {
		t.Fatal(err)
	}
	req, err := body.NewRequest(context.Background(), "POST", "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if req.ContentLength != int64(len(raw)) {
		t.Fatalf("length=%d bytes=%d", req.ContentLength, len(raw))
	}
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	for i, filename := range []string{"", "a.png", "b.jpg"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		want := image
		if i == 0 {
			want = []byte("YWJj")
		}
		if part.FormName() != "image" || part.FileName() != filename || !bytes.Equal(got, want) {
			t.Fatalf("part %d: name=%s filename=%s data=%v", i, part.FormName(), part.FileName(), got)
		}
		if wantMIME := []string{"", "image/png", "image/jpeg"}[i]; part.Header.Get("Content-Type") != wantMIME {
			t.Fatalf("part %d MIME=%s want=%s", i, part.Header.Get("Content-Type"), wantMIME)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("extra part: %v", err)
	}
	for range 2 {
		replay, err := req.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(replay)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatal("replayed multipart differs")
		}
	}
}

func TestMultipartDispositionMatchesStandardWriter(t *testing.T) {
	for _, name := range []string{"model", `image\"part`, "line\r\nbreak"} {
		var reference bytes.Buffer
		writer := multipart.NewWriter(&reference)
		if err := writer.WriteField(name, "gpt-image-2"); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.CreateFormFile(name, `ref\"photo.png`); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(bytes.NewReader(reference.Bytes()), writer.Boundary())
		for _, generated := range []Part{Field(name, "gpt-image-2"), File(name, `ref\"photo.png`, "application/octet-stream", []byte("png"))} {
			part, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			if got, want := generated.Header.Get("Content-Disposition"), part.Header.Get("Content-Disposition"); got != want {
				t.Fatalf("Content-Disposition=%q want=%q", got, want)
			}
		}
	}
}

func TestJSONMatchesStandardEncoding(t *testing.T) {
	values := []any{
		map[string]any{"image": string(bytes.Repeat([]byte("YWJj+/="), 100)), "text": "引号\"\\\n\x00<&>\u2028", "raw": json.RawMessage(`{"x":"<tag>"}`), "number": 1.25},
		map[string]any{"maps": []map[string]any{{"a": "x"}}, "array": []any{true, nil, 2}, "strings": []string{"a", "b"}, "bytes": []byte{0, 255}},
		map[string]string{"image": "data:image/png;base64,YWJj", "name": "a\"b"},
		map[string]any{"nil": map[string]any(nil), "bytes": []byte(nil), "array": []any(nil), "strings": []string(nil), "map_strings": map[string]string(nil)},
		struct {
			Name string `json:"name,omitempty"`
		}{Name: "<test>"},
	}
	for _, escapeHTML := range []bool{true, false} {
		for _, value := range values {
			var expected bytes.Buffer
			encoder := json.NewEncoder(&expected)
			encoder.SetEscapeHTML(escapeHTML)
			if err := encoder.Encode(value); err != nil {
				t.Fatal(err)
			}
			body, err := JSON(value, escapeHTML)
			if err != nil {
				t.Fatal(err)
			}
			req, err := body.NewRequest(context.Background(), "POST", "https://example.test")
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.TrimSuffix(expected.Bytes(), []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Fatalf("escapeHTML=%v\ngot %s\nwant %s", escapeHTML, got, want)
			}
			if int64(len(got)) != req.ContentLength {
				t.Fatal("incorrect ContentLength")
			}
			replay, err := req.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			again, err := io.ReadAll(replay)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, again) {
				t.Fatal("replayed JSON differs")
			}
		}
	}
	if _, err := JSON(map[string]any{"invalid": make(chan int)}, true); err == nil {
		t.Fatal("unsupported values must return an error")
	}
}

func TestRequestCanReplayAcross307Redirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/last", http.StatusTemporaryRedirect)
			return
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || string(got) != `{"image":"YWJj"}` || r.ContentLength != int64(len(got)) {
			t.Errorf("replay: method=%s length=%d body=%s", r.Method, r.ContentLength, got)
		}
	}))
	defer server.Close()
	body, err := JSON(map[string]any{"image": "YWJj"}, true)
	if err != nil {
		t.Fatal(err)
	}
	req, err := body.NewRequest(context.Background(), "POST", server.URL+"/first")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
}
