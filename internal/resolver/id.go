package resolver

import "regexp"

var (
	nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)
	idRE     = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{1,14}$`)
)
