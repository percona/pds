package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/percona/pds/adapter/internal/client/servicenow"
)

// supportNow is the fixed clock the support handler tests read, so an assembled
// description carries an exact timestamp.
var supportNow = time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)

// fakeServiceNow is an in-memory stand in for the Percona Connector. The
// adapter talks to it through the real client, so a handler test exercises the
// auth carriers, the fixed field values, and the decoding rather than a Go
// double.
//
// Four routes are served, the ones the adapter calls.
type fakeServiceNow struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []fakeSNRequest

	// number is the ticket number a create and a details read answer with.
	number string
	// createWithoutNumber answers a create with the record id only, which is
	// what the documented connector response carries.
	createWithoutNumber bool
	// failStatus, when non zero, answers every route with that status.
	failStatus int
	// rejectMessage, when set, answers a create with 200 and success false.
	rejectMessage string
	// replyStatus, when non-zero, answers a public comment update with this status.
	replyStatus int
	// missing answers the details route with an empty result, which is how the
	// connector reports a ticket number the account cannot see.
	missing bool
	// tickets is the list the tickets route answers with. Nil serves two.
	tickets []map[string]any
	// details overrides the detail response for a ticket number.
	details map[string]map[string]any
	// oversized answers the list route with a body past the client's cap.
	oversized bool
	// searchStatus, when non zero, is what the case table read answers with.
	searchStatus int
}

// fakeSNRequest is one recorded call.
type fakeSNRequest struct {
	Path   string
	Query  string
	APIKey string
	Body   map[string]any
}

func newFakeServiceNow(t *testing.T) *fakeServiceNow {
	t.Helper()

	f := &fakeServiceNow{number: "CS0061234"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)

	return f
}

// client returns a real ServiceNow client pointed at the fake, with the retry
// wait replaced so a failure test does not sit through a backoff.
func (f *fakeServiceNow) client(t *testing.T) *servicenow.Client {
	t.Helper()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     f.server.URL,
		APIKey:      "fake-sn-api-key",
		ClientToken: "fake-sn-client-token",
		Sleep:       func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("build servicenow client: %v", err)
	}

	return client
}

// down closes the fake so every call fails at the transport, which is what an
// unreachable ServiceNow looks like to the adapter.
func (f *fakeServiceNow) down() { f.server.Close() }

func (f *fakeServiceNow) seen() []fakeSNRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]fakeSNRequest, len(f.requests))
	copy(out, f.requests)

	return out
}

// lastBody returns the body of the most recent call, for asserting what the
// service assembled.
func (f *fakeServiceNow) lastBody(t *testing.T) map[string]any {
	t.Helper()

	seen := f.seen()
	if len(seen) == 0 {
		t.Fatal("no request reached the fake ServiceNow")
	}

	return seen[len(seen)-1].Body
}

func (f *fakeServiceNow) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)

	recorded := fakeSNRequest{
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		APIKey: r.Header.Get("x-sn-apikey"),
	}
	_ = json.Unmarshal(raw, &recorded.Body)

	f.mu.Lock()
	f.requests = append(f.requests, recorded)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		_, _ = w.Write([]byte(`{"error":{"message":"instance is not available"}}`))

		return
	}

	switch r.URL.Path {
	case servicenow.CreatePath:
		f.serveCreate(w)
	case servicenow.ListPath:
		f.serveList(w)
	case servicenow.SearchPath:
		f.serveSearch(w, r)
	case servicenow.DetailsPath:
		f.serveDetails(w, recorded.Body)
	case "/api/now/table/sn_customerservice_case/sys-1":
		if r.Method != http.MethodPatch {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}
		if f.replyStatus != 0 {
			w.WriteHeader(f.replyStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"client_token is not accepted"}}`))

			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServiceNow) serveCreate(w http.ResponseWriter) {
	if f.rejectMessage != "" {
		f.write(w, map[string]any{
			"result": map[string]any{"success": false, "error_message": f.rejectMessage},
		})

		return
	}

	result := map[string]any{
		"success":    true,
		"ticket_id":  "sys-1",
		"table_name": "sn_customerservice_case",
		"message":    "Ticket created successfully",
	}
	if !f.createWithoutNumber {
		result["number"] = f.number
	}

	f.write(w, map[string]any{"result": result})
}

func (f *fakeServiceNow) writeOversized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"result":[{"number":"CS0061234","description":"`)
	chunk := strings.Repeat("x", 1<<20)
	for range 9 {
		_, _ = io.WriteString(w, chunk)
	}
	_, _ = io.WriteString(w, `"}]}`)
}

