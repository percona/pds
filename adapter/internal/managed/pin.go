package managed

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	_ "embed"

	"sigs.k8s.io/yaml"
)

// Pin is a copy of charts/pds/compatibility.yaml, which a test holds equal,
// because the adapter cannot embed a file outside its module. The versions an
// environment may be upgraded to come from it.
//
//go:embed compatibility.yaml
var Pin string

// UpgradePaths maps a chart version an environment runs to the chart versions it
// may move to: support.managedUpgrade.from in the pin.
type UpgradePaths map[string][]string

// release is a chart version, optionally with a pre-release such as -rc.1.
var release = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// PinnedUpgrades reads the upgrade paths of the pin this build carries.
func PinnedUpgrades() (UpgradePaths, error) { return ParseUpgrades([]byte(Pin)) }

// ParseUpgrades reads support.managedUpgrade.from from a pin. A pin with no such
// block has no upgrade paths.
func ParseUpgrades(raw []byte) (UpgradePaths, error) {
	var pin struct {
		Support struct {
			ManagedUpgrade struct {
				From map[string][]string `json:"from"`
			} `json:"managedUpgrade"`
		} `json:"support"`
	}
	if err := yaml.Unmarshal(raw, &pin); err != nil {
		return nil, fmt.Errorf("read the compatibility pin: %w", err)
	}
	paths := UpgradePaths(pin.Support.ManagedUpgrade.From)
	for from, targets := range paths {
		for _, v := range append([]string{from}, targets...) {
			if !release.MatchString(v) {
				return nil, fmt.Errorf("the pin's managed upgrade names %q, which is not a chart release", v)
			}
		}
	}

	return paths, nil
}

// Next returns the newest version the pin lets an environment on current move
// to, leaving out any version at or below the one that was rolled back. It
// returns false when there is none.
func (p UpgradePaths) Next(current, skipped string) (string, bool) {
	best := ""
	for _, target := range p[current] {
		if skipped != "" && compareVersions(target, skipped) <= 0 {
			continue
		}
		if best == "" || compareVersions(target, best) > 0 {
			best = target
		}
	}

	return best, best != ""
}

// compareVersions orders two chart releases, a pre-release before its release.
// A version that does not parse sorts before every release.
func compareVersions(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := range 3 {
		if pa.numbers[i] != pb.numbers[i] {
			return pa.numbers[i] - pb.numbers[i]
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	}

	return strings.Compare(pa.pre, pb.pre)
}

type version struct {
	numbers [3]int
	pre     string
}

func parseVersion(v string) version {
	m := release.FindStringSubmatch(v)
	if m == nil {
		return version{numbers: [3]int{-1, -1, -1}}
	}
	var out version
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{numbers: [3]int{-1, -1, -1}}
		}
		out.numbers[i] = n
	}
	out.pre = strings.TrimPrefix(m[4], "-")

	return out
}
