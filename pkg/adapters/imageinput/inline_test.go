package imageinput

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func dataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func TestInlineImagesNeverUseTheNetwork(t *testing.T) {
	fetcher := Fetcher{TrustedOnly: true, Resolve: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("inline image reached the resolver")
		return nil, errors.New("unexpected")
	}}
	workspace := t.TempDir()
	set, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: dataURL("IMAGE/PNG", pngBytes)}})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Cleanup()
	if len(set.Images) != 1 || set.Images[0].MediaType != "image/png" || !strings.HasSuffix(set.Images[0].Path, ".png") {
		t.Fatalf("images %+v", set.Images)
	}
	if data, _ := os.ReadFile(set.Images[0].Path); !bytes.Equal(data, pngBytes) {
		t.Fatal("inline bytes changed")
	}
}

func TestInlineImagesAreValidated(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{1}, 32)...)
	big := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{0}, 2048)...)
	cases := map[string]string{
		dataURL("image/svg+xml", []byte("<svg/>")):                      CodeInvalid,
		"data:image/png," + base64.StdEncoding.EncodeToString(pngBytes): CodeInvalid,
		"data:image/png;charset=utf-8;base64,AAAA":                      CodeInvalid,
		"data:text/plain;base64,aGVsbG8=":                               CodeInvalid,
		"data:image/png;base64,***not-base64***":                        CodeInvalid,
		"data:image/png;base64,":                                        CodeInvalid,
		dataURL("image/png", jpeg):                                      CodeUnsupportedType,
		dataURL("image/png", []byte("<html>")):                          CodeUnsupportedType,
		dataURL("image/png", big):                                       CodeTooLarge,
	}
	for link, code := range cases {
		workspace := t.TempDir()
		_, err := (Fetcher{MaxBytes: 1024}).Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: link}})
		expectCode(t, err, code)
		if _, statErr := os.Stat(workspace + "/" + DirectoryName); !os.IsNotExist(statErr) {
			t.Fatalf("invalid inline image created files: %s", link[:min(len(link), 40)])
		}
	}
	// A huge payload is rejected before decoding.
	huge := "data:image/png;base64," + strings.Repeat("A", 4096)
	_, err := (Fetcher{MaxBytes: 1024}).Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: huge}})
	expectCode(t, err, CodeTooLarge)
}

func TestRedactReplacesOnlyInlineImages(t *testing.T) {
	value := map[string]any{
		"image_url": dataURL("image/png", pngBytes), "link_image_url": "https://example.com/a.png",
		"nested": []any{map[string]any{"x": "DATA:image/jpeg;base64,AAAA"}}, "note": "data: not an image", "n": 3.0,
		"other": "data:text/plain;base64,aGk=",
	}
	redacted := Redact(value).(map[string]any)
	if redacted["image_url"] != inlinePlaceholder || redacted["link_image_url"] != "https://example.com/a.png" ||
		redacted["nested"].([]any)[0].(map[string]any)["x"] != inlinePlaceholder || redacted["note"] != "data: not an image" ||
		redacted["n"] != 3.0 || redacted["other"] != "data:text/plain;base64,aGk=" {
		t.Fatalf("redacted %v", redacted)
	}
	if value["image_url"] == inlinePlaceholder {
		t.Fatal("Redact mutated its input")
	}
}
