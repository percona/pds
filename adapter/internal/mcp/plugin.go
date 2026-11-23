package mcp

import "github.com/percona/pds/adapter/internal/auth"

// PluginText is the registry's words as the plugin ships them, read by the
// generator in cmd/plugin-guidance so that the skill and the rule say what
// /mcp says.
type PluginText struct {
	// Instructions is what a client reads when it connects with every tool
	// allowed. What a session may do is narrower when its token is.
	Instructions string
	// Nudge is the offer to make PDS when an app needs a database.
	Nudge   string
	Prompts []PluginPrompt
}

// PluginPrompt is a workflow a person starts from the client's prompt list.
type PluginPrompt struct {
	Name        string
	Title       string
	Description string
}

// PluginGuidanceText returns the instructions at the widest tier, the offer
// and the prompts, in the order the registry lists them.
func PluginGuidanceText() PluginText {
	g := loadGuidance(MongoDBEnabled)
	text := PluginText{Instructions: g.instructionsFor(auth.AccessChangeDelete), Nudge: g.nudge}
	for _, p := range g.prompts {
		text.Prompts = append(text.Prompts, PluginPrompt{Name: p.Name, Title: p.Title, Description: p.Description})
	}

	return text
}
