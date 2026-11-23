package api_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/mcp"
)

// Customer copy is text a signed-in person can read. The join script and the
// agent talk to /api/internal, and that copy is left out.

var appErrorConstructors = map[string]bool{
	"Validation":     true,
	"NotFound":       true,
	"Conflict":       true,
	"Downstream":     true,
	"NotImplemented": true,
	"Unauthorized":   true,
	"Internal":       true,
}

var pendingCalls = map[string]bool{
	"pending":       true,
	"pendingLogs":   true,
	"pendingAlerts": true,
}

var pendingFuncs = map[string]bool{
	"monitoringPendingReason": true,
	"pmmNotUp":                true,
	"alertCopy":               true,
}

var (
	bannedNames = regexp.MustCompile(`\bPMM\b|\bFlux\b|\b[Tt]he (hub|adapter)\b|\b[Tt]his (hub|adapter)\b|\b[Ss]pokes?\b`)
	clusterWord = regexp.MustCompile(`(?i)\bclusters?\b`)
	infraPrefix = regexp.MustCompile(`(?i)(?:kubernetes|eks)\s+$`)
	emDash      = "\u2014"
	enDash      = "\u2013"
)

type copyHit struct {
	at, text, problem string
}

func TestCustomerCopy(t *testing.T) {
	var hits []copyHit
	for _, message := range customerMessages(t) {
		for _, problem := range problemsFor(message.text) {
			hits = append(hits, copyHit{at: message.at, text: message.text, problem: problem})
		}
	}
	assert.Empty(t, hits)
}

func TestCustomerCopyFindsStrings(t *testing.T) {
	got := map[string]bool{}
	for _, message := range customerMessages(t) {
		got[message.text] = true
	}

	require.Greater(t, len(got), 30)
	assert.Contains(t, got, "That endpoint does not exist.")
	assert.Contains(t, got, "Pods cannot be scheduled. Check the nodes and storage of the Kubernetes cluster.")
	assert.Contains(t, got, "Showing %d of %d alerts in %s. Open monitoring for the rest.")
	assert.NotContains(t, got, "This adapter is not running as a hub.")
	assert.NotContains(t, got, "A cluster name is required.")
}

func TestCustomerCopyRejectsAFixture(t *testing.T) {
	problems := problemsFor("Pick a cluster, then ask the hub, PMM, Flux, and the spoke. The adapter can wait — or use an en dash – here.")

	for _, want := range []string{"cluster", "the hub", "PMM", "Flux", "spoke", "The adapter", "dash"} {
		assert.Contains(t, problems, want)
	}
	assert.Contains(t, problemsFor("This adapter failed."), "This adapter")
}

func TestCustomerCopyReadsEveryToolSentence(t *testing.T) {
	got := map[string]bool{}
	for _, message := range registryToolCopy(t) {
		got[message.text] = true
	}

	assert.Contains(t, got, "List databases", "a title")
	assert.Contains(t, got, "Get one database: status, size, backup schedule, who can connect, and connection details without the password. "+
		"A database is usable when status is ready and connection is present. If message names a blocking problem, tell the user.", "a description")
	assert.Contains(t, got, "Only the databases on this environment.", "an input schema description")
	assert.Contains(t, got, "Pass this as since to get only newer entries.", "an output schema description")

	fixed := map[string]bool{}
	for _, message := range toolSentences(t, filepath.Join(moduleRoot(t), "internal", "mcp")) {
		fixed[message.text] = true
	}
	assert.Contains(t, fixed, "The control plane could not complete that request. Try again in a moment.")
}

func TestCustomerCopyRejectsAToolThatNamesAnInternalPart(t *testing.T) {
	tool := &sdk.Tool{
		Name:        "fixture",
		Title:       "Ask the hub",
		Description: "Reads PMM. Lists every cluster, then asks the adapter.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"environment": map[string]any{"type": "string", "description": "A spoke."}},
		},
	}

	var problems []string
	for _, message := range toolCopy(t, tool) {
		problems = append(problems, problemsFor(message.text)...)
	}

	for _, want := range []string{"the hub", "PMM", "cluster", "the adapter", "spoke"} {
		assert.Contains(t, problems, want)
	}
}

func TestCustomerCopyReadsTheInstructionsAndEveryPrompt(t *testing.T) {
	var places []string
	got := map[string]bool{}
	for _, message := range registryPromptCopy(t) {
		places = append(places, message.at)
		got[message.text] = true
	}

	assert.Contains(t, got, "Create a database for an app", "a prompt title")
	for _, want := range []string{
		"instructions at change_delete", "prompt new_database description", "prompt new_database argument engine description", "prompt connect_app text",
	} {
		assert.True(t, slices.ContainsFunc(places, func(at string) bool { return strings.HasPrefix(at, want) }), want)
	}
}

