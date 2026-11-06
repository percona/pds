package github

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

const deployKeyTitle = "pds-flux"

// legacyDeployKeyTitle is the title a release wrote before the product
// was renamed to PDS. Disconnect still has to delete those keys so a
// disconnected spoke cannot clone the reused repository.
const legacyDeployKeyTitle = "basecamp-flux" // allow-legacy-name

// EnsureCloneKey mints a read-only deploy key on the environment repo and
// returns the OpenSSH identity Flux should store. The org token stays here.
func (c *Client) EnsureCloneKey(ctx context.Context, environment string) (identity, pub string, err error) {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return "", "", fmt.Errorf("environment name is empty")
	}
	if err := c.EnsureRepo(ctx, environment); err != nil {
		return "", "", err
	}
	identity, pub, err = generateDeployKey()
	if err != nil {
		return "", "", err
	}
	if err := c.postDeployKey(ctx, environment, pub); err != nil {
		return "", "", err
	}

	return identity, pub, nil
}

// EnsureDeployKey posts pub as a read-only deploy key when GitHub does not
// already have it. Existing keys stay, including a working Flux clone.
func (c *Client) EnsureDeployKey(ctx context.Context, environment, pub string) error {
	environment = strings.TrimSpace(environment)
	pub = strings.TrimSpace(pub)
	if environment == "" {
		return fmt.Errorf("environment name is empty")
	}
	if pub == "" {
		return fmt.Errorf("deploy key is empty")
	}
	if err := c.EnsureRepo(ctx, environment); err != nil {
		return err
	}
	keys, err := c.listDeployKeys(ctx, environment)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if strings.TrimSpace(key.Key) == pub {
			return nil
		}
	}

	return c.postDeployKey(ctx, environment, pub)
}

// RevokeCloneKey deletes every pds-flux deploy key on the environment repo,
// and every key titled the way a pre-rename release wrote it. New keys
// are still created as pds-flux only. A later join mints a new identity,
// so a disconnected spoke cannot clone the reused repository.
func (c *Client) RevokeCloneKey(ctx context.Context, environment string) error {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return fmt.Errorf("environment name is empty")
	}
	keys, err := c.listDeployKeys(ctx, environment)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !revokeDeployKeyTitle(key.Title) || key.ID == 0 {
			continue
		}
		status, raw, err := c.do(ctx, http.MethodDelete, "/repos/"+c.org+"/"+environment+"/keys/"+strconv.Itoa(key.ID), nil)
		if err != nil {
			return err
		}
		if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
			return fmt.Errorf("github delete deploy key %s/%s: HTTP %d: %s", c.org, environment, status, truncate(raw, 200))
		}
	}

	return nil
}

func revokeDeployKeyTitle(title string) bool {
	return title == deployKeyTitle || title == legacyDeployKeyTitle
}

func generateDeployKey() (identity, pub string, err error) {
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(edPriv, "")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(edPub)
	if err != nil {
		return "", "", err
	}

	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

type deployKey struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Key   string `json:"key"`
}

func (c *Client) listDeployKeys(ctx context.Context, repo string) ([]deployKey, error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/keys?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github list deploy keys %s/%s: HTTP %d: %s", c.org, repo, status, truncate(raw, 200))
	}
	var keys []deployKey
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}

	return keys, nil
}

func (c *Client) postDeployKey(ctx context.Context, repo, pub string) error {
	payload, err := json.Marshal(map[string]any{
		"title":     deployKeyTitle,
		"key":       strings.TrimSpace(pub),
		"read_only": true,
	})
	if err != nil {
		return err
	}
	status, raw, err := c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/keys", payload)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		return nil
	}
	if status == http.StatusUnprocessableEntity {
		keys, err := c.listDeployKeys(ctx, repo)
		if err != nil {
			return err
		}
		want := strings.TrimSpace(pub)
		for _, key := range keys {
			if strings.TrimSpace(key.Key) == want {
				return nil
			}
		}
	}

	return fmt.Errorf("github create deploy key %s/%s: HTTP %d: %s", c.org, repo, status, truncate(raw, 200))
}
