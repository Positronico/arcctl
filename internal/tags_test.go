//go:build !hwtest

package internal_test

var listTags []string

// rawPath lists, by package, the exported functions besides hidio's own that
// may return a hidio.Raw: none outside a hwtest build.
var rawPath = map[string][]string{}
