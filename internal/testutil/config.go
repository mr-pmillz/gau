package testutil

import (
	"crypto/tls"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/valyala/fasthttp"
)

// NewProviderConfig returns a *providers.Config suitable for unit tests.
// The fasthttp.Client uses the default dialer (httptest serves over http,
// so TLS is irrelevant); timeouts are short so misbehaving tests fail fast
// rather than hang.
func NewProviderConfig(_ *testing.T) *providers.Config {
	return &providers.Config{
		Threads:    1,
		Timeout:    5,
		MaxRetries: 0, // tests script exact responses; retries would mask bugs
		Client: &fasthttp.Client{
			TLSConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Blacklist: mapset.NewThreadUnsafeSet[string](""),
	}
}
