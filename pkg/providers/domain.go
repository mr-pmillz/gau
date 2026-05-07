package providers

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// HasSubdomain reports whether the input contains a subdomain component.
// "www.example.com" → true, "example.com" → false, "example.co.uk" → false.
// Returns false when the input cannot be parsed against the Public Suffix List.
func HasSubdomain(domain string) bool {
	apex := Domain(domain)
	if apex == "" {
		return false
	}
	return normalizeDomain(domain) != apex
}

// Domain returns the registrable apex domain (eTLD+1) for the input.
// "www.example.co.uk" → "example.co.uk". Returns "" when the input cannot
// be parsed against the Public Suffix List.
func Domain(domain string) string {
	d := normalizeDomain(domain)
	if d == "" {
		return ""
	}
	apex, err := publicsuffix.EffectiveTLDPlusOne(d)
	if err != nil {
		return ""
	}
	return apex
}

func normalizeDomain(d string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
}