func TestCustomerCopyRejectsAPromptThatNamesAnInternalPart(t *testing.T) {
	prompt := &sdk.Prompt{
		Name:        "fixture",
		Title:       "Ask the hub",
		Description: "Reads PMM \u2014 then asks the adapter.",
		Arguments:   []*sdk.PromptArgument{{Name: "environment", Description: "A spoke."}},
	}

	var problems []string
	for _, message := range promptCopy(prompt, "Check every cluster, then ask Flux. Use an en dash \u2013 here.") {
		problems = append(problems, problemsFor(message.text)...)
	}

	for _, want := range []string{"the hub", "PMM", "dash", "the adapter", "spoke", "cluster", "Flux"} {
		assert.Contains(t, problems, want)
	}
}

func TestCustomerCopyAllowsInfrastructure(t *testing.T) {
	assert.Empty(t, problemsFor("Use the Kubernetes cluster or the EKS cluster."))
	assert.Empty(t, problemsFor("The script is create-cluster."))
}

func TestCustomerCopyReadsAnAppErrorFixture(t *testing.T) {
	src := `package api
func public() {
	_ = Validation("Create a cluster.")
}
`
	messages := appErrorMessages(t, src, nil)
	require.Equal(t, []string{"Create a cluster."}, texts(messages))
	assert.Contains(t, problemsFor(messages[0].text), "cluster")
}

type located struct {
	at, text string
}

func customerMessages(t *testing.T) []located {
	t.Helper()

	root := moduleRoot(t)
	var out []located
	out = append(out, appErrors(t, filepath.Join(root, "internal", "api"))...)
	out = append(out, pendingReasons(t, filepath.Join(root, "internal", "service"))...)
	out = append(out, serviceNowLabels(t, filepath.Join(root, "internal", "service", "support.go"))...)
	out = append(out, platformCopy(t, filepath.Join(root, "internal", "service"))...)
	out = append(out, activityNotes(t, filepath.Join(root, "internal", "service", "activity_detail.go"))...)
	out = append(out, inventoryMessages(t, filepath.Join(root, "internal", "hub"))...)
	out = append(out, startBlockerCopy(t, filepath.Join(root, "internal", "engine", "oe", "templates.go"))...)
	out = append(out, toolSentences(t, filepath.Join(root, "internal", "mcp"))...)
	out = append(out, registryToolCopy(t)...)
	out = append(out, registryPromptCopy(t)...)

	return out
}

// toolSentences reads the fixed sentences the MCP tools answer with. They are
// the constants of internal/mcp whose names start with msg.
func toolSentences(t *testing.T, dir string) []located {
	t.Helper()

	fset, files := parseDir(t, dir)
	var out []located
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) == 0 || len(value.Values) == 0 || !strings.HasPrefix(value.Names[0].Name, "msg") {
					continue
				}
				if text, ok := stringLiteral(value.Values[0]); ok {
					out = append(out, located{at: fset.Position(value.Pos()).String(), text: text})
				}
			}
		}
	}

	return out
}

// registryToolCopy lists the registry's tools the way a client does and reads
// what a client shows an agent and its user: each title, description and
// schema description.
func registryToolCopy(t *testing.T) []located {
	t.Helper()

	// The highest tier lists every tool, and the client kind is what lets it.
	caller := auth.Caller{Identity: auth.Identity{Client: auth.Client{Kind: auth.ClientChat}}, Access: auth.AccessChangeDelete}

	// MongoDB's words are read with the engine switch on as well, because they
	// reach customers on the day it is.
	var out []located
	for _, mongodb := range []bool{false, true} {
		session, err := mcp.New(nil, slog.New(slog.DiscardHandler), "test", mcp.WithMongoDB(mongodb)).Connect(t.Context(), caller)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })

		list, err := session.ListTools(t.Context(), nil)
		require.NoError(t, err)
		require.NotEmpty(t, list.Tools)
		var listed []string
		for _, tool := range list.Tools {
			listed = append(listed, tool.Name)
		}
		require.Contains(t, listed, "delete_database", "the copy of the change tools is read too")

		for _, tool := range list.Tools {
			out = append(out, toolCopy(t, tool)...)
		}
	}

	return out
}

