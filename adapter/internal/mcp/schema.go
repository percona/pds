package mcp

import (
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaOf infers a tool's schema from its Go type. It panics on a type the
// inference refuses, which is a mistake in the tool and shows on the first
// test run.
func schemaOf[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic("mcp: infer schema: " + err.Error())
	}
	tidy(schema)

	return schema
}

// tidy makes the inferred schema say what the catalog says. Inference types
// every pointer and slice as nullable, so a client would be told a value can
// be null when the tool never sends one.
func tidy(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	if len(schema.Types) == 2 && slices.Contains(schema.Types, "null") {
		for _, kind := range schema.Types {
			if kind != "null" {
				schema.Type = kind
			}
		}
		schema.Types = nil
	}
	for _, property := range schema.Properties {
		tidy(property)
	}
	tidy(schema.Items)
}

// resolve compiles a schema for validating arguments. It panics on a schema
// that does not compile, for the same reason schemaOf does.
func resolve(schema *jsonschema.Schema) *jsonschema.Resolved {
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic("mcp: resolve schema: " + err.Error())
	}

	return resolved
}
