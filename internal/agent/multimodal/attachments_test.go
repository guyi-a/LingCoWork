package multimodal

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestBuildUserMessageDisabledFallsBackToOCRMarker(t *testing.T) {
	got := BuildUserMessage("[image: /tmp/example.png]\nWhat is shown?", false)
	if got.Role != schema.User {
		t.Fatalf("role = %q, want user", got.Role)
	}
	if got.Content != "[file: /tmp/example.png]\nWhat is shown?" {
		t.Fatalf("content = %q", got.Content)
	}
	if len(got.UserInputMultiContent) != 0 {
		t.Fatal("disabled multimodal unexpectedly created content blocks")
	}
}

func TestBuildUserMessageCreatesOrderedNativeImageParts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pixel.png")
	data := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("payload")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	got := BuildUserMessage("Before\n[image: "+path+"]\nAfter", true)
	if got.Content != "" || got.Role != schema.User {
		t.Fatalf("unexpected message: %#v", got)
	}
	if len(got.UserInputMultiContent) != 3 {
		t.Fatalf("parts = %d, want 3", len(got.UserInputMultiContent))
	}
	if got.UserInputMultiContent[0].Text != "Before\n" ||
		got.UserInputMultiContent[2].Text != "\nAfter" {
		t.Fatalf("text order = %#v", got.UserInputMultiContent)
	}
	image := got.UserInputMultiContent[1].Image
	if image == nil || image.MIMEType != "image/png" ||
		image.Detail != schema.ImageURLDetailAuto ||
		image.Base64Data == nil {
		t.Fatalf("image part = %#v", image)
	}
	decoded, err := base64.StdEncoding.DecodeString(*image.Base64Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != string(data) {
		t.Fatalf("decoded image differs: %x", decoded)
	}
}

func TestBuildUserMessageRejectsSpoofedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.png")
	if err := os.WriteFile(path, []byte{0xff, 0xd8, 0xff, 0x00}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	got := BuildUserMessage("[image: "+path+"]", true)
	if len(got.UserInputMultiContent) != 0 {
		t.Fatal("spoofed image unexpectedly became a native image part")
	}
	if !strings.Contains(got.Content, "image content is image/jpeg but extension declares image/png") {
		t.Fatalf("fallback = %q", got.Content)
	}
}

func TestBuildUserMessageFallsBackForMissingAndOversizedImages(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.png")
	got := BuildUserMessage("[image: "+missing+"]", true)
	if !strings.Contains(got.Content, "file not found") {
		t.Fatalf("missing fallback = %q", got.Content)
	}

	path := filepath.Join(t.TempDir(), "large.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create large image: %v", err)
	}
	if err := file.Truncate(MaxImageBytes + 1); err != nil {
		t.Fatalf("truncate large image: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close large image: %v", err)
	}
	got = BuildUserMessage("[image: "+path+"]", true)
	if !strings.Contains(got.Content, "too large") {
		t.Fatalf("oversized fallback = %q", got.Content)
	}
}

func TestImageBudgetLimitsCountAndTotalBytes(t *testing.T) {
	countBudget := NewImageBudget()
	for i := 0; i < MaxImagesPerRequest; i++ {
		if err := countBudget.consume(1); err != nil {
			t.Fatalf("consume image %d: %v", i, err)
		}
	}
	if err := countBudget.consume(1); err == nil ||
		!strings.Contains(err.Error(), "count limit") {
		t.Fatalf("count limit error = %v", err)
	}

	byteBudget := NewImageBudget()
	if err := byteBudget.consume(MaxImageBytesPerRequest); err != nil {
		t.Fatalf("consume byte budget: %v", err)
	}
	if err := byteBudget.consume(1); err == nil ||
		!strings.Contains(err.Error(), "budget exceeded") {
		t.Fatalf("byte limit error = %v", err)
	}
}

func TestImageMimeFromContent(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff}, "image/jpeg"},
		{"gif", []byte("GIF89a"), "image/gif"},
		{"webp", []byte("RIFFxxxxWEBP"), "image/webp"},
		{"unknown", []byte("not an image"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageMimeFromContent(tt.data); got != tt.want {
				t.Fatalf("mime = %q, want %q", got, tt.want)
			}
		})
	}
}
