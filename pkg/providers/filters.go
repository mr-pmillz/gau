package providers

import "net/url"

type filterParam struct {
	values []string
	prefix string
}

type Filters struct {
	From              string   `mapstructure:"from"`
	To                string   `mapstructure:"to"`
	MatchStatusCodes  []string `mapstructure:"matchstatuscodes"`
	MatchMimeTypes    []string `mapstructure:"matchmimetypes"`
	FilterStatusCodes []string `mapstructure:"filterstatuscodes"`
	FilterMimeTypes   []string `mapstructure:"filtermimetypes"`
}

func (f *Filters) GetParameters(forWayback bool) string {
	form := url.Values{}
	addIfSet(form, "from", f.From)
	addIfSet(form, "to", f.To)

	if forWayback {
		addFilterParams(form, f.waybackFilterParams())
	} else {
		addFilterParams(form, f.commonCrawlFilterParams())
	}

	params := form.Encode()
	if params != "" {
		return "&" + params
	}

	return params
}

func addIfSet(form url.Values, key string, value string) {
	if value != "" {
		form.Add(key, value)
	}
}

func addFilterParams(form url.Values, params []filterParam) {
	for _, param := range params {
		for _, value := range param.values {
			form.Add("filter", param.prefix+value)
		}
	}
}

func (f *Filters) waybackFilterParams() []filterParam {
	return []filterParam{
		{values: f.MatchMimeTypes, prefix: "mimetype:"},
		{values: f.MatchStatusCodes, prefix: "statuscode:"},
		{values: f.FilterStatusCodes, prefix: "!statuscode:"},
		{values: f.FilterMimeTypes, prefix: "!mimetype:"},
	}
}

func (f *Filters) commonCrawlFilterParams() []filterParam {
	return []filterParam{
		{values: f.MatchStatusCodes, prefix: "status:"},
		{values: f.MatchMimeTypes, prefix: "mime:"},
		{values: f.FilterStatusCodes, prefix: "!=status:"},
		{values: f.FilterMimeTypes, prefix: "!=mime:"},
	}
}
