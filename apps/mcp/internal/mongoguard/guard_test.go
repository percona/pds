package mongoguard_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/mongoguard"
)

const database = "app"

func check(t *testing.T, text string, mode mongoguard.Mode) (mongoguard.Command, error) {
	t.Helper()

	return mongoguard.Check(text, database, mode)
}

func TestTheReadCommandsAreAcceptedAndRunAsReads(t *testing.T) {
	for name, text := range map[string]string{
		"find":            `{"find": "orders", "filter": {"total": {"$gt": 5}}, "projection": {"_id": 0}, "sort": {"total": -1}, "limit": 10, "skip": 2, "hint": {"$natural": 1}, "collation": {"locale": "en"}, "comment": "report", "let": {"min": 5}, "maxTimeMS": 500}`,
		"aggregate":       `{"aggregate": "orders", "pipeline": [{"$match": {"a": 1}}, {"$group": {"_id": "$a", "n": {"$sum": 1}}}, {"$sort": {"n": -1}}, {"$limit": 5}], "cursor": {"batchSize": 10}, "allowDiskUse": true}`,
		"aggregate joins": `{"aggregate": "orders", "pipeline": [{"$lookup": {"from": "items", "let": {"id": "$_id"}, "pipeline": [{"$match": {"$expr": {"$eq": ["$o", "$$id"]}}}], "as": "items"}}, {"$unionWith": {"coll": "archive", "pipeline": [{"$match": {}}]}}, {"$facet": {"a": [{"$count": "n"}], "b": [{"$sample": {"size": 2}}]}}], "cursor": {}}`,
		"count":           `{"count": "orders", "query": {"a": 1}, "limit": 5, "hint": "a_1"}`,
		"distinct":        `{"distinct": "orders", "key": "status", "query": {"a": 1}}`,
		"listCollections": `{"listCollections": 1, "filter": {"name": "orders"}, "nameOnly": true}`,
		"listIndexes":     `{"listIndexes": "orders"}`,
		"listDatabases":   `{"listDatabases": 1, "nameOnly": true}`,
		"collStats":       `{"collStats": "orders", "scale": 1024}`,
		"dbStats":         `{"dbStats": 1, "scale": 1}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			cmd, err := check(t, text, mode)
			if err != nil {
				t.Errorf("%s in mode %d: %v", name, mode, err)

				continue
			}
			if cmd.Kind != mongoguard.Read {
				t.Errorf("%s ran as a write", name)
			}
		}
	}
}

func TestListDatabasesRunsOnTheAdminDatabaseAndTheRestOnTheConnectionsDatabase(t *testing.T) {
	for text, want := range map[string]string{
		`{"listDatabases": 1}`: "admin", `{"find": "c"}`: "app", `{"dbStats": 1}`: "app",
	} {
		cmd, err := check(t, text, mongoguard.ReadOnly)
		if err != nil || cmd.Database != want {
			t.Errorf("%s: database %q, error %v, want %q", text, cmd.Database, err, want)
		}
	}
}

func TestTheWriteCommandsAreRefusedByTheReadToolAndAcceptedWithTheWriteGrant(t *testing.T) {
	for name, text := range map[string]string{
		"insert":           `{"insert": "orders", "documents": [{"a": 1}, {"a": 2}], "ordered": true}`,
		"update":           `{"update": "orders", "updates": [{"q": {"a": 1}, "u": {"$set": {"a": 2}}, "upsert": true, "multi": true}]}`,
		"update pipeline":  `{"update": "orders", "updates": [{"q": {}, "u": [{"$set": {"a": 2}}]}]}`,
		"delete":           `{"delete": "orders", "deletes": [{"q": {"a": 1}, "limit": 1}]}`,
		"findAndModify":    `{"findAndModify": "orders", "query": {"a": 1}, "update": {"$inc": {"n": 1}}, "new": true, "upsert": true}`,
		"createIndexes":    `{"createIndexes": "orders", "indexes": [{"key": {"a": 1}, "name": "a_1", "unique": true}]}`,
		"create":           `{"create": "orders", "capped": true, "size": 4096}`,
		"drop":             `{"drop": "orders"}`,
		"aggregate $out":   `{"aggregate": "orders", "pipeline": [{"$match": {}}, {"$out": "copy"}], "cursor": {}}`,
		"aggregate $merge": `{"aggregate": "orders", "pipeline": [{"$merge": {"into": "copy"}}], "cursor": {}}`,
		"$out own db":      `{"aggregate": "orders", "pipeline": [{"$out": {"db": "app", "coll": "copy"}}], "cursor": {}}`,
	} {
		_, err := check(t, text, mongoguard.ReadOnly)
		var refusal *mongoguard.Refusal
		if err == nil || !asRefusal(err, &refusal) {
			t.Errorf("%s was accepted by the read tool", name)

			continue
		}
		wanted := messages.MongoNotRead
		if strings.HasPrefix(name, "aggregate") || strings.HasPrefix(name, "$out") {
			wanted = messages.MongoWriteStage
		}
		if err.Error() != wanted {
			t.Errorf("%s: reason %q", name, err)
		}

		cmd, err := check(t, text, mongoguard.ReadWrite)
		if err != nil || cmd.Kind != mongoguard.Write {
			t.Errorf("%s with the grant: kind %v, error %v", name, cmd.Kind, err)
		}
	}
}

func asRefusal(err error, target **mongoguard.Refusal) bool {
	r, ok := err.(*mongoguard.Refusal)
	*target = r

	return ok
}

func TestAWriteStageHiddenInASubPipelineIsNeverARead(t *testing.T) {
	for name, text := range map[string]string{
		"$facet":     `{"aggregate": "c", "pipeline": [{"$facet": {"x": [{"$out": "z"}]}}], "cursor": {}}`,
		"$lookup":    `{"aggregate": "c", "pipeline": [{"$lookup": {"from": "d", "pipeline": [{"$merge": {"into": "z"}}], "as": "r"}}], "cursor": {}}`,
		"$unionWith": `{"aggregate": "c", "pipeline": [{"$unionWith": {"coll": "d", "pipeline": [{"$out": "z"}]}}], "cursor": {}}`,
		"deep":       `{"aggregate": "c", "pipeline": [{"$facet": {"x": [{"$lookup": {"from": "d", "pipeline": [{"$unionWith": {"coll": "e", "pipeline": [{"$out": "z"}]}}], "as": "r"}}]}}], "cursor": {}}`,
	} {
		if _, err := check(t, text, mongoguard.ReadOnly); err == nil {
			t.Errorf("%s: a nested write stage was accepted by the read tool", name)
		}
		// Even with the grant, a write stage is only accepted as the last stage of the top level.
		if _, err := check(t, text, mongoguard.ReadWrite); err == nil || err.Error() != messages.MongoWriteStageMisplaced {
			t.Errorf("%s with the grant: %v", name, err)
		}
	}

	if _, err := check(t, `{"aggregate": "c", "pipeline": [{"$out": "z"}, {"$match": {}}], "cursor": {}}`, mongoguard.ReadWrite); err == nil || err.Error() != messages.MongoWriteStageMisplaced {
		t.Errorf("a write stage that is not last: %v", err)
	}
}

func TestAWriteStageThatNamesAnotherDatabaseIsRefusedEvenWithTheGrant(t *testing.T) {
	for name, text := range map[string]string{
		"$merge":     `{"aggregate": "c", "pipeline": [{"$merge": {"into": {"db": "other", "coll": "x"}}}], "cursor": {}}`,
		"$out":       `{"aggregate": "c", "pipeline": [{"$out": {"db": "admin", "coll": "x"}}], "cursor": {}}`,
		"$lookup":    `{"aggregate": "c", "pipeline": [{"$lookup": {"from": {"db": "other", "coll": "x"}, "localField": "a", "foreignField": "b", "as": "r"}}], "cursor": {}}`,
		"$unionWith": `{"aggregate": "c", "pipeline": [{"$unionWith": {"coll": {"db": "other", "coll": "x"}}}], "cursor": {}}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			if _, err := check(t, text, mode); err == nil {
				t.Errorf("%s was accepted in mode %d", name, mode)
			}
		}
	}
	if _, err := check(t, `{"aggregate": "c", "pipeline": [{"$merge": {"into": {"db": "app", "coll": "x"}, "on": "_id"}}], "cursor": {}}`, mongoguard.ReadWrite); err != nil {
		t.Errorf("a merge into the connection's own database: %v", err)
	}
}

