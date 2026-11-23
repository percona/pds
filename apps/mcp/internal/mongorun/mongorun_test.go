package mongorun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"

	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

var limits = sqlrun.Limits{MaxRows: 3, MaxBytes: 1000, Timeout: 30 * time.Second}

func target() sqlrun.Target {
	return sqlrun.Target{
		Engine: sqlrun.MongoDB, Host: "m-0.example.com", Port: 27017, User: "dba@x", Database: "app", AuthSource: "admin",
		TLS: sqlrun.TLSOff, Members: []string{"m-0.example.com:27017", "m-1.example.com:27017"}, ReplicaSet: "rs0",
	}
}

func TestTheConnectionStringHasEveryMemberTheReplicaSetAndPlainTransport(t *testing.T) {
	uri := URI(target(), "p@ss:w/rd#1")

	want := "mongodb://dba%40x:p%40ss%3Aw%2Frd%231@m-0.example.com:27017,m-1.example.com:27017/app?authSource=admin&replicaSet=rs0&tls=false"
	if uri != want {
		t.Fatalf("uri %s, want %s", uri, want)
	}
	opts := options.Client().ApplyURI(uri)
	if opts.Validate() != nil || len(opts.Hosts) != 2 || *opts.ReplicaSet != "rs0" || opts.Auth.Username != "dba@x" || opts.Auth.Password != "p@ss:w/rd#1" || opts.Auth.AuthSource != "admin" {
		t.Fatalf("the driver read %+v from %s", opts, uri)
	}
	if opts.TLSConfig != nil {
		t.Fatal("plain transport was turned into TLS")
	}
}

func TestTheConnectionStringWithoutAPasswordCarriesNone(t *testing.T) {
	uri := URI(target(), "")
	if strings.Contains(uri, ":@") || strings.Contains(uri, "p@ss") || !strings.HasPrefix(uri, "mongodb://dba%40x@m-0") {
		t.Fatalf("uri %s", uri)
	}
}

func TestADirectConnectionAndASeparateAuthDatabaseAreKept(t *testing.T) {
	tgt := target()
	tgt.Members, tgt.Direct, tgt.Database, tgt.AuthSource = []string{"127.0.0.1:27017"}, true, "shop", "admin"
	uri := URI(tgt, "x")

	opts := options.Client().ApplyURI(uri)
	if opts.Validate() != nil || opts.Direct == nil || !*opts.Direct || opts.Auth.AuthSource != "admin" || len(opts.Hosts) != 1 {
		t.Fatalf("the driver read %+v from %s", opts, uri)
	}
	if !strings.Contains(uri, "/shop?") || !strings.Contains(uri, "directConnection=true") || !strings.Contains(uri, "authSource=admin") {
		t.Fatalf("uri %s", uri)
	}
}

func TestATargetWithoutMembersHasTheOneHostAndPort(t *testing.T) {
	tgt := sqlrun.Target{Engine: sqlrun.MongoDB, Host: "m.example.com", Port: 27018, User: "u", Database: "app", TLS: sqlrun.TLSVerify}
	uri := URI(tgt, "")
	if !strings.HasPrefix(uri, "mongodb://u@m.example.com:27018/app?") || !strings.Contains(uri, "tls=true") {
		t.Fatalf("uri %s", uri)
	}
}

func doc(t *testing.T, text string) bson.D {
	t.Helper()
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &d); err != nil {
		t.Fatal(err)
	}

	return d
}

func lookup(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}

	return nil, false
}

func TestEveryCommandCarriesATimeLimitTheAgentCannotRaise(t *testing.T) {
	for text, want := range map[string]int64{
		`{"find": "c"}`:                      30000,
		`{"find": "c", "maxTimeMS": 500}`:    500,
		`{"find": "c", "maxTimeMS": 600000}`: 30000,
		`{"count": "c", "maxTimeMS": 0}`:     30000,
		`{"drop": "c", "maxTimeMS": -5}`:     30000,
		`{"dbStats": 1, "maxTimeMS": 1.5}`:   1,
	} {
		d := prepare(doc(t, text), limits)
		got, _ := lookup(d, "maxTimeMS")
		if got != want {
			t.Errorf("%s: maxTimeMS %v, want %d", text, got, want)
		}
		if d[0].Key != doc(t, text)[0].Key {
			t.Errorf("%s: the command is no longer first: %v", text, d)
		}
		count := 0
		for _, e := range d {
			if e.Key == "maxTimeMS" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s: maxTimeMS appears %d times", text, count)
		}
	}
}

