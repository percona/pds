package mongoguard

import (
	"encoding/json"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/messages"
)

// check validates one value of a command.
type check func(c *checker, v any) *Refusal

// spec is what one command accepts. A field that is not listed is refused, which is what keeps a
// second command, a different database, a session or a read concern out of the document.
type spec struct {
	kind Kind
	// admin means the server answers the command only on the admin database.
	admin bool
	// collection means the command's value names a collection. Otherwise it is the number 1.
	collection bool
	fields     map[string]check
	required   []string
}

type checker struct {
	command  string
	database string
	// writes is set when an aggregation has a $out or $merge stage.
	writes bool
}

func (c *checker) bad(what string) *Refusal { return refuse(messages.MongoBadShape(c.command, what)) }

// fields checks the command's own value and then each field against the spec.
func (c *checker) fields(doc object, sp spec) *Refusal {
	if sp.collection {
		if r := c.collection(doc[0].value); r != nil {
			return r
		}
	} else if _, ok := doc[0].value.(json.Number); !ok {
		return c.bad("its value has to be the number 1")
	}
	for _, m := range doc[1:] {
		validate, ok := sp.fields[m.key]
		if !ok {
			return refuse(messages.MongoFieldNotAccepted(c.command, m.key))
		}
		if r := validate(c, m.value); r != nil {
			return r
		}
	}
	for _, key := range sp.required {
		if _, ok := doc.get(key); !ok {
			return c.bad(key + " is required")
		}
	}

	return nil
}

// collection checks a collection name. The collections whose names start with system. hold the
// database's users, roles, keys and stored functions, so no command reads or changes them.
func (c *checker) collection(v any) *Refusal {
	name, ok := v.(string)
	if !ok || name == "" || strings.ContainsRune(name, 0) {
		return c.bad("the collection name has to be a non-empty string")
	}
	if strings.HasPrefix(name, "system.") {
		return refuse(messages.MongoSystemCollection)
	}

	return nil
}

// namespace checks where a stage reads or writes: a collection name, or a document with a database
// and a collection. A database other than the connection's is refused.
func (c *checker) namespace(v any) *Refusal {
	o, ok := v.(object)
	if !ok {
		return c.collection(v)
	}
	for _, m := range o {
		switch m.key {
		case "db":
			if db, ok := m.value.(string); !ok || db != c.database {
				return refuse(messages.MongoOtherDatabase)
			}
		case "coll":
			if r := c.collection(m.value); r != nil {
				return r
			}
		default:
			return c.bad("a namespace has only db and coll")
		}
	}

	return nil
}

func isObject(c *checker, v any) *Refusal {
	if _, ok := v.(object); !ok {
		return c.bad("a field that holds a document is not one")
	}

	return nil
}

func isString(c *checker, v any) *Refusal {
	if _, ok := v.(string); !ok {
		return c.bad("a field that holds text is not text")
	}

	return nil
}

func isNumber(c *checker, v any) *Refusal {
	if _, ok := v.(json.Number); !ok {
		return c.bad("a field that holds a number is not one")
	}

	return nil
}

func isBool(c *checker, v any) *Refusal {
	if _, ok := v.(bool); !ok {
		return c.bad("a field that holds true or false is not one")
	}

	return nil
}

func isArray(c *checker, v any) *Refusal {
	if _, ok := v.([]any); !ok {
		return c.bad("a field that holds a list is not one")
	}

	return nil
}

// objectOrString is the shape of a hint.
func objectOrString(c *checker, v any) *Refusal {
	switch v.(type) {
	case object, string:
		return nil
	}

	return c.bad("a hint is an index name or a key document")
}

// objectOrArray is the shape of an update: a document, or a pipeline.
func objectOrArray(c *checker, v any) *Refusal {
	switch v.(type) {
	case object, []any:
		return nil
	}

	return c.bad("an update is a document or a pipeline")
}

// cursorField accepts only a batch size, because the runner sets the one that bounds the result.
func cursorField(c *checker, v any) *Refusal {
	o, ok := v.(object)
	if !ok {
		return c.bad("cursor is a document")
	}
	for _, m := range o {
		if m.key != "batchSize" {
			return c.bad("cursor takes only batchSize")
		}
		if r := isNumber(c, m.value); r != nil {
			return r
		}
	}

	return nil
}

