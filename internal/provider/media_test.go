package provider

import (
	"encoding/base64"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestResolveImageURLKeepsHTTPURI(t *testing.T) {
	const uri = "https://cdn.example/frame.png"
	got, err := ResolveImageURL(&modelhubv2.Media{
		MimeType: "image/png",
		Source:   &modelhubv2.Media_Uri{Uri: uri},
	})
	if err != nil || got != uri {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveImageURLEncodesInlineBytes(t *testing.T) {
	const png = "fake-png"
	got, err := ResolveImageURL(&modelhubv2.Media{
		MimeType: "image/PNG",
		Source:   &modelhubv2.Media_Data{Data: []byte(png)},
	})
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("got=%q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != png {
		t.Fatalf("decoded=%q", decoded)
	}
}

func TestImageDataURIRequiresMime(t *testing.T) {
	_, err := ImageDataURI("", []byte("png"))
	if err == nil || Kind(err) != ErrorInvalidArgument || !strings.Contains(err.Error(), "mime_type is required") {
		t.Fatalf("err=%v", err)
	}
	if !IsNotAttempted(err) {
		t.Fatal("ImageDataURI local validation must be NotAttempted")
	}
}

func TestResolveImageURLLocalFailuresAreNotAttempted(t *testing.T) {
	_, err := ResolveImageURL(nil)
	if !IsNotAttempted(err) {
		t.Fatalf("nil media: %v", err)
	}
	_, err = ResolveImageURL(&modelhubv2.Media{MimeType: "text/plain", Source: &modelhubv2.Media_Data{Data: []byte("x")}})
	if !IsNotAttempted(err) {
		t.Fatalf("bad mime: %v", err)
	}
}

func TestParseDataURIRoundTrip(t *testing.T) {
	encoded, err := ImageDataURI("image/jpeg", []byte("jpeg-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	mime, data, err := ParseDataURI(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" || string(data) != "jpeg-bytes" {
		t.Fatalf("mime=%q data=%q", mime, data)
	}
}
