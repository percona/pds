package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/service"
)

func TestTheAuthorizationServerNeedsAnOriginForItsIssuer(t *testing.T) {
	for url, want := range map[string]string{
		"https://pds.example":       "https://pds.example",
		"https://pds.example/":      "https://pds.example",
		" https://pds.example ":     "https://pds.example",
		"http://localhost:8080":     "http://localhost:8080",
		"https://pds.example/base":  "",
		"https://pds.example?x=1":   "",
		"https://pds.example#frag":  "",
		"ftp://pds.example":         "",
		"https://user@pds.example":  "",
		"pds.example":               "",
		"":                          "",
		"https://":                  "",
		"http://pds.example":        "",
		"http://localhost.evil.com": "",
	} {
		assert.Equal(t, want, oauthIssuer(config.Config{HubPublicURL: url}), url)
	}
}

func TestNoAuthorizationServerIsBuiltWithoutAnIssuerOrADatabase(t *testing.T) {
	identity := service.NewIdentity(nil)

	assert.Nil(t, newOAuthService(config.Config{HubPublicURL: "https://pds.example/base"}, nil, identity, nil, nil, nil))
	assert.Nil(t, newOAuthService(config.Config{HubPublicURL: "https://pds.example"}, nil, identity, nil, nil, nil), "no database")
}

func TestTheIssuerIsTheOneSetApartFromThePublicURLWhenThereIsOne(t *testing.T) {
	// The lab's hub is reached by the connected environments at a name that is not a valid
	// plain http issuer, and by a person's browser and agent at the loopback.
	lab := config.Config{HubPublicURL: "http://host.docker.internal:18080", HubOAuthIssuer: "http://127.0.0.1:18080"}
	assert.Equal(t, "http://127.0.0.1:18080", oauthIssuer(lab))

	assert.Empty(t, oauthIssuer(config.Config{HubPublicURL: "http://host.docker.internal:18080"}), "without the setting the public URL decides")

	assert.Equal(t, "https://agents.example", oauthIssuer(config.Config{
		HubPublicURL: "https://pds.example", HubOAuthIssuer: " https://agents.example/ ",
	}))
}

func TestTheSettingFollowsTheSameRulesAsThePublicURL(t *testing.T) {
	for url, want := range map[string]string{
		"http://pds.example":          "",
		"http://localhost.evil.com":   "",
		"https://agents.example/base": "",
		"https://user@agents.example": "",
		"https://agents.example?x=1":  "",
		"ftp://agents.example":        "",
		"http://[::1]:8080":           "http://[::1]:8080",
		"http://127.0.0.1:18080":      "http://127.0.0.1:18080",
	} {
		// A usable public URL does not rescue an unusable issuer.
		assert.Equal(t, want, oauthIssuer(config.Config{HubPublicURL: "https://pds.example", HubOAuthIssuer: url}), url)
	}
}
