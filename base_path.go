package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func validateBasePath(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid base path: %w", err)
	}
	if u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n\t") ||
		strings.ContainsAny(value, "?#") {
		return errors.New("base path must be a same-origin absolute path without query or fragment (e.g. /copilot)")
	}
	return nil
}