// promptArguments are values that pass each prompt argument's check.
var promptArguments = map[string]string{
	"database":    "prod-eu/pds.orders-dev",
	"engine":      "mysql",
	"environment": "prod-eu",
	"name":        "orders-dev",
}

// registryPromptCopy reads what a client shows from the registry's prompts at
// every tier: the server instructions, each prompt's title, description and
// arguments, and the text each one answers with, filled in and left empty.
func registryPromptCopy(t *testing.T) []located {
	t.Helper()

	var out []located
	for _, access := range []auth.Access{auth.AccessRead, auth.AccessChange, auth.AccessChangeDelete} {
		for _, mongodb := range []bool{false, true} {
			caller := auth.Caller{Identity: auth.Identity{Client: auth.Client{Kind: auth.ClientChat}}, Access: access}
			session, err := mcp.New(nil, slog.New(slog.DiscardHandler), "test", mcp.WithMongoDB(mongodb)).Connect(t.Context(), caller)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			instructions := session.InitializeResult().Instructions
			require.NotEmpty(t, instructions)
			out = append(out, located{at: "instructions at " + string(access), text: instructions})

			list, err := session.ListPrompts(t.Context(), nil)
			require.NoError(t, err)
			require.NotEmpty(t, list.Prompts)
			for _, prompt := range list.Prompts {
				filled := map[string]string{}
				required := map[string]string{}
				for _, argument := range prompt.Arguments {
					filled[argument.Name] = promptArguments[argument.Name]
					if mongodb && argument.Name == "engine" {
						filled[argument.Name] = "mongodb"
					}
					if argument.Required {
						required[argument.Name] = promptArguments[argument.Name]
					}
				}
				for _, arguments := range []map[string]string{filled, required} {
					got, err := session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: prompt.Name, Arguments: arguments})
					require.NoError(t, err)
					for _, message := range got.Messages {
						content, ok := message.Content.(*sdk.TextContent)
						require.True(t, ok, prompt.Name)
						out = append(out, promptCopy(prompt, content.Text)...)
					}
				}
			}
		}
	}

	return out
}

// promptCopy is every sentence of a prompt a client can show, and the text it
// answered with.
func promptCopy(prompt *sdk.Prompt, answer string) []located {
	at := "prompt " + prompt.Name
	out := []located{
		{at: at + " title", text: prompt.Title},
		{at: at + " description", text: prompt.Description},
		{at: at + " text", text: answer},
	}
	for _, argument := range prompt.Arguments {
		out = append(out,
			located{at: at + " argument " + argument.Name + " title", text: argument.Title},
			located{at: at + " argument " + argument.Name + " description", text: argument.Description},
		)
	}

	var present []located
	for _, message := range out {
		if message.text != "" {
			present = append(present, message)
		}
	}

	return present
}

// toolCopy is every sentence of a tool a client can show.
func toolCopy(t *testing.T, tool *sdk.Tool) []located {
	t.Helper()

	var out []located
	add := func(where, text string) {
		if text != "" {
			out = append(out, located{at: "tool " + tool.Name + " " + where, text: text})
		}
	}
	add("title", tool.Title)
	add("description", tool.Description)
	for _, schema := range []struct {
		where string
		value any
	}{{"input", tool.InputSchema}, {"output", tool.OutputSchema}} {
		body, err := json.Marshal(schema.value)
		require.NoError(t, err)
		var parsed any
		require.NoError(t, json.Unmarshal(body, &parsed))
		for _, description := range schemaDescriptions(parsed) {
			add(schema.where+" schema", description)
		}
	}

	return out
}

// schemaDescriptions is every description in a JSON schema, at any depth.
func schemaDescriptions(node any) []string {
	var out []string
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if text, ok := child.(string); ok && key == "description" {
				out = append(out, text)

				continue
			}
			out = append(out, schemaDescriptions(child)...)
		}
	case []any:
		for _, child := range value {
			out = append(out, schemaDescriptions(child)...)
		}
	}

	return out
}

// startBlockerCopy reads the sentences that say why a database's pods are not
// up. The console shows them as the database's status message.
func startBlockerCopy(t *testing.T, path string) []located {
	t.Helper()

	fset, file := parseFile(t, path)
	var out []located
	for _, name := range []string{"startBlockerMessage", "unschedulableMessage"} {
		fn := funcByName(file, name)
		if fn == nil || fn.Body == nil {
			t.Fatalf("%s is missing", name)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if text, ok := stringLiteral(asExpr(n)); ok && text != "" {
				out = append(out, located{at: fset.Position(n.Pos()).String(), text: text})
			}

			return true
		})
	}

	return out
}

