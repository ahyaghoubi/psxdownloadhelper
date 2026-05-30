// Package match classifies PlayStation CDN URLs against rule packs.
// Rule packs are YAML data files (see internal/match/rules/) so community
// contributions don't require recompilation. See plan.md §6.4.
package match

import "regexp"

// Kind labels each classified URL. Default order in plan.md §6.4.
type Kind string

const (
	KindUnknown      Kind = "unknown"
	KindIgnore       Kind = "ignore"
	KindPKGBase      Kind = "pkg-base"      // PS4 /appkgo/ base
	KindPKGPatch     Kind = "pkg-patch"     // PS4 /ppkgo/ patch
	KindPKGApp       Kind = "pkg-app"       // PS5 /app/pkg/ application
	KindPKGSC        Kind = "pkg-sc"        // PS5 _sc.pkg PlayGo/info
	KindPKGDelta     Kind = "pkg-delta"     // *-DP.pkg cumulative delta
	KindManifestJSON Kind = "manifest-json" // *.json manifests
	KindManifestXML  Kind = "manifest-xml"  // PS5 version.xml
	KindCRC          Kind = "crc"           // PS5 *.crc chunk checksum
)

// Rule is the on-disk and in-memory shape of a single classification rule.
// HostSuffix matches either the exact host or any subdomain of it (".host").
type Rule struct {
	Kind       Kind   `yaml:"kind"`
	HostSuffix string `yaml:"host_suffix"`
	PathRegex  string `yaml:"path_regex"`
}

// compiledRule is a Rule with its PathRegex pre-compiled.
type compiledRule struct {
	kind       Kind
	hostSuffix string
	pathRegex  *regexp.Regexp
}

// RuleSet is an ordered list of rules; first match wins.
type RuleSet struct {
	rules []compiledRule
}

// Hint carries metadata extracted from a URL alongside its Kind.
// PartIndex is -1 when not parseable from the basename.
type Hint struct {
	TitleHint string
	PartIndex int
}