func TestServerSideJavaScriptIsRefusedEverywhereAndInBothTools(t *testing.T) {
	for name, text := range map[string]string{
		"find $where":              `{"find": "c", "filter": {"$where": "sleep(1000)"}}`,
		"find $where nested":       `{"find": "c", "filter": {"$and": [{"a": 1}, {"$or": [{"$where": "true"}]}]}}`,
		"count $where":             `{"count": "c", "query": {"$where": "true"}}`,
		"distinct $where":          `{"distinct": "c", "key": "a", "query": {"$where": "true"}}`,
		"$function in $addFields":  `{"aggregate": "c", "pipeline": [{"$addFields": {"x": {"$function": {"body": "function(){return 1}", "args": [], "lang": "js"}}}}], "cursor": {}}`,
		"$accumulator in $group":   `{"aggregate": "c", "pipeline": [{"$group": {"_id": 1, "x": {"$accumulator": {"init": "function(){}", "accumulate": "function(){}", "accumulateArgs": [], "merge": "function(){}", "lang": "js"}}}}], "cursor": {}}`,
		"$expr with $function":     `{"find": "c", "filter": {"$expr": {"$function": {"body": "x", "args": [], "lang": "js"}}}}`,
		"$function in $facet":      `{"aggregate": "c", "pipeline": [{"$facet": {"a": [{"$project": {"x": {"$function": {}}}}]}}], "cursor": {}}`,
		"$function in $lookup":     `{"aggregate": "c", "pipeline": [{"$lookup": {"from": "d", "pipeline": [{"$match": {"$where": "1"}}], "as": "r"}}], "cursor": {}}`,
		"$function in let":         `{"find": "c", "filter": {}, "let": {"f": {"$function": {}}}}`,
		"$where in hint":           `{"find": "c", "hint": {"$where": "1"}}`,
		"$where in collation":      `{"count": "c", "collation": {"$where": "1"}}`,
		"$where unicode escape":    `{"find": "c", "filter": {"\u0024where": "1"}}`,
		"$where escaped in full":   `{"find": "c", "filter": {"\u0024\u0077here": "1"}}`,
		"$function escape":         `{"find": "c", "filter": {"$expr": {"\u0024function": {}}}}`,
		"$function unicode escape": `{"find": "c", "filter": {"$expr": {"$function": {}}}}`,
		"upper case $WHERE":        `{"find": "c", "filter": {"$WHERE": "1"}}`,
		"internal JS operator":     `{"aggregate": "c", "pipeline": [{"$project": {"x": {"$_internalJsEmit": {}}}}], "cursor": {}}`,
		"$code wrapper":            `{"find": "c", "filter": {"a": {"$code": "function(){}"}}}`,
		"$scope wrapper":           `{"insert": "c", "documents": [{"a": {"$code": "x", "$scope": {}}}]}`,
		"mapReduce":                `{"mapReduce": "c", "map": "function(){}", "reduce": "function(){}", "out": "z"}`,
		"eval":                     `{"eval": "db.c.drop()"}`,
		"$eval":                    `{"$eval": "1"}`,
		"update with $function":    `{"update": "c", "updates": [{"q": {}, "u": [{"$set": {"x": {"$function": {}}}}]}]}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			_, err := check(t, text, mode)
			if err == nil || err.Error() != messages.MongoJavaScript {
				t.Errorf("%s in mode %d: %v", name, mode, err)
			}
		}
	}
}

func TestACommandOnNoListIsRefusedInBothTools(t *testing.T) {
	for _, name := range []string{
		"createUser", "createRole", "dropUser", "dropRole", "updateUser", "grantRolesToUser", "revokeRolesFromUser", "grantPrivilegesToRole",
		"dropDatabase", "dropIndexes", "collMod", "renameCollection", "convertToCapped", "compact", "shutdown", "replSetReconfig",
		"replSetStepDown", "killOp", "killCursors", "killAllSessions", "fsync", "fsyncUnlock", "setParameter", "getParameter", "setFeatureCompatibilityVersion",
		"currentOp", "serverStatus", "buildInfo", "hello", "isMaster", "ping", "explain", "getMore", "getLog", "listCommands", "connectionStatus",
		"usersInfo", "rolesInfo", "profile", "validate", "reIndex", "logout", "authenticate", "saslStart", "enableSharding", "shardCollection",
		"moveChunk", "mapReduce", "group", "geoSearch", "applyOps", "cloneCollectionAsCapped", "internalRenameIfOptionsAndIndexesMatch",
		"FIND", "Find", "fInd", "DROP", "Insert", "LISTDATABASES", "find ", "find\u0000", "$find", "",
	} {
		text := `{` + quote(name) + `: "c"}`
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			cmd, err := check(t, text, mode)
			if err == nil {
				t.Errorf("%q was accepted in mode %d: %+v", name, mode, cmd)
			}
		}
	}
}

func quote(s string) string {
	raw, _ := json.Marshal(s)

	return string(raw)
}

func TestUserManagementAndServerCommandsAreSaidToBeAdministration(t *testing.T) {
	for _, name := range []string{"createUser", "createRole", "dropUser", "grantRolesToUser", "dropDatabase", "dropIndexes", "collMod", "renameCollection", "shutdown", "replSetReconfig", "killOp", "fsync", "setParameter"} {
		_, err := check(t, `{"`+name+`": 1}`, mongoguard.ReadWrite)
		if err == nil || err.Error() != messages.MongoAdmin {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := check(t, `{"somethingNew": 1}`, mongoguard.ReadWrite); err == nil || !strings.Contains(err.Error(), "somethingNew") {
		t.Errorf("an unknown command: %v", err)
	}
}

func TestOnlyOneCommandIsAcceptedAndNothingElseIsHiddenInTheText(t *testing.T) {
	for name, text := range map[string]string{
		"empty":                 ``,
		"blank":                 "  \n ",
		"array of commands":     `[{"find": "c"}, {"drop": "c"}]`,
		"two documents":         `{"find": "c"} {"drop": "c"}`,
		"two lines":             "{\"find\": \"c\"}\n{\"drop\": \"c\"}",
		"trailing text":         `{"find": "c"} ; db.c.drop()`,
		"trailing comma":        `{"find": "c",}`,
		"shell syntax":          `db.c.find({})`,
		"single quotes":         `{'find': 'c'}`,
		"unquoted keys":         `{find: "c"}`,
		"a string":              `"find"`,
		"a number":              `1`,
		"null":                  `null`,
		"an empty object":       `{}`,
		"no command first":      `{"filter": {}, "find": "c"}`,
		"command value missing": `{"find": 1}`,
		"collection not string": `{"find": {"a": 1}}`,
		"collection empty":      `{"find": ""}`,
		"duplicate command":     `{"find": "c", "find": "d"}`,
		"duplicate field":       `{"find": "c", "filter": {}, "filter": {"$where": "1"}}`,
		"duplicate nested":      `{"find": "c", "filter": {"a": 1, "a": 2}}`,
		"NaN":                   `{"find": "c", "filter": {"a": NaN}}`,
		"comment":               `/* x */ {"find": "c"}`,
		"byte order mark":       string(rune(0xFEFF)) + `{"find": "c"}`,
		"null in a key":         `{"find": "c", "fil` + "\x00" + `ter": {}}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			if cmd, err := check(t, text, mode); err == nil {
				t.Errorf("%s was accepted in mode %d: %+v", name, mode, cmd)
			}
		}
	}
}

func TestASecondCommandSmuggledAsAFieldIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"read then drop":       `{"find": "c", "drop": "c"}`,
		"drop then read":       `{"drop": "c", "find": "c"}`,
		"insert as a read":     `{"find": "c", "insert": "c", "documents": [{"a": 1}]}`,
		"aggregate and insert": `{"aggregate": "c", "pipeline": [], "insert": "c", "cursor": {}}`,
		"count and delete":     `{"count": "c", "delete": "c", "deletes": [{"q": {}, "limit": 0}]}`,
		"distinct and update":  `{"distinct": "c", "key": "a", "update": "c", "updates": []}`,
		"insert and find":      `{"insert": "c", "documents": [], "find": "c"}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			if _, err := check(t, text, mode); err == nil {
				t.Errorf("%s was accepted in mode %d", name, mode)
			}
		}
	}
}

func TestAFieldThatChangesHowTheCommandRunsIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"$db":                    `{"find": "c", "$db": "admin"}`,
		"lsid":                   `{"find": "c", "lsid": {"id": {"$binary": {"base64": "AAAA", "subType": "04"}}}}`,
		"txnNumber":              `{"insert": "c", "documents": [], "txnNumber": 1}`,
		"readConcern":            `{"find": "c", "readConcern": {"level": "snapshot"}}`,
		"writeConcern":           `{"insert": "c", "documents": [], "writeConcern": {"w": 0}}`,
		"$readPreference":        `{"find": "c", "$readPreference": {"mode": "secondary"}}`,
		"$audit":                 `{"find": "c", "$audit": {}}`,
		"explain field":          `{"aggregate": "c", "pipeline": [{"$match": {}}], "explain": true}`,
		"explain command":        `{"explain": {"aggregate": "c", "pipeline": [{"$out": "z"}], "cursor": {}}, "verbosity": "executionStats"}`,
		"tailable":               `{"find": "c", "tailable": true, "awaitData": true}`,
		"noCursorTimeout":        `{"find": "c", "noCursorTimeout": true}`,
		"bypassValidation":       `{"insert": "c", "documents": [], "bypassDocumentValidation": true}`,
		"comment document":       `{"find": "c", "comment": {"a": 1}}`,
		"let on count":           `{"count": "c", "let": {}}`,
		"cursor with options":    `{"aggregate": "c", "pipeline": [], "cursor": {"batchSize": 1, "other": 1}}`,
		"update statement field": `{"update": "c", "updates": [{"q": {}, "u": {}, "x": 1}]}`,
		"update without q":       `{"update": "c", "updates": [{"u": {}}]}`,
		"delete without limit":   `{"delete": "c", "deletes": [{"q": {}}]}`,
		"index storage engine":   `{"createIndexes": "c", "indexes": [{"key": {"a": 1}, "name": "a", "storageEngine": {}}]}`,
		"pipeline not an array":  `{"aggregate": "c", "pipeline": {"$match": {}}, "cursor": {}}`,
		"pipeline missing":       `{"aggregate": "c", "cursor": {}}`,
		"stage with two keys":    `{"aggregate": "c", "pipeline": [{"$match": {}, "$out": "z"}], "cursor": {}}`,
		"stage not an object":    `{"aggregate": "c", "pipeline": ["$out"], "cursor": {}}`,
		"empty stage":            `{"aggregate": "c", "pipeline": [{}], "cursor": {}}`,
		"filter not a document":  `{"find": "c", "filter": "a"}`,
		"documents not an array": `{"insert": "c", "documents": {"a": 1}}`,
		"maxTimeMS not a number": `{"find": "c", "maxTimeMS": "1"}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			if _, err := check(t, text, mode); err == nil {
				t.Errorf("%s was accepted in mode %d", name, mode)
			}
		}
	}
}