func appErrors(t *testing.T, dir string) []located {
	t.Helper()

	fset, files := parseDir(t, dir)
	excluded := internalOnly(files)
	var out []located
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || excluded[fn.Name.Name] {
				continue
			}
			out = append(out, callsIn(fset, fn.Body, func(call *ast.CallExpr) (string, bool) {
				return constructorMessage(call)
			})...)
		}
	}

	return out
}

func appErrorMessages(t *testing.T, src string, excluded map[string]bool) []located {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err)

	var out []located
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || excluded[fn.Name.Name] {
			continue
		}
		out = append(out, callsIn(fset, fn.Body, func(call *ast.CallExpr) (string, bool) {
			return constructorMessage(call)
		})...)
	}

	return out
}

// internalOnly is every function that serves /api/internal and nothing a
// customer calls. A helper stays in the set only while every caller is already
// in it, so a helper shared with a public route is still checked.
func internalOnly(files []*ast.File) map[string]bool {
	defined := map[string]bool{}
	callers := map[string]map[string]bool{}
	excluded := map[string]bool{}

	for _, file := range files {
		for _, name := range handlerNames(file) {
			excluded[name] = true
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			defined[fn.Name.Name] = true
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := calleeName(call)
				if !ok {
					return true
				}
				if callers[name] == nil {
					callers[name] = map[string]bool{}
				}
				callers[name][fn.Name.Name] = true

				return true
			})
		}
	}

	changed := true
	for changed {
		changed = false
		for name := range defined {
			if excluded[name] {
				continue
			}
			from := callers[name]
			if len(from) == 0 {
				continue
			}
			onlyInternal := true
			for caller := range from {
				if !excluded[caller] {
					onlyInternal = false
					break
				}
			}
			if onlyInternal {
				excluded[name] = true
				changed = true
			}
		}
	}

	return excluded
}

func handlerNames(file *ast.File) []string {
	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		internal := false
		handler := ""
		for _, arg := range call.Args {
			if text, ok := stringLiteral(arg); ok && strings.HasPrefix(text, "/api/internal/") {
				internal = true
			}
			sel, ok := arg.(*ast.SelectorExpr)
			if ok {
				handler = sel.Sel.Name
			}
		}
		if internal && handler != "" {
			names = append(names, handler)
		}

		return true
	})

	return names
}

func pendingReasons(t *testing.T, dir string) []located {
	t.Helper()

	fset, files := parseDir(t, dir)
	consts := constStrings(files)
	var out []located
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if pendingFuncs[fn.Name.Name] {
				out = append(out, returnedStrings(fset, fn.Body)...)
			}
			out = append(out, callsIn(fset, fn.Body, func(call *ast.CallExpr) (string, bool) {
				name, ok := calleeName(call)
				if !ok || !pendingCalls[name] {
					return "", false
				}
				for _, arg := range call.Args {
					if text, ok := stringLiteral(arg); ok {
						return text, true
					}
					ident, ok := arg.(*ast.Ident)
					if ok && consts[ident.Name] != "" {
						return consts[ident.Name], true
					}
				}

				return "", false
			})...)
		}
	}

	return out
}

func serviceNowLabels(t *testing.T, path string) []located {
	t.Helper()

	fset, file := parseFile(t, path)
	fn := funcByName(file, "AssembleDescription")
	if fn == nil || fn.Body == nil {
		t.Fatal("AssembleDescription is missing")
	}

	var out []located
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 2 {
			return true
		}
		text, ok := stringLiteral(lit.Elts[0])
		if !ok {
			return true
		}
		out = append(out, located{at: fset.Position(lit.Pos()).String(), text: text})

		return true
	})

	return out
}

func asExpr(n ast.Node) ast.Expr {
	expr, _ := n.(ast.Expr)

	return expr
}

func platformCopy(t *testing.T, dir string) []located {
	t.Helper()

	fset, files := parseDir(t, dir)
	consts := constStrings(files)
	var out []located
	for _, file := range files {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		if name != "platform.go" && name != "platform_discovery.go" && name != "everest_health.go" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if text, ok := fieldText(node.Key, node.Value, consts); ok {
					out = append(out, located{at: fset.Position(node.Pos()).String(), text: text})
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if i >= len(node.Rhs) {
						continue
					}
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					if text, ok := fieldText(sel.Sel, node.Rhs[i], consts); ok {
						out = append(out, located{at: fset.Position(node.Pos()).String(), text: text})
					}
				}
			}

			return true
		})
	}

	return out
}

