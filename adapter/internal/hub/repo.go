package hub

import (
	"path/filepath"
	"strconv"
	"strings"
)

// repoPartLimit caps the organization and the team in a repository name so the
// whole name stays under GitHub's 100 characters beside a 63 character
// environment name.
const repoPartLimit = 16

// GitDir is the directory a laptop hub writes desired state into.
//
// When prefix is false the tree stays at root, which is the layout of a hub that
// serves one Organization. When prefix is true each Team gets a subdirectory
// named by its id, so two Teams with an environment called prod do not share
// files. Flux paths inside that tree stay clusters/<environment>/.
func GitDir(root string, team uint, prefix bool) string {
	root = strings.TrimSpace(root)
	if !prefix || team == 0 {
		return root
	}

	return filepath.Join(root, strconv.FormatUint(uint64(team), 10))
}

// RepoPrefix is the organization-team half of a new environment's repository
// name. Both are free text in the database, so each becomes a lower case slug
// cut to repoPartLimit. A blank part is left out.
func RepoPrefix(organization, team string) string {
	parts := make([]string, 0, 2)
	for _, part := range []string{organization, team} {
		if slug := repoSlug(part); slug != "" {
			parts = append(parts, slug)
		}
	}

	return strings.Join(parts, "-")
}

func repoSlug(text string) string {
	var out strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && out.Len() > 0 {
				out.WriteByte('-')
			}
			dash = false
			out.WriteRune(r)

			continue
		}
		dash = true
	}

	slug := out.String()
	if len(slug) > repoPartLimit {
		slug = strings.TrimRight(slug[:repoPartLimit], "-")
	}

	return slug
}
