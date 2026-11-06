// Command plugin-guidance writes the files of plugins/pds that come from the
// registry's guidance: the skill and the Cursor rule. Nobody edits them by
// hand; the test next to this file fails when they differ from what this
// writes.
//
//	cd adapter && go run ./cmd/plugin-guidance ../plugins/pds
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/percona/pds/adapter/internal/mcp"
)

// The words only the plugin says: when to use the skill, how signing in
// works from a client, and what the second server does.
//
//go:embed skill.md.tmpl rule.mdc.tmpl
var templates embed.FS

// generate returns every file, keyed by its path under the plugin directory.
func generate(text mcp.PluginText) map[string]string {
	facts := struct {
		mcp.PluginText
		Description string
	}{PluginText: text, Description: description(text)}

	return map[string]string{
		"skills/databases/SKILL.md": render("skill.md.tmpl", facts),
		"rules/use-pds.mdc":         render("rule.mdc.tmpl", facts),
	}
}

// description tells an agent what the skill is for and when to reach for it:
// the registry's first sentence, then its offer. It is quoted so that a colon
// in the text stays part of one YAML string.
func description(text mcp.PluginText) string {
	_, afterHeading, _ := strings.Cut(text.Instructions, "\n\n")
	first, _, _ := strings.Cut(afterHeading, "\n")
	summary, _, _ := strings.Cut(first, ". ")

	return fmt.Sprintf("%q", strings.TrimSuffix(summary, ".")+". "+text.Nudge)
}

func render(name string, facts any) string {
	// The templates are embedded and read plain strings, so neither step fails once a test has run.
	parsed := template.Must(template.ParseFS(templates, name))

	var out strings.Builder
	if err := parsed.Execute(&out, facts); err != nil {
		panic("plugin-guidance: render " + name + ": " + err.Error())
	}

	return out.String()
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: plugin-guidance <plugin directory>")
		os.Exit(2)
	}

	for path, content := range generate(mcp.PluginGuidanceText()) {
		target := filepath.Join(os.Args[1], path)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil { //nolint:gosec // G306: the plugin's files are public text that is checked in
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