func fieldText(key ast.Expr, value ast.Expr, consts map[string]string) (string, bool) {
	ident, ok := key.(*ast.Ident)
	if !ok || (ident.Name != "Label" && ident.Name != "Detail" && ident.Name != "Fix") {
		return "", false
	}
	if text, ok := stringLiteral(value); ok {
		return text, true
	}
	name, ok := value.(*ast.Ident)
	if ok && consts[name.Name] != "" {
		return consts[name.Name], true
	}

	return "", false
}

func activityNotes(t *testing.T, path string) []located {
	t.Helper()

	fset, file := parseFile(t, path)
	var out []located
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !notesAppend(call) {
			return true
		}
		for _, arg := range call.Args[1:] {
			if text, ok := stringLiteral(arg); ok {
				out = append(out, located{at: fset.Position(arg.Pos()).String(), text: text})
			}
		}

		return true
	})

	return out
}

func notesAppend(call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "append" || len(call.Args) < 2 {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)

	return ok && sel.Sel.Name == "Notes"
}

func inventoryMessages(t *testing.T, dir string) []located {
	t.Helper()

	fset, files := parseDir(t, dir)
	var out []located
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Message" || i >= len(assign.Rhs) {
					continue
				}
				if text, ok := stringLiteral(assign.Rhs[i]); ok {
					out = append(out, located{at: fset.Position(assign.Pos()).String(), text: text})
				}
			}

			return true
		})
	}

	return out
}

func constructorMessage(call *ast.CallExpr) (string, bool) {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || !appErrorConstructors[ident.Name] || len(call.Args) == 0 {
		return "", false
	}

	return stringLiteral(call.Args[0])
}

func callsIn(fset *token.FileSet, body ast.Node, take func(*ast.CallExpr) (string, bool)) []located {
	var out []located
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		text, ok := take(call)
		if ok && text != "" {
			out = append(out, located{at: fset.Position(call.Pos()).String(), text: text})
		}

		return true
	})

	return out
}

func returnedStrings(fset *token.FileSet, body ast.Node) []located {
	var out []located
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			if text, ok := stringLiteral(result); ok && text != "" {
				out = append(out, located{at: fset.Position(result.Pos()).String(), text: text})
			}
		}

		return true
	})

	return out
}

func stringLiteral(expr ast.Expr) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(node.Value)
		if err != nil {
			return "", false
		}

		return text, true
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		// A name or a table spliced into a sentence is not copy. The literal
		// parts on either side still are.
		left, lok := stringLiteral(node.X)
		right, rok := stringLiteral(node.Y)
		if !lok && !rok {
			return "", false
		}

		return left + right, true
	case *ast.ParenExpr:
		return stringLiteral(node.X)
	case *ast.CallExpr:
		if !isSprintf(node) || len(node.Args) == 0 {
			return "", false
		}

		return stringLiteral(node.Args[0])
	default:
		return "", false
	}
}

func isSprintf(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "Sprintf"
	case *ast.SelectorExpr:
		return fun.Sel.Name == "Sprintf"
	default:
		return false
	}
}

func calleeName(call *ast.CallExpr) (string, bool) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, true
	case *ast.SelectorExpr:
		return fun.Sel.Name, true
	default:
		return "", false
	}
}

func constStrings(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if text, ok := stringLiteral(value.Values[i]); ok {
						out[name.Name] = text
					}
				}
			}
		}
	}

	return out
}

func funcByName(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}

	return nil
}

func problemsFor(text string) []string {
	var problems []string
	problems = append(problems, bannedNames.FindAllString(text, -1)...)
	if bareCluster(text) {
		problems = append(problems, "cluster")
	}
	if strings.Contains(text, emDash) || strings.Contains(text, enDash) {
		problems = append(problems, "dash")
	}

	return problems
}

func bareCluster(text string) bool {
	for _, match := range clusterWord.FindAllStringIndex(text, -1) {
		start := match[0]
		if infraPrefix.MatchString(text[:start]) {
			continue
		}
		if start > 0 {
			switch text[start-1] {
			case '-', '.', '/':
				continue
			}
		}

		return true
	}

	return false
}

func texts(messages []located) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.text)
	}

	return out
}

func parseDir(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)
		files = append(files, file)
	}
	require.NotEmpty(t, files)

	return fset, files
}

func parseFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	return fset, file
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	return filepath.Join(repoRoot(t), "adapter")
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
