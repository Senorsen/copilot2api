package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDescribeRequestImages_ReportsDimensions(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "no images here"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/png", "data": pngDataURL(t, 7, 11),
				}},
			}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	diags := describeRequestImages(raw)
	if len(diags) != 1 {
		t.Fatalf("diags = %d, want 1", len(diags))
	}
	got := diags[0]
	if got.Path != "messages.1.content.1" {
		t.Errorf("path = %q", got.Path)
	}
	if got.Width != 7 || got.Height != 11 {
		t.Errorf("dimensions = %dx%d, want 7x11", got.Width, got.Height)
	}
	if got.Format != "png" {
		t.Errorf("format = %q, want png", got.Format)
	}
	if got.Problem != "" {
		t.Errorf("problem = %q, want none", got.Problem)
	}
}

func TestDescribeRequestImages_FlagsUndecodableAndMismatched(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/png", "data": "",
				}},
				map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/jpeg", "data": pngDataURL(t, 4, 4),
				}},
			}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	diags := describeRequestImages(raw)
	if len(diags) != 2 {
		t.Fatalf("diags = %d, want 2", len(diags))
	}
	if diags[0].Problem != "empty image payload" {
		t.Errorf("first problem = %q", diags[0].Problem)
	}
	if diags[1].Problem == "" {
		t.Error("expected a media_type mismatch to be reported")
	}
}

func TestUpstreamRejectedImages(t *testing.T) {
	if !upstreamRejectedImages([]byte(`{"error":{"message":"messages.80.content.1.image.source"}}`)) {
		t.Error("expected image-related error to be detected")
	}
	if upstreamRejectedImages([]byte(`{"error":{"message":"rate limit exceeded"}}`)) {
		t.Error("unrelated error should not trigger image decoding")
	}
}
