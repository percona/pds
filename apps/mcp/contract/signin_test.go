package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/harness"
)

// The contract tests run against a real control plane, which the compose workflow brings up with
// an organization whose owner has a password. Without one they skip, so the default run stays on
// the fake in the blackbox tests.
var (
	realURL      = os.Getenv("PDS_REAL_URL")
	realUsername = os.Getenv("PDS_REAL_USERNAME")
	realPassword = os.Getenv("PDS_REAL_PASSWORD")
)

type person struct {
	t      testing.TB
	client *http.Client
}

// signInAsThePerson signs in to the console as the organization's user, the way the browser would
// have by the time the consent screen is reached.
func signInAsThePerson(ctx context.Context, t testing.TB) *person {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &person{t: t, client: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}

	status, body := p.post(ctx, "/api/auth/login", map[string]any{"username": realUsername, "password": realPassword})
	if status != http.StatusOK {
		t.Fatalf("console sign in answered %d: %s", status, body)
	}

	return p
}

func (p *person) post(ctx context.Context, path string, payload any) (int, []byte) {
	p.t.Helper()
	raw, _ := json.Marshal(payload)

	return p.do(ctx, http.MethodPost, path, bytes.NewReader(raw))
}

func (p *person) do(ctx context.Context, method, address string, body io.Reader) (int, []byte) {
	p.t.Helper()
	if address[0] == '/' {
		address = realURL + address
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		p.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if loc := resp.Header.Get("Location"); loc != "" {
		out = []byte(loc)
	}

	return resp.StatusCode, out
}

var allowOnce sync.Once

// allowAccess lets the organization's agents do up to the given access, once per run.
func (p *person) allowAccess(ctx context.Context, access string) {
	p.t.Helper()
	allowOnce.Do(func() {
		raw, _ := json.Marshal(map[string]any{"access": access, "passwords": false, "sqlWrites": false})
		status, body := p.do(ctx, http.MethodPut, "/api/organization/ai", bytes.NewReader(raw))
		if status != http.StatusOK {
			p.t.Fatalf("setting what agents may do answered %d: %s", status, body)
		}
	})
}

// browse is the browser step of pds-mcp login: the person's console session opens the
// authorization address, lands on the consent screen, allows the given access and the browser is
// sent back to the loopback address the command listens on.
func browse(access string) harness.Browse {
	return func(ctx context.Context, address string) error {
		t := &quietT{}
		p := signInAsThePerson(ctx, t)

		status, location := p.do(ctx, http.MethodGet, address, nil)
		if status != http.StatusFound {
			return fmt.Errorf("the authorization request answered %d: %s", status, location)
		}
		consent, err := url.Parse(string(location))
		if err != nil || consent.Query().Get("request") == "" {
			return fmt.Errorf("the authorization request led to %q, not to the consent screen", location)
		}
		request := consent.Query().Get("request")

		status, body := p.post(ctx, "/api/agent-consent/lookup", map[string]any{"request": request})
		if status != http.StatusOK {
			return fmt.Errorf("the consent lookup answered %d: %s", status, body)
		}
		var screen struct {
			Client        struct{ Kind string }
			Organizations []struct {
				ID        uint
				OwnTeamID uint
			}
		}
		if err := json.Unmarshal(body, &screen); err != nil || len(screen.Organizations) == 0 {
			return fmt.Errorf("the consent screen carried no organization: %s", body)
		}
		if screen.Client.Kind != "local" {
			return fmt.Errorf("the consent screen shows the client as %q, want the local client", screen.Client.Kind)
		}

		org := screen.Organizations[0]
		status, body = p.post(ctx, "/api/agent-consent/approve", map[string]any{
			"request": request, "organizationId": org.ID, "teamId": org.OwnTeamID, "access": access,
		})
		if status != http.StatusOK {
			return fmt.Errorf("approving answered %d: %s", status, body)
		}
		var back struct{ RedirectURL string }
		if err := json.Unmarshal(body, &back); err != nil || back.RedirectURL == "" {
			return fmt.Errorf("approving gave no redirect: %s", body)
		}

		// The browser follows the redirect to the command's loopback listener.
		status, body = p.do(ctx, http.MethodGet, back.RedirectURL, nil)
		if status != http.StatusOK {
			return fmt.Errorf("the command's listener answered %d: %s", status, body)
		}

		return nil
	}
}

// quietT lets the sign in helper run on the harness's goroutine without a *testing.T: a failure
// there is returned as the browse error.
type quietT struct{ testing.TB }

func (q *quietT) Helper()                   {}
func (q *quietT) Fatalf(f string, a ...any) { panic(fmt.Sprintf(f, a...)) }
func (q *quietT) Fatal(a ...any)            { panic(fmt.Sprint(a...)) }