func TestAggregationStagesThatReachTheServerOrOtherSessionsAreRefused(t *testing.T) {
	for _, stage := range []string{"$currentOp", "$listSessions", "$listLocalSessions", "$listSampledQueries", "$changeStream", "$documents", "$planCacheStats", "$search", "$searchMeta", "$vectorSearch", "$queryStats", "$listCatalog", "$shardedDataDistribution", "$_internalSplitPipeline", "$unknown", "$OUT", "$Merge"} {
		text := `{"aggregate": "c", "pipeline": [{` + quote(stage) + `: {}}], "cursor": {}}`
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			_, err := check(t, text, mode)
			if err == nil || !strings.Contains(err.Error(), "aggregation stage") {
				t.Errorf("%s in mode %d: %v", stage, mode, err)
			}
		}
	}

	if _, err := check(t, `{"aggregate": 1, "pipeline": [{"$currentOp": {}}], "cursor": {}}`, mongoguard.ReadOnly); err == nil {
		t.Error("a database level aggregate was accepted")
	}
	if _, err := check(t, `{"aggregate": "c", "pipeline": [{"$match": {}}, {"$unknownStage": {}}], "cursor": {}}`, mongoguard.ReadOnly); err == nil {
		t.Error("an unknown stage after a known one was accepted")
	}
	if _, err := check(t, `{"aggregate": "c", "pipeline": [{"$facet": {"a": [{"$currentOp": {}}]}}], "cursor": {}}`, mongoguard.ReadOnly); err == nil {
		t.Error("an admin stage inside $facet was accepted")
	}
}

