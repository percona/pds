package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The support routes need a ServiceNow behind them, which the router tests
// here do not have, so their answers are checked against the field names the
// handler's own types carry. A tool that named a field the API calls something
// else would drop it without a sound.
func TestSupportToolsNameOnlyFieldsTheAPISends(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "api", "support.go"), nil, 0)
	require.NoError(t, err)

	sent := map[string]map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		structure, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		sent[spec.Name.Name] = map[string]bool{}
		for _, field := range structure.Fields.List {
			if field.Tag == nil {
				continue
			}
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
			sent[spec.Name.Name][strings.Split(tag, ",")[0]] = true
		}

		return true
	})

	for tool, api := range map[reflect.Type]string{
		reflect.TypeFor[ticket]():        "ticketResponse",
		reflect.TypeFor[ticketSummary](): "ticketResponse",
		reflect.TypeFor[ticketComment](): "ticketCommentResponse",
		reflect.TypeFor[tickets]():       "ticketListResponse",
	} {
		require.NotEmpty(t, sent[api], api)
		for i := range tool.NumField() {
			field := tool.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.Anonymous || name == "" {
				continue
			}
			assert.True(t, sent[api][name], "%s.%s is not a field of %s", tool.Name(), name, api)
		}
	}
}
