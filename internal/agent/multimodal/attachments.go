// Package multimodal builds LLM-ready schema.Message values that carry
// multimodal content (images today; audio / video / file parts later).
//
// The one call site today is service/chat.go, both when sending a fresh
// user turn and when hydrating history for replay. The single source of
// truth for how [image: /abs/path] markers become image content parts
// lives here so the two paths never drift.
package multimodal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// MaxImageBytes caps a single inlined image at 5 MiB. Above this we
// degrade to an "image not available" text hint so a huge screenshot
// can't blow up a chat request (base64 inflates ~33% on top of the raw
// bytes, so 5 MiB on disk becomes ~6.7 MiB in the wire payload).
const MaxImageBytes = 5 * 1024 * 1024

const (
	// Keep enough headroom below DeepSeek's 48 MiB JSON request limit for
	// base64 expansion (~4/3), text, tool schemas and protocol overhead.
	MaxImagesPerRequest     = 20
	MaxImageBytesPerRequest = 30 * 1024 * 1024
)

// ImageBudget is shared by every user message assembled for one model
// request. It prevents individually-valid historical images from combining
// into an oversized chat/completions body.
type ImageBudget struct {
	images int
	bytes  int
}

func NewImageBudget() *ImageBudget {
	return &ImageBudget{}
}

func (b *ImageBudget) consume(size int) error {
	if b == nil {
		return nil
	}
	if b.images >= MaxImagesPerRequest {
		return fmt.Errorf("request image count limit reached (%d)", MaxImagesPerRequest)
	}
	if b.bytes+size > MaxImageBytesPerRequest {
		return fmt.Errorf("request image budget exceeded (%d MiB)", MaxImageBytesPerRequest/(1024*1024))
	}
	b.images++
	b.bytes += size
	return nil
}

// imageMarkerRE matches a full line "[image: /abs/path]" — same shape as
// [file:] / [folder:] markers, kept multiline-anchored so a marker-shaped
// substring inside prose isn't accidentally lifted out.
var imageMarkerRE = regexp.MustCompile(`(?m)^\[image:\s*(.+?)\]\s*$`)

// BuildUserMessage turns a persisted user text — which may contain
// [image: /abs/path] markers among the prose and other markers — into an
// LLM-ready schema.Message.
//
// multimodalEnabled controls whether image markers are expanded into
// native image content blocks (true) or downgraded to [file:] markers so
// the model reads them as text via extract_document_text / OCR (false).
// The downgrade path is what lets a text-only model still "see" the
// image contents in a lossy way.
//
// When multimodal is enabled and images load successfully, the result is
// a multipart message (UserInputMultiContent) whose parts are:
//   - text run before/between images (kept verbatim, including any
//     [file:] / [folder:] markers which the model still resolves via tools)
//   - one image_url part per successfully-loaded [image:] marker, carrying
//     the base64 file bytes
//   - a final text run after the last marker
//
// Images that fail to load (missing file, oversized, unsupported type) are
// inlined as "[image not available: <basename> (<reason>)]" text at their
// original position so the model still knows something was meant to be
// there and why it isn't.
//
// When no image markers exist (or multimodal is disabled), returns the
// equivalent plain schema.UserMessage — a plain text message that any
// text-only model can still handle.
func BuildUserMessage(rawText string, multimodalEnabled bool) *schema.Message {
	return BuildUserMessageWithBudget(rawText, multimodalEnabled, NewImageBudget())
}

