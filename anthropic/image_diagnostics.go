package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"strings"

	// Registered for their image.DecodeConfig side effects.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// imageDiagnostic describes one inline image found in a request body.
type imageDiagnostic struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type,omitempty"`
	Format    string `json:"format,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Bytes     int    `json:"bytes"`
	Problem   string `json:"problem,omitempty"`
}

// upstreamRejectedImages reports whether an upstream error message blames an
// image, so the expensive decode below only runs when it can explain a failure.
func upstreamRejectedImages(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("image"))
}

// describeRequestImages decodes every inline base64 image in an Anthropic
// request and reports its real dimensions. Upstream only says which message
// index it rejected, which is not enough to tell whether the image is
// zero-sized, oversized, or simply not the format its media type claims.
func describeRequestImages(reqBody []byte) []imageDiagnostic {
	var payload struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqBody, &payload); err != nil {
		return nil
	}

	var out []imageDiagnostic
	for msgIdx, msg := range payload.Messages {
		var blocks []struct {
			Type   string `json:"type"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for blockIdx, block := range blocks {
			if block.Type != "image" || block.Source.Type != "base64" {
				continue
			}
			out = append(out, describeImageBlock(
				fmt.Sprintf("messages.%d.content.%d", msgIdx, blockIdx),
				block.Source.MediaType,
				block.Source.Data,
			))
		}
	}
	return out
}

func describeImageBlock(path, mediaType, data string) imageDiagnostic {
	d := imageDiagnostic{Path: path, MediaType: mediaType}

	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Some clients emit data URLs or padded/newline-wrapped base64.
		cleaned := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(data)
		if idx := strings.Index(cleaned, ","); idx >= 0 && strings.HasPrefix(cleaned, "data:") {
			cleaned = cleaned[idx+1:]
		}
		raw, err = base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			d.Problem = "base64 decode failed"
			return d
		}
	}
	d.Bytes = len(raw)
	if len(raw) == 0 {
		d.Problem = "empty image payload"
		return d
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		d.Problem = "decode config failed: " + err.Error()
		return d
	}
	d.Format = format
	d.Width = cfg.Width
	d.Height = cfg.Height

	switch {
	case cfg.Width == 0 || cfg.Height == 0:
		d.Problem = "zero dimension"
	case mediaType != "" && !mediaTypeMatchesFormat(mediaType, format):
		d.Problem = fmt.Sprintf("media_type %q does not match decoded format %q", mediaType, format)
	}
	return d
}

func mediaTypeMatchesFormat(mediaType, format string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return format == "jpeg"
	case "image/png":
		return format == "png"
	case "image/gif":
		return format == "gif"
	case "image/webp":
		// webp is not in the standard library, so a decode failure above is the
		// signal rather than a format mismatch here.
		return true
	}
	return true
}
