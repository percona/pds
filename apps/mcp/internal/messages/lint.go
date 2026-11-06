package messages

import "regexp"

// Banned are the words the customer copy rules refuse: operator words, a bare cluster, dashes and
// exclamation marks. The copy tests over every sentence a person or an agent reads share this list.
var Banned = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bPMM\b`), regexp.MustCompile(`(?i)\bFlux\b`), regexp.MustCompile(`(?i)\bhub\b`),
	regexp.MustCompile(`(?i)\bthe adapter\b`), regexp.MustCompile(`(?i)\bspoke\b`),
	regexp.MustCompile(`(?i)(^|[^a-z])clusters?\b`), regexp.MustCompile(`\x{2014}|\x{2013}`),
	regexp.MustCompile(`!`),
}
