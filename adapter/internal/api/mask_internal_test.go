package api

import (
	"strings"
	"testing"
)

// Masking moved from TypeScript to Go with this route, and the seven cases
// that guarded it moved nowhere. The end-to-end test covers one fixture URI
// whose password holds no at sign, so the index arithmetic below was free to
// regress and still look correct.
func TestMaskURI(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ in, want string }{
		"replaces the password with a mask": {
			in:   "mysql://app:secret@orders-dev.svc:3306/orders-dev",
			want: "mysql://app:" + passwordMask + "@orders-dev.svc:3306/orders-dev",
		},
		"masks a short password to the same width": {
			in:   "mysql://app:a@host:3306/db",
			want: "mysql://app:" + passwordMask + "@host:3306/db",
		},
		"masks a long password to the same width": {
			in:   "mysql://app:0123456789abcdef@host:3306/db",
			want: "mysql://app:" + passwordMask + "@host:3306/db",
		},
		"keeps the query string, which carries TLS options rather than secrets": {
			in:   "postgresql://app:secret@host:5432/orders?sslmode=require",
			want: "postgresql://app:" + passwordMask + "@host:5432/orders?sslmode=require",
		},
		"masks a password that contains an at sign": {
			in:   "mysql://app:p@ss@host:3306/db",
			want: "mysql://app:" + passwordMask + "@host:3306/db",
		},
		"says nothing when a slash hides an at sign in the password": {
			in:   "mysql://app:p@ss/word@host:3306/db",
			want: "",
		},
		"leaves a URI with no password alone": {
			in:   "mysql://app@host:3306/db",
			want: "mysql://app@host:3306/db",
		},
		"leaves a URI with no user info alone": {
			in:   "mysql://host:3306/db",
			want: "mysql://host:3306/db",
		},
		"returns an unparseable string unchanged rather than guessing": {
			in:   "not-a-uri",
			want: "not-a-uri",
		},
		// A password holding an unescaped /, ? or # ends the authority before
		// the at sign, so no parser reads the credential as user info. This
		// used to return the input unchanged, which put the whole password in
		// the list payload for every database on the page. The adapter builds
		// its own URIs with url.UserPassword, which escapes these bytes, but
		// it passes an upstream URI through verbatim when OpenEverest sends
		// one, and this function is the only guard on that path.
		"says nothing when a slash hides the user info": {
			in:   "mysql://app:p/ss@host:3306/db",
			want: "",
		},
		"says nothing when a question mark hides the user info": {
			in:   "mysql://app:p?ss@host:3306/db",
			want: "",
		},
		"says nothing when a hash hides the user info": {
			in:   "mysql://app:p#ss@host:3306/db",
			want: "",
		},
		"returns the empty string unchanged": {
			in:   "",
			want: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := maskURI(tc.in); got != tc.want {
				t.Fatalf("maskURI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The property behind every case above: whatever comes back must not carry the
// password that went in. A masking function that fails open is worse than none,
// because the payload it guards is rendered for every database at once.
func TestMaskURINeverEchoesThePassword(t *testing.T) {
	t.Parallel()

	const password = "s3cr3t"

	for _, in := range []string{
		"mysql://app:" + password + "@host:3306/db",
		"mysql://app:" + password + "/x@host:3306/db",
		"mysql://app:" + password + "?x@host:3306/db",
		"mysql://app:" + password + "#x@host:3306/db",
		"mysql://app:" + password + "@host:3306/db?opt=1",
		"postgresql://app:" + password + "@host:5432/orders",
		"mysql://app:" + password + "@@host:3306/db",
		"mysql://app:p@" + password + "/word@host:3306/db",
	} {
		if got := maskURI(in); strings.Contains(got, password) {
			t.Errorf("maskURI(%q) = %q, which still carries the password", in, got)
		}
	}
}
