package blocklist

// Preset describes a built-in public DNS blocklist source from the HRNeo
// catalog. Presets are convenience entries only; users can still add any
// validated public HTTP(S) source.
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

var presets = []Preset{
	{ID: "hagezi-light", Name: "HaGeZi Light", URL: "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/light-onlydomains.txt"},
	{ID: "hagezi-multi", Name: "HaGeZi Multi", URL: "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/multi-onlydomains.txt"},
	{ID: "hagezi-pro", Name: "HaGeZi Pro", URL: "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro-onlydomains.txt"},
	{ID: "oisd-small", Name: "OISD small", URL: "https://small.oisd.nl/domainswild2"},
	{ID: "oisd-big", Name: "OISD big", URL: "https://big.oisd.nl/domainswild2"},
	{ID: "adguard-dns", Name: "AdGuard DNS filter", URL: "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt"},
	{ID: "stevenblack", Name: "StevenBlack", URL: "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"},
}

// Catalog returns a defensive copy of the built-in source presets.
func Catalog() []Preset {
	return append([]Preset(nil), presets...)
}
