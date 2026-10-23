package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/store"
)

// Site is one Prism Central behind Portico, presented to tenants by its
// code (design sites §4.1). The id is immutable and is what placements,
// sessions and stash cookies reference; the code is what tenants see and
// the MSP may rename.
type Site struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Order     int    `json:"order"`
	URL       string `json:"url"`
	TLSVerify bool   `json:"tls_verify"`
	CAPEM     string `json:"ca_pem"`
	// TLSPin is the SHA-256 of the leaf certificate the MSP confirmed, in
	// proxy.Fingerprint form; set, it wins over TLSVerify (usability 4.3).
	TLSPin      string `json:"tls_pin,omitempty"`
	APIKey      Secret `json:"api_key"`
	SAMLIdPName string `json:"saml_idp_name"`
	// SAMLEntityID is the service-provider entity id of the site's IdP,
	// which is the Keycloak SAML client id (design sites §4.5).
	SAMLEntityID string `json:"saml_entity_id"`
	// Defaults offered when a tenant is placed on the site.
	ClusterExtID          string `json:"cluster_ext_id"`
	StorageContainerExtID string `json:"storage_container_ext_id"`
}

// Trust is how Portico trusts the site's Prism Central.
func (s Site) Trust() proxy.SiteTrust {
	return proxy.SiteTrust{CAPEM: []byte(s.CAPEM), Pin: s.TLSPin, Verify: s.TLSVerify}
}

// The site a single-Prism Central document becomes (design sites §4.7).
const (
	LegacySiteID   = store.LegacySiteID
	LegacySiteCode = "SITE-1"
)

