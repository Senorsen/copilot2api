package main

import "testing"

func TestValidateBasePath(t *testing.T) {
	for _, value := range []string{"", "/", "/copilot", "/apps/copilot/", "/my path/", "/%E4%B8%AD/"} {
		t.Run("valid:"+value, func(t *testing.T) {
			if err := validateBasePath(value); err != nil {
				t.Fatalf("validateBasePath(%q): %v", value, err)
			}
		})
	}
	for _, value := range []string{
		"copilot", "https://example.com/copilot", "//example.com/copilot", "javascript:alert(1)",
		"///example.com", "/\\example.com", "/%5Cexample.com", "/%2f%2fexample.com",
		"/copilot?x=1", "/copilot?", "/copilot#fragment", "/copilot#", "/%", "/copilot\n/",
	} {
		t.Run("invalid:"+value, func(t *testing.T) {
			if err := validateBasePath(value); err == nil {
				t.Fatalf("validateBasePath(%q) unexpectedly succeeded", value)
			}
		})
	}
}
