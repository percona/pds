package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// streamedAs feeds whole to a reply stream in pieces of the given size and
// returns what the person was shown.
func streamedAs(whole string, size int) string {
	var shown strings.Builder
	stream := newReplyStream(func(s string) { shown.WriteString(s) })
	stream.beginRound()
	for i := 0; i < len(whole); i += size {
		stream.write(whole[i:min(i+size, len(whole))])
	}
	stream.endRound(whole)

	return shown.String()
}

// Whatever the pieces are, the person sees what redacting the whole answer
// would have left.
func TestReplyStreamShowsWhatRedactingTheWholeAnswerLeaves(t *testing.T) {
	samples := map[string]string{
		"plain":                "Orders-dev is healthy and both members are ready.\n\nThe last backup finished an hour ago.",
		"uri":                  "Connect with mysql://app:s3cret@orders-dev:3306/orders to see it. Done.",
		"password line":        "The password: hunter2 is wrong.\nNext line stays.",
		"bearer":               "Send Authorization: Bearer abcdefghijklmnop12345 with it.",
		"mysql flag":           "Connect with mysql -h db -u root -pHunter2 and then run it.",
		"mysqldump flag":       "Run mysqldump -uroot -psecretpw db to dump it.",
		"flag before name":     "Use -pHunter2 with the mysql client here.",
		"flag on a later line": "Open the client with mysql first.\nThen pass -pHunter2 on that line only.\nDone.",
		"key":                  "Here:\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\nabcSECRETBODY\n-----END PRIVATE KEY-----\nthanks",
		"two keys":             "a\n-----BEGIN A-----\nx\n-----END A-----\nb\n-----BEGIN PRIVATE KEY-----\nSECRETBODY2\n-----END PRIVATE KEY-----\nc",
		"certificate":          "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\nafter",
		"api key field":        "Set api_key = sk-live-123 in the file, then restart.",
	}
	for name, whole := range samples {
		want := strings.TrimSpace(redactCredentials(whole))
		for _, size := range []int{1, 2, 3, 5, 8, 13, 1000} {
			assert.Equal(t, want, streamedAs(whole, size), "%s, in pieces of %d", name, size)
		}
	}
}

func TestReplyStreamWithholdsAKeyThatNeverEnds(t *testing.T) {
	shown := streamedAs("Here:\n-----BEGIN PRIVATE KEY-----\nMIIEvQ\nSECRETBODY", 4)

	assert.NotContains(t, shown, "SECRETBODY")
	assert.NotContains(t, shown, "BEGIN")
	assert.Contains(t, shown, redacted)
}