func TestTheDatabasesOwnCollectionsAreNeverReadOrChanged(t *testing.T) {
	for name, text := range map[string]string{
		"find":        `{"find": "system.users"}`,
		"aggregate":   `{"aggregate": "system.keys", "pipeline": [], "cursor": {}}`,
		"lookup":      `{"aggregate": "c", "pipeline": [{"$lookup": {"from": "system.users", "localField": "a", "foreignField": "b", "as": "r"}}], "cursor": {}}`,
		"lookup sub":  `{"aggregate": "c", "pipeline": [{"$unionWith": "system.roles"}], "cursor": {}}`,
		"graphLookup": `{"aggregate": "c", "pipeline": [{"$graphLookup": {"from": "system.users", "startWith": "$a", "connectFromField": "a", "connectToField": "b", "as": "r"}}], "cursor": {}}`,
		"insert":      `{"insert": "system.js", "documents": [{"_id": "f", "value": 1}]}`,
		"drop":        `{"drop": "system.views"}`,
		"out":         `{"aggregate": "c", "pipeline": [{"$out": "system.js"}], "cursor": {}}`,
		"merge":       `{"aggregate": "c", "pipeline": [{"$merge": {"into": "system.users"}}], "cursor": {}}`,
		"distinct":    `{"distinct": "system.users", "key": "user"}`,
		"count":       `{"count": "system.users"}`,
	} {
		for _, mode := range []mongoguard.Mode{mongoguard.ReadOnly, mongoguard.ReadWrite} {
			if _, err := check(t, text, mode); err == nil || err.Error() != messages.MongoSystemCollection && err.Error() != messages.MongoWriteStage {
				t.Errorf("%s in mode %d: %v", name, mode, err)
			}
		}
	}
}

