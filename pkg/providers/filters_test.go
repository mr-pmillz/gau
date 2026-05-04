package providers_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/stretchr/testify/require"
)

func TestFilters_GetParameters_Empty(t *testing.T) {
	f := providers.Filters{}
	require.Empty(t, f.GetParameters(true))
	require.Empty(t, f.GetParameters(false))
}

func TestFilters_GetParameters_LeadingAmpersand(t *testing.T) {
	f := providers.Filters{From: "202401"}
	for _, mode := range []struct {
		name       string
		forWayback bool
	}{
		{"wayback", true},
		{"commoncrawl", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			got := f.GetParameters(mode.forWayback)
			require.True(t, strings.HasPrefix(got, "&"),
				"params must start with & so they can be appended to a URL with existing query: got %q", got)
		})
	}
}

func TestFilters_GetParameters_Wayback(t *testing.T) {
	f := providers.Filters{
		From:              "202401",
		To:                "202412",
		MatchStatusCodes:  []string{"200", "301"},
		MatchMimeTypes:    []string{"text/html"},
		FilterStatusCodes: []string{"404"},
		FilterMimeTypes:   []string{"image/png"},
	}
	raw := f.GetParameters(true)
	require.True(t, strings.HasPrefix(raw, "&"))

	values, err := url.ParseQuery(strings.TrimPrefix(raw, "&"))
	require.NoError(t, err)

	require.Equal(t, "202401", values.Get("from"))
	require.Equal(t, "202412", values.Get("to"))

	filters := values["filter"]
	require.ElementsMatch(t, filters, []string{
		"mimetype:text/html",
		"statuscode:200",
		"statuscode:301",
		"!statuscode:404",
		"!mimetype:image/png",
	}, "wayback uses ! prefix and statuscode/mimetype keys")
}

func TestFilters_GetParameters_CommonCrawl(t *testing.T) {
	f := providers.Filters{
		From:              "202401",
		To:                "202412",
		MatchStatusCodes:  []string{"200"},
		MatchMimeTypes:    []string{"text/html"},
		FilterStatusCodes: []string{"404"},
		FilterMimeTypes:   []string{"image/png"},
	}
	raw := f.GetParameters(false)
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "&"))
	require.NoError(t, err)

	filters := values["filter"]
	require.ElementsMatch(t, filters, []string{
		"status:200",
		"mime:text/html",
		"!=status:404",
		"!=mime:image/png",
	}, "commoncrawl uses != prefix and status/mime keys (different from wayback)")
}

func TestFilters_GetParameters_OnlyDates(t *testing.T) {
	f := providers.Filters{From: "202301", To: "202312"}
	raw := f.GetParameters(true)
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "&"))
	require.NoError(t, err)

	require.Equal(t, "202301", values.Get("from"))
	require.Equal(t, "202312", values.Get("to"))
	require.Empty(t, values["filter"])
}
