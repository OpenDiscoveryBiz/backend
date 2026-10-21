package dk

import (
	"net/url"
	"regexp"
)

var (
	nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)
	dkID     = regexp.MustCompile(`^(DK)([0-9]+)$`)
	schemeRE = regexp.MustCompile(`(?i)^[a-z0-9]+?://`)
)

func parseURL(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