func TestTheCommandThatRunsIsTheOneThatWasChecked(t *testing.T) {
	cmd, err := check(t, `{"\u0066ind": "orders",   "filter": {"name": "a\u0022b", "n": 12345678901234567890, "f": 1.50, "t": true, "z": null, "l": [1, "x"]}}`, mongoguard.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "find" || cmd.Kind != mongoguard.Read {
		t.Fatalf("command %+v", cmd)
	}
	want := `{"find":"orders","filter":{"name":"a\"b","n":12345678901234567890,"f":1.50,"t":true,"z":null,"l":[1,"x"]}}`
	if cmd.JSON != want {
		t.Fatalf("json %s, want %s", cmd.JSON, want)
	}
	if !json.Valid([]byte(cmd.JSON)) {
		t.Fatal("not valid JSON")
	}
}

func TestExtendedJSONTypesPassBecauseAnAgentNeedsThemToFindADocument(t *testing.T) {
	text := `{"find": "c", "filter": {"_id": {"$oid": "507f1f77bcf86cd799439011"}, "at": {"$gt": {"$date": "2024-01-01T00:00:00Z"}}, "n": {"$numberLong": "5"}, "r": {"$regex": "^a", "$options": "i"}}}`
	if _, err := check(t, text, mongoguard.ReadOnly); err != nil {
		t.Fatal(err)
	}
}

func TestACommandThatIsTooLongOrTooDeepIsRefused(t *testing.T) {
	long := `{"find": "c", "filter": {"a": "` + strings.Repeat("x", mongoguard.MaxLength) + `"}}`
	if _, err := check(t, long, mongoguard.ReadOnly); err == nil || err.Error() != messages.MongoTooLong {
		t.Errorf("a long command: %v", err)
	}
	deep := `{"find": "c", "filter": ` + strings.Repeat(`{"a": `, 150) + `1` + strings.Repeat(`}`, 150) + `}`
	if _, err := check(t, deep, mongoguard.ReadOnly); err == nil || err.Error() != messages.MongoTooDeep {
		t.Errorf("a deep command: %v", err)
	}
	deepArray := `{"find": "c", "filter": {"a": ` + strings.Repeat(`[`, 150) + strings.Repeat(`]`, 150) + `}}`
	if _, err := check(t, deepArray, mongoguard.ReadOnly); err == nil || err.Error() != messages.MongoTooDeep {
		t.Errorf("a deep array: %v", err)
	}
}

func TestARefusalNeverRepeatsMoreThanAnAgentCanHold(t *testing.T) {
	_, err := check(t, `{"`+strings.Repeat("x\n<b>", 100)+`": 1}`, mongoguard.ReadOnly)
	if err == nil || len(err.Error()) > 600 || strings.ContainsAny(err.Error(), "\n<>") {
		t.Fatalf("refusal %q", err)
	}
}