func TestACommandThatAnswersWithACursorAsksForNoMoreThanTheLimitAllows(t *testing.T) {
	find := prepare(doc(t, `{"find": "c", "batchSize": 100000}`), limits)
	if v, _ := lookup(find, "batchSize"); v != int32(4) {
		t.Errorf("find batchSize %v", v)
	}

	for _, text := range []string{`{"aggregate": "c", "pipeline": []}`, `{"aggregate": "c", "pipeline": [], "cursor": {"batchSize": 99999}}`, `{"listCollections": 1}`, `{"listIndexes": "c"}`} {
		d := prepare(doc(t, text), limits)
		cursor, ok := lookup(d, "cursor")
		inner, _ := cursor.(bson.D)
		if !ok || len(inner) != 1 || inner[0].Key != "batchSize" || inner[0].Value != int32(4) {
			t.Errorf("%s: cursor %v", text, cursor)
		}
	}

	if d := prepare(doc(t, `{"count": "c"}`), limits); len(d) != 2 {
		t.Errorf("a command with no cursor was given one: %v", d)
	}
}

func TestACommandThatCannotBeReadAsExtendedJSONIsTheAgentsMistake(t *testing.T) {
	var query *sqlrun.QueryError
	_, err := decode(`{"find": "c", "filter": {"_id": {"$oid": "zz"}}}`)
	if !errors.As(err, &query) || !strings.Contains(query.Message, "extended JSON") {
		t.Fatalf("error %v", err)
	}
}