func (f *fakeServiceNow) serveList(w http.ResponseWriter) {
	if f.oversized {
		f.writeOversized(w)

		return
	}

	if f.tickets != nil {
		f.write(w, map[string]any{"result": f.tickets})

		return
	}

	f.write(w, map[string]any{"result": f.defaultTickets()})
}

func (f *fakeServiceNow) defaultTickets() []map[string]any {
	return []map[string]any{
		{
			"number":            f.number,
			"sys_id":            "sys-1",
			"table_name":        "sn_customerservice_case",
			"state":             "Awaiting Percona",
			"priority":          "3 - Moderate",
			"short_description": "Cannot create database orders-prod",
			"description":       "Reported by the Percona Database Service control plane.\nDatabase: orders-prod",
			"u_main_technology": "MySQL",
			"opened_at":         "2026-08-20 09:12:33",
			"sys_updated_on":    "2026-08-21 14:02:10",
		},
		{
			"number":            "CS0061198",
			"sys_id":            "sys-2",
			"table_name":        "sn_customerservice_case",
			"state":             "Resolved",
			"short_description": "Backup of orders-staging failed",
			"description":       "Reported by the Percona Database Service control plane.\nDatabase: orders-staging",
			"u_main_technology": "MySQL",
			"opened_at":         "2026-08-11 08:04:00",
			"resolved_at":       "2026-08-12 16:30:41",
			"sys_updated_on":    "2026-08-12 16:30:41",
		},
	}
}

// serveSearch answers the Table API read the support list uses. The instance
// filters, so this applies the same description match the query asks for.
func (f *fakeServiceNow) serveSearch(w http.ResponseWriter, r *http.Request) {
	if f.searchStatus != 0 {
		w.WriteHeader(f.searchStatus)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient rights"}}`))

		return
	}

	if f.oversized {
		f.writeOversized(w)

		return
	}

	matches := searchNeedles(r.URL.Query().Get("sysparm_query"))

	rows := f.tickets
	if rows == nil {
		rows = f.defaultTickets()
	}

	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		description, _ := row["description"].(string)
		if len(matches) > 0 && !containsAny(description, matches) {
			continue
		}

		out = append(out, row)
	}

	f.write(w, map[string]any{"result": out})
}

// searchNeedles reads the description conditions out of an encoded query the
// way the instance does: conditions are joined with ^OR and the ordering ends
// the list.
func searchNeedles(query string) []string {
	var out []string
	for _, part := range strings.Split(query, "^") {
		if strings.HasPrefix(part, "ORDERBY") {
			break
		}

		part = strings.TrimPrefix(part, "OR")
		if needle := strings.TrimPrefix(part, "descriptionLIKE"); needle != part {
			out = append(out, needle)
		}
	}

	return out
}

func containsAny(description string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(description, needle) {
			return true
		}
	}

	return false
}

func (f *fakeServiceNow) serveDetails(w http.ResponseWriter, body map[string]any) {
	if f.missing {
		f.write(w, map[string]any{"result": map[string]any{}})

		return
	}

	number, _ := body["ticket_number"].(string)
	if number == "" {
		number = f.number
	}
	if f.details != nil {
		if detail, ok := f.details[number]; ok {
			f.write(w, map[string]any{"result": detail})

			return
		}
	}

	f.write(w, map[string]any{"result": map[string]any{
		"number":            number,
		"sys_id":            "sys-1",
		"table_name":        "sn_customerservice_case",
		"state":             "Awaiting Percona",
		"priority":          "3 - Moderate",
		"account":           "Example Corp",
		"contact":           "Dana Platform",
		"short_description": "Cannot create database orders-prod",
		"description":       "Reported by the Percona Database Service control plane.\nDatabase: orders-prod",
		"u_main_technology": "MySQL",
		"opened_at":         "2026-08-20 09:12:33",
		"sys_updated_on":    "2026-08-21 14:02:10",
		"comments": []map[string]any{
			{
				"created_on": "2026-08-20 11:40:02",
				"created_by": "percona.support",
				"label":      "Additional comments",
				"value":      "Could you attach the operator events?",
			},
		},
	}})
}

func (f *fakeServiceNow) write(w http.ResponseWriter, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(body)
}