// BuildUserMessageWithBudget is BuildUserMessage with a request-scoped image
// budget. Callers assembling history should share one budget across all user
// messages in the request.
func BuildUserMessageWithBudget(
	rawText string,
	multimodalEnabled bool,
	budget *ImageBudget,
) *schema.Message {
	if !multimodalEnabled {
		// Rewrite [image: ...] → [file: ...] so the model treats the path
		// like any other attached file and dispatches to the OCR-capable
		// extract_document_text reader (see prompts/general.go for the
		// extension → reader mapping).
		rewritten := imageMarkerRE.ReplaceAllString(rawText, "[file: $1]")
		return schema.UserMessage(rewritten)
	}
	matches := imageMarkerRE.FindAllStringSubmatchIndex(rawText, -1)
	if len(matches) == 0 {
		return schema.UserMessage(rawText)
	}

	parts := make([]schema.MessageInputPart, 0, len(matches)*2+1)
	var textBuf strings.Builder
	cursor := 0

	flushText := func() {
		if textBuf.Len() == 0 {
			return
		}
		s := textBuf.String()
		textBuf.Reset()
		// Preserve leading/trailing whitespace between parts — Claude
		// tolerates it and stripping might glue "[img] 描述" into "[img]描述"
		// on some renderings. Only skip completely-empty text parts.
		if strings.TrimSpace(s) == "" {
			return
		}
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: s,
		})
	}

	for _, m := range matches {
		start, end := m[0], m[1]
		pathStart, pathEnd := m[2], m[3]
		if start > cursor {
			textBuf.WriteString(rawText[cursor:start])
		}
		path := strings.TrimSpace(rawText[pathStart:pathEnd])
		base := filepath.Base(path)

		data, mime, err := loadInlineImage(path)
		if err == nil {
			err = budget.consume(len(data))
		}
		if err != nil {
			textBuf.WriteString(fmt.Sprintf("[image not available: %s (%s)]", base, err))
			log.Printf("multimodal.BuildUserMessage: skip image %s: %v", path, err)
			cursor = end
			continue
		}

		// Successfully loaded — split the accumulated text at this point
		// so the image lands in the flow at the right position.
		flushText()
		b64 := base64.StdEncoding.EncodeToString(data)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &b64,
					MIMEType:   mime,
				},
				Detail: schema.ImageURLDetailAuto,
			},
		})
		cursor = end
	}
	if cursor < len(rawText) {
		textBuf.WriteString(rawText[cursor:])
	}
	flushText()

	// If every image failed to load, the message ended up as pure text —
	// collapse back into a plain schema.UserMessage to keep the wire shape
	// simple (and avoid a multipart request that carries no images).
	hasImage := false
	for _, p := range parts {
		if p.Type == schema.ChatMessagePartTypeImageURL {
			hasImage = true
			break
		}
	}
	if !hasImage {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == schema.ChatMessagePartTypeText {
				b.WriteString(p.Text)
			}
		}
		return schema.UserMessage(b.String())
	}

	return &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	}
}

// loadInlineImage reads an image file for inline delivery. Enforces the
// size cap and rejects unknown extensions. Returns (bytes, mimeType) on
// success.
func loadInlineImage(path string) ([]byte, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("path is not absolute")
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("file not found")
		}
		return nil, "", err
	}
	if st.IsDir() {
		return nil, "", fmt.Errorf("is a directory")
	}
	if st.Size() > MaxImageBytes {
		return nil, "", fmt.Errorf("too large: %d KiB > %d KiB cap",
			st.Size()/1024, MaxImageBytes/1024)
	}
	declaredMIME := imageMimeFromExt(filepath.Ext(path))
	if declaredMIME == "" {
		return nil, "", fmt.Errorf("unsupported image type %q", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	actualMIME := imageMimeFromContent(data)
	if actualMIME == "" {
		return nil, "", fmt.Errorf("unsupported image content")
	}
	if declaredMIME != actualMIME {
		return nil, "", fmt.Errorf("image content is %s but extension declares %s", actualMIME, declaredMIME)
	}
	return data, actualMIME, nil
}

func imageMimeFromContent(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

// imageMimeFromExt maps a file extension to the wire MIME type for image
// content parts. Keep in sync with what the target vision model accepts —
// Claude / Anthropic today take png / jpeg / webp / gif; other models are
// stricter but this set is a safe common denominator.
func imageMimeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return ""
	}
}
