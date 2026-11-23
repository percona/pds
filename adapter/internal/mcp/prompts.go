package mcp

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"text/template"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine/oe"
	"github.com/percona/pds/adapter/internal/hub"
)

// The sentences a prompt answers with when an argument is refused. They name
// the argument and never repeat its value.
const (
	msgPromptArgumentMissing = "A required prompt argument is missing."
	msgPromptArgumentInvalid = "A prompt argument is not valid."
)

// guidanceFiles holds the words the registry says to an agent: the server
// instructions, the offer to make PDS, and one file per prompt. They are text
// so that the plugin and the Chat read the same words, and the customer copy
// test reads them as a client receives them.
//
//go:embed guidance
var guidanceFiles embed.FS

// guidance is the instructions and prompts, read once when the registry is
// built.
type guidance struct {
	nudge        string
	instructions *template.Template
	prompts      []prompt
	// engines are the engines a person may ask for, which the prompt check
	// and the words about engines follow.
	engines []offered
}

// A prompt is a workflow a person starts from a client's prompt list.
type prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Access      auth.Access      `json:"access"`
	Arguments   []promptArgument `json:"arguments"`
	text        *template.Template
}

type promptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	text        *template.Template
}

// maxDatabaseIDLength is the longest id that can hold two names of 63
// characters, a slash and a dot.
const maxDatabaseIDLength = 2*63 + 2

var accessTiers = []auth.Access{auth.AccessRead, auth.AccessChange, auth.AccessChangeDelete}

// promptChecks is what each prompt argument may hold. A value is placed in
// text an agent follows, so a value that is not the shape of a name or an id
// is refused rather than passed on.
func promptChecks(engines []offered) map[string]func(string) bool {
	return map[string]func(string) bool{
		"database":    func(value string) bool { return len(value) <= maxDatabaseIDLength && databaseID.MatchString(value) },
		"engine":      func(value string) bool { return isOffered(engines, value) },
		"environment": hub.ValidEnvironmentName,
		"name":        hub.ValidName,
	}
}

// loadGuidance reads and checks every file. It panics on a mistake in one,
// like a tool's schema does, which shows on the first test run. mongodb is the
// engine switch.
func loadGuidance(mongodb bool) guidance {
	read := func(name string) string {
		body, err := fs.ReadFile(guidanceFiles, path.Join("guidance", name))
		if err != nil {
			panic("mcp: read " + name + ": " + err.Error())
		}

		return string(body)
	}

	g := guidance{
		engines:      offeredEngines(mongodb),
		nudge:        strings.TrimSpace(read("nudge.md")),
		instructions: template.Must(template.New("instructions").Option("missingkey=zero").Parse(read("instructions.md"))),
	}

	files, err := fs.Glob(guidanceFiles, "guidance/prompts/*.md")
	if err != nil || len(files) == 0 {
		panic("mcp: no prompt files")
	}
	for _, file := range files {
		g.prompts = append(g.prompts, parsePrompt(file, read(strings.TrimPrefix(file, "guidance/")), promptChecks(g.engines)))
	}

	return g
}

// parsePrompt splits a prompt file into its front matter and its text.
func parsePrompt(file, content string, checks map[string]func(string) bool) prompt {
	front, body, found := strings.Cut(strings.TrimPrefix(content, "---\n"), "\n---\n")
	if !found || !strings.HasPrefix(content, "---\n") {
		panic("mcp: " + file + " has no front matter")
	}

	var p prompt
	if err := yaml.UnmarshalStrict([]byte(front), &p); err != nil {
		panic("mcp: " + file + ": " + err.Error())
	}
	if p.Name == "" || p.Title == "" || p.Description == "" || !slices.Contains(accessTiers, p.Access) || path.Base(file) != p.Name+".md" {
		panic("mcp: " + file + " needs a name that matches the file, a title, a description and an access tier")
	}
	var err error
	for i, argument := range p.Arguments {
		if checks[argument.Name] == nil || argument.Description == "" {
			panic("mcp: " + file + ": argument " + argument.Name + " needs a check and a description")
		}
		if p.Arguments[i].text, err = template.New(argument.Name).Option("missingkey=zero").Parse(argument.Description); err != nil {
			panic("mcp: " + file + ": " + err.Error())
		}
	}

	if p.text, err = template.New(p.Name).Option("missingkey=zero").Parse(strings.TrimSpace(body)); err != nil {
		panic("mcp: " + file + ": " + err.Error())
	}

	return p
}

// render runs a template with the tier's facts and the values it was given. A
// value that was not given is empty, so the text asks for it.
func render(t *template.Template, values map[string]string) string {
	var out bytes.Buffer
	// The templates parse at load and read only strings, so this does not fail.
	if err := t.Execute(&out, values); err != nil {
		panic("mcp: render " + t.Name() + ": " + err.Error())
	}

	return strings.TrimSpace(out.String())
}

// facts are what the text may promise at a tier: a tool above it is not
// listed, so no text names one.
func (g guidance) facts(access auth.Access) map[string]string {
	facts := map[string]string{
		"EngineList":   joinWords(engineLabels(g.engines), "and"),
		"EngineChoice": joinWords(engineLabels(g.engines), "or"),
		"EngineIDs":    joinWords(engineIDs(g.engines), "or"),
	}
	if isOffered(g.engines, oe.MongoDB.ID) {
		facts["MongoDB"] = "yes"
	}
	if access.Allows(auth.AccessChange) {
		facts["Changes"] = "yes"
	}
	if access.Allows(auth.AccessChangeDelete) {
		facts["Deletes"] = "yes"
	}

	return facts
}

// instructionsFor is what a client reads when it connects, for a caller with
// this access.
func (g guidance) instructionsFor(access auth.Access) string {
	facts := g.facts(access)
	facts["Nudge"] = g.nudge

	return render(g.instructions, facts)
}

// addPrompts registers the prompts the access includes.
func (g guidance) addPrompts(server *sdk.Server, access auth.Access) {
	checks := promptChecks(g.engines)
	for _, p := range g.prompts {
		if !access.Allows(p.Access) {
			continue
		}

		arguments := make([]*sdk.PromptArgument, 0, len(p.Arguments))
		for _, argument := range p.Arguments {
			arguments = append(arguments, &sdk.PromptArgument{Name: argument.Name, Description: render(argument.text, g.facts(access)), Required: argument.Required})
		}
		listed := &sdk.Prompt{Name: p.Name, Title: p.Title, Description: p.Description, Arguments: arguments}

		server.AddPrompt(listed, func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			values, err := p.values(req.Params.Arguments, checks)
			if err != nil {
				return nil, err
			}
			facts := g.facts(access)
			for name, value := range values {
				facts[name] = value
			}

			return &sdk.GetPromptResult{
				Description: p.Description,
				Messages:    []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: render(p.text, facts)}}},
			}, nil
		})
	}
}

// values checks the arguments a client sent against the prompt's own. An
// argument the prompt does not name is ignored, as a tool ignores a field it
// does not name.
func (p prompt) values(sent map[string]string, checks map[string]func(string) bool) (map[string]string, error) {
	values := map[string]string{}
	for _, argument := range p.Arguments {
		value := sent[argument.Name]
		if value == "" {
			if argument.Required {
				return nil, promptRefusal(msgPromptArgumentMissing, argument.Name)
			}

			continue
		}
		if !checks[argument.Name](value) {
			return nil, promptRefusal(msgPromptArgumentInvalid, argument.Name)
		}
		values[argument.Name] = value
	}

	return values, nil
}

func promptRefusal(message, argument string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("%s Check the %s argument.", message, argument)}
}
