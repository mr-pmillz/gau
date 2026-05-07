package providers_test

import (
	"testing"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/stretchr/testify/require"
)

const (
	exampleDotCom  = "example.com"
	exampleDotCoUK = "example.co.uk"
)

func TestHasSubdomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"apex .com", exampleDotCom, false},
		{"apex .co.uk", exampleDotCoUK, false},
		{"www subdomain", "www.example.com", true},
		{"deep subdomain", "a.b.c.example.com", true},
		{"subdomain on multipart TLD", "www.example.co.uk", true},
		{"uppercase apex", "EXAMPLE.COM", false},
		{"uppercase subdomain", "WWW.EXAMPLE.COM", true},
		{"trailing dot apex", "example.com.", false},
		{"trailing dot subdomain", "www.example.com.", true},
		{"surrounding whitespace", "  www.example.com  ", true},
		{"empty", "", false},
		{"public suffix only", "co.uk", false},
		{"single label", "localhost", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, providers.HasSubdomain(tc.in))
		})
	}
}

func TestDomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"apex .com", exampleDotCom, exampleDotCom},
		{"www subdomain", "www.example.com", exampleDotCom},
		{"deep subdomain", "a.b.c.example.com", exampleDotCom},
		{"multipart TLD apex", exampleDotCoUK, exampleDotCoUK},
		{"multipart TLD subdomain", "www.example.co.uk", exampleDotCoUK},
		{"uppercase normalized to lower", "WWW.EXAMPLE.COM", exampleDotCom},
		{"trailing dot", "www.example.com.", exampleDotCom},
		{"empty", "", ""},
		{"public suffix only", "co.uk", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, providers.Domain(tc.in))
		})
	}
}
