package api

import (
	"encoding/json"
	"fmt"
	"path"

	"sigs.k8s.io/yaml"
)

// CloudTemplateGrants says whether the bootstrap template allows an action to
// one of the roles it makes. A refusal of an action the template grants means a
// role was edited or an account policy overrides it, and the sentence a person
// reads points at the right one. A condition on a grant is not weighed: the
// question is whether the template ever grants it.
func CloudTemplateGrants() (func(action string) bool, error) {
	var tpl struct {
		Resources map[string]struct {
			Type       string `json:"Type"`
			Properties struct {
				PolicyDocument policyDocument `json:"PolicyDocument"`
				Policies       []struct {
					PolicyDocument policyDocument `json:"PolicyDocument"`
				} `json:"Policies"`
			} `json:"Properties"`
		} `json:"Resources"`
	}
	if err := yaml.Unmarshal([]byte(cloudConnectionTemplate), &tpl); err != nil {
		return nil, fmt.Errorf("read the bootstrap template: %w", err)
	}

	var patterns []string
	collect := func(doc policyDocument) {
		for _, statement := range doc.Statement {
			if statement.Effect == "Allow" {
				patterns = append(patterns, statement.Action...)
			}
		}
	}
	for _, resource := range tpl.Resources {
		collect(resource.Properties.PolicyDocument)
		for _, inline := range resource.Properties.Policies {
			collect(inline.PolicyDocument)
		}
	}

	return func(action string) bool {
		if action == "" {
			return false
		}
		for _, pattern := range patterns {
			if ok, _ := path.Match(pattern, action); ok {
				return true
			}
		}

		return false
	}, nil
}

type policyDocument struct {
	Statement []struct {
		Effect string     `json:"Effect"`
		Action stringList `json:"Action"`
	} `json:"Statement"`
}

// stringList reads a policy field that is one string or a list of them.
type stringList []string

func (l *stringList) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*l = []string{one}

		return nil
	}
	var many []string

	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*l = many

	return nil
}