var (
	reSiteID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	reSiteCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{1,31}$`)
)

// ValidSiteID reports whether id can name a site.
func ValidSiteID(id string) bool { return reSiteID.MatchString(id) }

// ValidSiteCode reports whether code can be shown as a site code.
func ValidSiteCode(code string) bool { return reSiteCode.MatchString(code) }

// SiteIDFromCode is the default id of a new site: its code in lower case.
func SiteIDFromCode(code string) string { return strings.ToLower(strings.TrimSpace(code)) }

// Site returns the site with the given id.
func (s Settings) Site(id string) (Site, bool) {
	for _, site := range s.Sites {
		if site.ID == id {
			return site, true
		}
	}
	return Site{}, false
}

// siteIndex returns the position of the site with the given id, or -1.
func (s Settings) siteIndex(id string) int {
	for i := range s.Sites {
		if s.Sites[i].ID == id {
			return i
		}
	}
	return -1
}

// OrderedSites returns the sites by Order, then Code.
func (s Settings) OrderedSites() []Site {
	out := append([]Site(nil), s.Sites...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// missingFields names what a site still lacks to be served.
func (s Site) missingFields() []string {
	var out []string
	for _, f := range []struct{ name, value string }{{"url", s.URL}, {"api_key", string(s.APIKey)}, {"saml_idp_name", s.SAMLIdPName}} {
		if strings.TrimSpace(f.value) == "" {
			out = append(out, f.name)
		}
	}
	return out
}

// Complete reports whether the site has its connection, its service
// account's key and its SAML trust: only a complete site is served.
func (s Site) Complete() bool { return len(s.missingFields()) == 0 }

// ServedSites returns the complete sites in order. A site still being
// added (no key or no SAML trust yet) is not served, and does not stop
// the others from running (review I1).
func (s Settings) ServedSites() []Site {
	var out []Site
	for _, site := range s.OrderedSites() {
		if site.Complete() {
			out = append(out, site)
		}
	}
	return out
}

// IncompleteSites lists the sites not served yet, each with what it
// lacks: "nyc1 (api_key, saml_idp_name)".
func (s Settings) IncompleteSites() []string {
	var out []string
	for _, site := range s.OrderedSites() {
		if m := site.missingFields(); len(m) > 0 {
			out = append(out, site.ID+" ("+strings.Join(m, ", ")+")")
		}
	}
	return out
}

// PassthroughSiteOrFirst is the site the internal pass-through serves:
// the one named in the settings when it is complete, else the first
// served site by order (design sites §4.6).
func (s Settings) PassthroughSiteOrFirst() (Site, bool) {
	if site, ok := s.Site(s.PassthroughSite); ok && site.Complete() {
		return site, true
	}
	if o := s.ServedSites(); len(o) > 0 {
		return o[0], true
	}
	return Site{}, false
}

// validateSites checks the shape of every site and the cross-site rules.
func (s Settings) validateSites() []error {
	var errs []error
	ids, codes := map[string]bool{}, map[string]bool{}
	for i, site := range s.Sites {
		if !ValidSiteID(site.ID) {
			errs = append(errs, fmt.Errorf("settings: sites[%d].id %q must be 1 to 32 lower-case letters, digits and dashes", i, site.ID))
		} else if ids[site.ID] {
			errs = append(errs, fmt.Errorf("settings: duplicate site id %q", site.ID))
		}
		ids[site.ID] = true
		if !ValidSiteCode(site.Code) {
			errs = append(errs, fmt.Errorf("settings: sites[%d].code %q must be 2 to 32 upper-case letters, digits and dashes", i, site.Code))
		} else if codes[site.Code] {
			errs = append(errs, fmt.Errorf("settings: duplicate site code %q", site.Code))
		}
		codes[site.Code] = true
		if site.URL == "" {
			continue
		}
		u, err := url.Parse(site.URL)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			if err == nil && u.User != nil {
				errs = append(errs, fmt.Errorf("settings: sites[%d].url must not carry credentials", i))
			} else {
				errs = append(errs, fmt.Errorf("settings: sites[%d].url is not an http(s) URL", i))
			}
		case u.User != nil:
			// Not echoed: the userinfo is a password in clear.
			errs = append(errs, fmt.Errorf("settings: sites[%d].url must not carry credentials", i))
		}
	}
	if s.PassthroughSite != "" && !ids[s.PassthroughSite] {
		errs = append(errs, fmt.Errorf("settings: passthrough_site %q names no site", s.PassthroughSite))
	}
	return errs
}

// legacyDocument is the part of a single-Prism Central document that
// moves into site-1.
type legacyDocument struct {
	PrismCentral *struct {
		URL         string `json:"url"`
		TLSVerify   bool   `json:"tls_verify"`
		CAPEM       string `json:"ca_pem"`
		APIKey      Secret `json:"api_key"`
		SAMLIdPName string `json:"saml_idp_name"`
	} `json:"prism_central"`
	Tenants struct {
		ClusterExtID          string `json:"cluster_ext_id"`
		StorageContainerExtID string `json:"storage_container_ext_id"`
	} `json:"tenants"`
}

// adoptLegacy turns the legacy prism_central block of raw into site-1
// when out has no sites; the key is moved still sealed. It reports
// whether it did.
func adoptLegacy(raw []byte, out *Settings) bool {
	if len(out.Sites) > 0 {
		return false
	}
	var l legacyDocument
	if err := json.Unmarshal(raw, &l); err != nil || l.PrismCentral == nil || l.PrismCentral.URL == "" {
		return false
	}
	name := l.PrismCentral.URL
	if u, err := url.Parse(l.PrismCentral.URL); err == nil && u.Hostname() != "" {
		name = u.Hostname()
	}
	out.Sites = []Site{{
		ID: LegacySiteID, Code: LegacySiteCode, Name: name,
		URL: l.PrismCentral.URL, TLSVerify: l.PrismCentral.TLSVerify, CAPEM: l.PrismCentral.CAPEM,
		APIKey: l.PrismCentral.APIKey, SAMLIdPName: l.PrismCentral.SAMLIdPName,
		ClusterExtID: l.Tenants.ClusterExtID, StorageContainerExtID: l.Tenants.StorageContainerExtID,
	}}
	return true
}
