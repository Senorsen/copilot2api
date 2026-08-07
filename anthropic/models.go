package anthropic

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/whtsky/copilot2api/internal/models"
)

// modelAliases maps model name variants to their Copilot equivalents.
// Only needed for non-obvious mappings that can't be derived algorithmically.
var modelAliases = map[string]string{
	// Non-obvious version mappings (pre-4.5 naming used single-digit versions)
	"claude-opus-4":   "claude-opus-4.5",
	"claude-sonnet-4": "claude-sonnet-4", // identity, but here for documentation
}

// versionHyphenRe matches hyphen-separated version numbers like "4-6" or "4-5"
// that appear after a letter segment (e.g. "opus-4-6"). Both digits must be single
// to avoid matching date components like "04-14" or "20-25" in "2025-04-14".
var versionHyphenRe = regexp.MustCompile(`([a-zA-Z]-)(\d)-(\d)([^0-9]|$)`)

// dateSuffixRe matches an 8-digit date suffix like "-20250514" or "-20251001"
// at the end of a model ID (optionally followed by more digits for timestamps).
var dateSuffixRe = regexp.MustCompile(`-(\d{8,})$`)

// context1mRe matches the "context-1m" token in the anthropic-beta header,
// used by Claude Code to signal the 1M context window variant.
var context1mRe = regexp.MustCompile(`\bcontext-1m\b`)

// Context1MBeta is the anthropic-beta token GitHub Copilot requires to unlock
// the 1M context window for capable Claude models. Without it the upstream
// enforces a 200k input-token hard limit. There are no separate "-1m" model
// IDs anymore; this header is the only switch.
const Context1MBeta = "context-1m-2025-08-07"

// context1mModelRe matches Claude models known to support the 1M context
// window (opus / sonnet, version 4.6 or higher, with or without a date or -1m
// suffix). Examples: claude-opus-4.6, claude-sonnet-4.6, claude-opus-4.7-1m,
// claude-opus-4.10, claude-opus-5.0. Haiku and pre-4.6 models are excluded.
var context1mModelRe = regexp.MustCompile(`claude-(opus|sonnet)-(4\.(?:[6-9]|\d{2,})|[5-9]\.|[1-9]\d+\.)`)

// forceContext1M reports whether the given (already alias-resolved) model
// supports the 1M context window and should always be sent the beta header.
func forceContext1M(model string) bool {
	return context1mModelRe.MatchString(model)
}

// context1mHeaders returns ExtraHeaders that force the 1M context window for
// capable models. Returns nil for models that don't support it. We always
// inject the beta header (regardless of what the client sent) so the upstream
// never silently falls back to the 200k limit.
func context1mHeaders(model string) map[string]string {
	if !forceContext1M(model) {
		return nil
	}
	return map[string]string{"anthropic-beta": Context1MBeta}
}

// mergeContext1MBeta returns ExtraHeaders that combine any client-provided
// anthropic-beta tokens with the forced context-1m token for capable models.
// Returns nil when the model doesn't support 1M and the client sent no beta.
func mergeContext1MBeta(model, clientBeta string) map[string]string {
	want1m := forceContext1M(model)
	if clientBeta == "" {
		if want1m {
			return map[string]string{"anthropic-beta": Context1MBeta}
		}
		return nil
	}
	if want1m && !context1mRe.MatchString(clientBeta) {
		return map[string]string{"anthropic-beta": clientBeta + "," + Context1MBeta}
	}
	return map[string]string{"anthropic-beta": clientBeta}
}

// resolveModelAlias returns the canonical model ID for Copilot's model list.
// It applies the following transformations in order:
//  1. Strip date suffixes (e.g. "-20250514")
//  2. Normalize hyphen-separated versions to dot-separated (e.g. "4-6" → "4.6")
//  3. Check explicit alias overrides for non-obvious mappings
func resolveModelAlias(modelID string) string {
	// Step 1: Strip date suffix (e.g. "claude-opus-4-6-20250514" → "claude-opus-4-6")
	stripped := dateSuffixRe.ReplaceAllString(modelID, "")
	if stripped == "" {
		stripped = modelID // safety: don't strip everything
	}

	// Step 2: Normalize hyphen-separated versions to dot-separated
	normalized := versionHyphenRe.ReplaceAllString(stripped, "${1}${2}.${3}${4}")

	// Step 3: Check explicit aliases (e.g. "claude-opus-4" → "claude-opus-4.5")
	if alias, ok := modelAliases[normalized]; ok {
		return alias
	}

	// If normalization changed anything, return the normalized form
	if normalized != modelID {
		return normalized
	}

	return modelID
}

// isTokenError reports whether err looks like a GitHub token acquisition
// failure (expired/refresh failed) that is worth retrying after a short delay.
func isTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no valid github token available") ||
		strings.Contains(msg, "authentication required") ||
		strings.Contains(msg, "no valid github token")
}

// getModelInfo returns cached model info, fetching from upstream if needed.
// On token-related errors it retries up to 2 times (3 attempts total) with a
// 1 second delay between attempts.
func (h *Handler) getModelInfo(ctx context.Context, modelID string) (*models.Info, bool) {
	modelID = resolveModelAlias(modelID)

	const maxRetries = 2
	var err error
	var infoMap map[string]*models.Info
	for attempt := 0; attempt <= maxRetries; attempt++ {
		infoMap, err = h.models.GetInfo(ctx)
		if err == nil {
			return infoMap[modelID], false
		}
		if !isTokenError(err) || attempt == maxRetries {
			break
		}
		slog.Warn("token unavailable fetching models for capability detection, retrying", "error", err, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			slog.Error("failed to fetch models for capability detection", "error", ctx.Err())
			return nil, true
		case <-time.After(time.Second):
		}
	}

	slog.Error("failed to fetch models for capability detection", "error", err)
	return nil, true
}

// findModelBySubstring searches the upstream model list for a model ID that
// contains the given substring. Returns the full model ID if found, or "" if
// no match (or if the model list fetch fails). This enables fuzzy matching
// for -1m variants like "claude-opus-4.7-1m-internal".
func (h *Handler) findModelBySubstring(ctx context.Context, substring string) string {
	infoMap, err := h.models.GetInfo(ctx)
	if err != nil {
		return ""
	}
	// Prefer exact match first.
	if _, ok := infoMap[substring]; ok {
		return substring
	}
	// Fall back to substring match.
	for id := range infoMap {
		if strings.Contains(id, substring) {
			return id
		}
	}
	return ""
}

func modelSupportsEndpoint(info *models.Info, endpoint string) bool {
	return models.SupportsEndpoint(info, endpoint)
}