// listOf checks every element of a list as a document with the fields given, and that the
// required ones are there.
func listOf(fields map[string]check, required ...string) check {
	return func(c *checker, v any) *Refusal {
		list, ok := v.([]any)
		if !ok {
			return c.bad("a field that holds a list is not one")
		}
		for _, e := range list {
			o, ok := e.(object)
			if !ok {
				return c.bad("a list that holds documents holds something else")
			}
			for _, m := range o {
				validate, ok := fields[m.key]
				if !ok {
					return refuse(messages.MongoFieldNotAccepted(c.command, m.key))
				}
				if r := validate(c, m.value); r != nil {
					return r
				}
			}
			for _, key := range required {
				if _, ok := o.get(key); !ok {
					return c.bad(key + " is required")
				}
			}
		}

		return nil
	}
}

// documents checks that a list holds only documents.
func documents(c *checker, v any) *Refusal {
	list, ok := v.([]any)
	if !ok {
		return c.bad("a field that holds a list is not one")
	}
	for _, e := range list {
		if _, ok := e.(object); !ok {
			return c.bad("a list that holds documents holds something else")
		}
	}

	return nil
}

// collectionField checks a field that names a collection.
func collectionField(c *checker, v any) *Refusal { return c.collection(v) }

// pipelineField checks the pipeline of an aggregate, which may end in a write stage.
func pipelineField(c *checker, v any) *Refusal { return c.pipeline(v, true) }

// viewPipeline checks the pipeline of a view, which never writes.
func viewPipeline(c *checker, v any) *Refusal { return c.pipeline(v, false) }

// readStages reshape and read documents. Stages that reach the server's own state or other
// sessions ($currentOp, $listSessions, $changeStream) and the full text search stages are not here.
var readStages = map[string]bool{
	"$match": true, "$project": true, "$addFields": true, "$set": true, "$unset": true, "$sort": true, "$limit": true,
	"$skip": true, "$group": true, "$count": true, "$unwind": true, "$lookup": true, "$unionWith": true, "$facet": true,
	"$bucket": true, "$bucketAuto": true, "$sortByCount": true, "$replaceRoot": true, "$replaceWith": true,
	"$sample": true, "$redact": true, "$geoNear": true, "$graphLookup": true, "$collStats": true, "$indexStats": true,
	"$setWindowFields": true, "$densify": true, "$fill": true,
}

// pipeline checks every stage, and the stages of every sub-pipeline, against the list. A write
// stage is accepted only as the last stage of the top level pipeline, where the server runs it.
func (c *checker) pipeline(v any, top bool) *Refusal {
	stages, ok := v.([]any)
	if !ok {
		return c.bad("a pipeline is a list of stages")
	}
	for i, s := range stages {
		stage, ok := s.(object)
		if !ok || len(stage) != 1 {
			return c.bad("each stage is a document with one key")
		}
		name, body := stage[0].key, stage[0].value

		switch name {
		case "$out", "$merge":
			c.writes = true
			if !top || i != len(stages)-1 {
				return refuse(messages.MongoWriteStageMisplaced)
			}
			target := body
			if name == "$merge" {
				merge, ok := body.(object)
				if !ok {
					return c.bad("$merge takes a document")
				}
				if target, ok = merge.get("into"); !ok {
					return c.bad("$merge needs into")
				}
			}
			if r := c.namespace(target); r != nil {
				return r
			}
		case "$lookup", "$graphLookup":
			spec, ok := body.(object)
			if !ok {
				return c.bad(name + " takes a document")
			}
			if from, ok := spec.get("from"); ok {
				if r := c.namespace(from); r != nil {
					return r
				}
			}
			if sub, ok := spec.get("pipeline"); ok {
				if r := c.pipeline(sub, false); r != nil {
					return r
				}
			}
		case "$unionWith":
			if spec, ok := body.(object); ok {
				if coll, ok := spec.get("coll"); ok {
					if r := c.namespace(coll); r != nil {
						return r
					}
				}
				if sub, ok := spec.get("pipeline"); ok {
					if r := c.pipeline(sub, false); r != nil {
						return r
					}
				}
			} else if r := c.namespace(body); r != nil {
				return r
			}
		case "$facet":
			facets, ok := body.(object)
			if !ok {
				return c.bad("$facet takes a document of pipelines")
			}
			for _, f := range facets {
				if r := c.pipeline(f.value, false); r != nil {
					return r
				}
			}
		default:
			if !readStages[name] {
				return refuse(messages.MongoStageNotAccepted(name))
			}
		}
	}

	return nil
}