func raw(t *testing.T, text string) bson.Raw {
	t.Helper()
	out, err := bson.Marshal(doc(t, text))
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestACursorAnswerBecomesOneRowForEachDocumentAndIsCutAtTheRowLimit(t *testing.T) {
	reply := raw(t, `{"cursor": {"firstBatch": [{"a": 1}, {"a": 2}, {"a": 3}, {"a": 4}], "id": {"$numberLong": "77"}, "ns": "app.orders"}, "ok": 1, "$audit": {"x": 1}, "operationTime": {"$timestamp": {"t": 1, "i": 1}}}`)

	got, cursor, err := shape(reply, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 3 || !got.Truncated || got.Columns[0] != "document" {
		t.Fatalf("result %+v", got)
	}
	if string(got.Rows[0][0].(json.RawMessage)) != `{"a":1}` {
		t.Fatalf("first row %s", got.Rows[0][0])
	}
	if cursor.id != 77 || cursor.collection != "orders" {
		t.Fatalf("the cursor left open is %+v", cursor)
	}
}

func TestACursorThatHasMoreThanItReturnedIsTruncatedAndOnePastItsEndIsNot(t *testing.T) {
	more := raw(t, `{"cursor": {"firstBatch": [{"a": 1}], "id": {"$numberLong": "5"}, "ns": "app.c"}, "ok": 1}`)
	if got, cursor, _ := shape(more, limits); !got.Truncated || cursor.id != 5 {
		t.Errorf("more to read: %+v %+v", got, cursor)
	}
	done := raw(t, `{"cursor": {"firstBatch": [{"a": 1}], "id": {"$numberLong": "0"}, "ns": "app.c"}, "ok": 1}`)
	if got, cursor, _ := shape(done, limits); got.Truncated || cursor.id != 0 || len(got.Rows) != 1 {
		t.Errorf("all read: %+v %+v", got, cursor)
	}
	empty := raw(t, `{"cursor": {"firstBatch": [], "id": {"$numberLong": "0"}, "ns": "app.c"}, "ok": 1}`)
	if got, _, _ := shape(empty, limits); len(got.Rows) != 0 || got.Truncated {
		t.Errorf("no documents: %+v", got)
	}
}

func TestAnAnswerWithoutACursorIsOneDocumentWithoutTheServerBookkeeping(t *testing.T) {
	reply := raw(t, `{"n": 4, "ok": 1, "$audit": {"t": {"$timestamp": {"t": 1, "i": 1}}}, "operationTime": {"$timestamp": {"t": 1, "i": 1}}}`)
	got, cursor, err := shape(reply, limits)
	if err != nil || cursor.id != 0 || len(got.Rows) != 1 {
		t.Fatalf("result %+v %+v %v", got, cursor, err)
	}
	text := string(got.Rows[0][0].(json.RawMessage))
	if text != `{"n":4,"ok":1}` {
		t.Fatalf("document %s", text)
	}
}

func TestDocumentsAreCutAtTheByteBudgetWithoutSplittingOne(t *testing.T) {
	big := strings.Repeat("x", 400)
	reply := raw(t, `{"cursor": {"firstBatch": [{"a": "`+big+`"}, {"a": "`+big+`"}, {"a": "`+big+`"}], "id": 0, "ns": "app.c"}, "ok": 1}`)
	got, _, _ := shape(reply, limits)
	if len(got.Rows) != 2 || !got.Truncated {
		t.Fatalf("rows %d, truncated %v", len(got.Rows), got.Truncated)
	}
	for _, row := range got.Rows {
		if !json.Valid(row[0].(json.RawMessage)) {
			t.Fatal("a document was cut in the middle")
		}
	}

	huge := raw(t, `{"cursor": {"firstBatch": [{"a": "`+strings.Repeat("x", 2000)+`"}], "id": 0, "ns": "app.c"}, "ok": 1}`)
	if got, _, _ := shape(huge, limits); len(got.Rows) != 0 || !got.Truncated {
		t.Errorf("a document over the whole budget: %+v", got)
	}
	single := raw(t, `{"values": ["`+strings.Repeat("x", 2000)+`"], "ok": 1}`)
	if got, _, _ := shape(single, limits); len(got.Rows) != 0 || !got.Truncated {
		t.Errorf("an answer over the whole budget: %+v", got)
	}
}

func TestExtendedJSONKeepsTypesAReaderCanTellApart(t *testing.T) {
	reply := raw(t, `{"cursor": {"firstBatch": [{"_id": {"$oid": "507f1f77bcf86cd799439011"}, "at": {"$date": "2024-01-02T03:04:05Z"}, "n": 3, "f": 1.5}], "id": 0, "ns": "app.c"}, "ok": 1}`)
	got, _, _ := shape(reply, limits)
	text := string(got.Rows[0][0].(json.RawMessage))
	for _, want := range []string{`"_id":{"$oid":"507f1f77bcf86cd799439011"}`, `"$date":"2024-01-02T03:04:05Z"`, `"n":3`, `"f":1.5`} {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not hold %s", text, want)
		}
	}
}

func TestWhatTheDriverSaidBecomesTheSentenceTheDataServerKnows(t *testing.T) {
	selection := func(msg string) error {
		return topology.ServerSelectionError{Wrapped: errors.New(msg)}
	}
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"auth":              {selection(`connection() error occurred during connection handshake: auth error: sasl conversation error: unable to authenticate using mechanism "SCRAM-SHA-256": (AuthenticationFailed) Authentication failed.`), sqlrun.ErrRefused},
		"command auth":      {mongo.CommandError{Code: 18, Name: "AuthenticationFailed", Message: "Authentication failed."}, sqlrun.ErrRefused},
		"tls":               {selection(`connection() error occurred during connection handshake: tls: failed to verify certificate: x509: certificate signed by unknown authority`), sqlrun.ErrTLS},
		"no tls":            {selection(`remote error: tls: handshake failure`), sqlrun.ErrTLS},
		"nothing listening": {selection(`dial tcp 127.0.0.1:1: connect: connection refused`), sqlrun.ErrUnreachable},
		"selection timeout": {selection(`context deadline exceeded`), sqlrun.ErrUnreachable},
		"max time":          {mongo.CommandError{Code: 50, Name: "MaxTimeMSExpired", Message: "operation exceeded time limit"}, sqlrun.ErrTimeout},
		"deadline":          {context.DeadlineExceeded, sqlrun.ErrTimeout},
		"not authorized":    {mongo.CommandError{Code: 13, Name: "Unauthorized", Message: "not authorized on app to execute command"}, nil},
	} {
		got := classify(tc.err, "secret")
		if tc.want == nil {
			var query *sqlrun.QueryError
			if !errors.As(got, &query) || query.Message != "not authorized on app to execute command" {
				t.Errorf("%s: %v", name, got)
			}

			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestAServerMessageNeverRepeatsThePassword(t *testing.T) {
	got := classify(mongo.CommandError{Code: 2, Name: "BadValue", Message: "bad value for password hunter2"}, "hunter2")
	var query *sqlrun.QueryError
	if !errors.As(got, &query) || strings.Contains(query.Message, "hunter2") {
		t.Fatalf("error %v", got)
	}
	if got := classify(errors.New("dial hunter2"), "hunter2"); strings.Contains(got.Error(), "hunter2") {
		t.Fatalf("error %v", got)
	}
}