var (
	// generic are the fields every accepted command may carry.
	generic = map[string]check{"comment": isString, "maxTimeMS": isNumber}

	findFields = merge(generic, map[string]check{
		"filter": isObject, "projection": isObject, "sort": isObject, "skip": isNumber, "limit": isNumber,
		"batchSize": isNumber, "singleBatch": isBool, "hint": objectOrString, "collation": isObject, "let": isObject,
		"min": isObject, "max": isObject, "returnKey": isBool, "showRecordId": isBool, "allowDiskUse": isBool,
	})

	updateFields = map[string]check{
		"q": isObject, "u": objectOrArray, "upsert": isBool, "multi": isBool, "collation": isObject,
		"arrayFilters": isArray, "hint": objectOrString,
	}
	deleteFields = map[string]check{"q": isObject, "limit": isNumber, "collation": isObject, "hint": objectOrString}
	indexFields  = map[string]check{
		"key": isObject, "name": isString, "unique": isBool, "sparse": isBool, "hidden": isBool,
		"expireAfterSeconds": isNumber, "partialFilterExpression": isObject, "collation": isObject,
	}
)

func merge(sets ...map[string]check) map[string]check {
	out := map[string]check{}
	for _, set := range sets {
		for k, v := range set {
			out[k] = v
		}
	}

	return out
}

// commands is every command the data server runs. The first group are reads, the second changes
// data and is refused by the read tool.
var commands = map[string]spec{
	"find": {kind: Read, collection: true, fields: findFields},
	"aggregate": {kind: Read, collection: true, required: []string{"pipeline"}, fields: merge(generic, map[string]check{
		"pipeline": pipelineField, "cursor": cursorField, "allowDiskUse": isBool, "collation": isObject,
		"hint": objectOrString, "let": isObject,
	})},
	"count": {kind: Read, collection: true, fields: merge(generic, map[string]check{
		"query": isObject, "limit": isNumber, "skip": isNumber, "hint": objectOrString, "collation": isObject,
	})},
	"distinct": {kind: Read, collection: true, required: []string{"key"}, fields: merge(generic, map[string]check{
		"key": isString, "query": isObject, "collation": isObject,
	})},
	"listCollections": {kind: Read, fields: merge(generic, map[string]check{
		"filter": isObject, "nameOnly": isBool, "authorizedCollections": isBool, "cursor": cursorField,
	})},
	"listIndexes":   {kind: Read, collection: true, fields: merge(generic, map[string]check{"cursor": cursorField})},
	"listDatabases": {kind: Read, admin: true, fields: merge(generic, map[string]check{"filter": isObject, "nameOnly": isBool, "authorizedDatabases": isBool})},
	"collStats":     {kind: Read, collection: true, fields: merge(generic, map[string]check{"scale": isNumber})},
	"dbStats":       {kind: Read, fields: merge(generic, map[string]check{"scale": isNumber, "freeStorage": isNumber})},

	"insert": {kind: Write, collection: true, required: []string{"documents"}, fields: merge(generic, map[string]check{
		"documents": documents, "ordered": isBool,
	})},
	"update": {kind: Write, collection: true, required: []string{"updates"}, fields: merge(generic, map[string]check{
		"updates": listOf(updateFields, "q", "u"), "ordered": isBool, "let": isObject,
	})},
	"delete": {kind: Write, collection: true, required: []string{"deletes"}, fields: merge(generic, map[string]check{
		"deletes": listOf(deleteFields, "q", "limit"), "ordered": isBool, "let": isObject,
	})},
	"findAndModify": {kind: Write, collection: true, fields: merge(generic, map[string]check{
		"query": isObject, "sort": isObject, "update": objectOrArray, "remove": isBool, "new": isBool, "fields": isObject,
		"upsert": isBool, "collation": isObject, "arrayFilters": isArray, "hint": objectOrString, "let": isObject,
	})},
	"createIndexes": {kind: Write, collection: true, required: []string{"indexes"}, fields: merge(generic, map[string]check{
		"indexes": listOf(indexFields, "key", "name"),
	})},
	"create": {kind: Write, collection: true, fields: merge(generic, map[string]check{
		"capped": isBool, "size": isNumber, "max": isNumber, "validator": isObject, "validationLevel": isString,
		"validationAction": isString, "collation": isObject, "viewOn": collectionField, "pipeline": viewPipeline,
		"timeseries": isObject, "expireAfterSeconds": isNumber,
	})},
	"drop": {kind: Write, collection: true, fields: generic},
}
