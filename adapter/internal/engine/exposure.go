package engine

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Exposure is whether an instance can be reached from outside its cluster.
//
// An external instance sits behind a cloud load balancer. The instance exists
// before the load balancer has an address, so Address is empty for a while
// after create, and the connection published in that window is the in-cluster
// one.
type Exposure struct {
	External bool
	// Private is an internal load balancer, whose address is only reachable
	// from inside the customer's VPC. False on an external instance is an
	// internet-facing one.
	Private bool
	// SourceRanges are the CIDR ranges allowed to connect. A public instance
	// always has at least one; open to the internet is 0.0.0.0/0.
	SourceRanges []string
	// Address is the host a client outside the cluster connects to. Empty
	// until the load balancer has been given one.
	Address string
}

// Reachable reports an external database whose load balancer has an address.
// That is the connection string a client outside the cluster can copy. Until
// the address exists the published host is still inside the cluster, and the
// password stays there with it.
func (e Exposure) Reachable() bool {
	return e.External && strings.TrimSpace(e.Address) != ""
}

// Visibility names where an instance accepts connections from, the way the
// API reports it.
type Visibility string

const (
	// VisibilityCluster is an instance with no load balancer, reachable from
	// inside its cluster only. Databases created before every create got a
	// load balancer keep it.
	VisibilityCluster Visibility = "cluster"
	// VisibilityPrivate is an internal load balancer on a VPC address.
	VisibilityPrivate Visibility = "private"
	// VisibilityPublic is an internet-facing load balancer.
	VisibilityPublic Visibility = "public"
)

// Visibility reports where the instance accepts connections from.
func (e Exposure) Visibility() Visibility {
	switch {
	case !e.External:
		return VisibilityCluster
	case e.Private:
		return VisibilityPrivate
	default:
		return VisibilityPublic
	}
}

// Access is the load balancer a create asks for.
type Access string

const (
	// AccessPrivate is an internal load balancer on a VPC address. It is what
	// a create that names no access gets.
	AccessPrivate Access = "private"
	// AccessPublic is an internet-facing load balancer. Its allow list is
	// required; open to the internet is 0.0.0.0/0.
	AccessPublic Access = "public"
)

// PubliclyReachable reports a public database whose load balancer has an
// address. Its connection string is the one a client anywhere may be handed,
// so it is the only one that leaves the cluster. A private address is only
// reachable from the customer's VPC, and every database has one, so its
// password stays on the cluster with the in-cluster ones.
func (e Exposure) PubliclyReachable() bool {
	return e.Reachable() && !e.Private
}

// PrivateSourceRanges are the RFC 1918 ranges a private load balancer admits.
// An internal load balancer only has a VPC address anyway; the ranges are what
// keeps a private database private when the cloud ignores the internal
// annotation and builds an internet-facing one.
var PrivateSourceRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// MaxSourceRanges caps the allow list. Cloud load balancers turn every range
// into firewall rules with their own per-resource limits, and a list longer
// than this is a network policy that belongs somewhere other than a create
// form.
const MaxSourceRanges = 32

var (
	// ErrInvalidSourceRange is an allow list entry that is not an address or
	// a CIDR range, or an allow list on a database that is not public.
	ErrInvalidSourceRange = errors.New("invalid source range")
	// ErrInvalidAccess is an access value that is neither private nor public.
	ErrInvalidAccess = errors.New("invalid access")
)

// AccessError is a create whose access is neither private nor public.
type AccessError struct {
	Value string
}

// Error implements error. This text is for logs.
func (e *AccessError) Error() string {
	return fmt.Sprintf("%s: %q", ErrInvalidAccess, e.Value)
}

// Unwrap lets callers match with errors.Is(err, ErrInvalidAccess).
func (e *AccessError) Unwrap() error { return ErrInvalidAccess }

// UserMessage is the sentence the developer sees.
func (e *AccessError) UserMessage() string {
	return "Access must be private or public."
}

// OpenInternetRange is the allow list entry for a public database open to
// every address. A public database always carries one; an empty list is refused.
const OpenInternetRange = "0.0.0.0/0"

// SourceRangeError is an allow list the create refused. Value is the entry at
// fault. TooMany, Unexposed, and Missing name the refusals that are about the
// list rather than one entry in it. Unexposed is an allow list on a database
// that is not public.
type SourceRangeError struct {
	Value     string
	TooMany   bool
	Unexposed bool
	Missing   bool
}

// Error implements error. This text is for logs.
func (e *SourceRangeError) Error() string {
	switch {
	case e.TooMany:
		return fmt.Sprintf("%s: more than %d ranges", ErrInvalidSourceRange, MaxSourceRanges)
	case e.Unexposed:
		return fmt.Sprintf("%s: ranges given for a database that is not public", ErrInvalidSourceRange)
	case e.Missing:
		return fmt.Sprintf("%s: a public database needs an allow list", ErrInvalidSourceRange)
	default:
		return fmt.Sprintf("%s: %q", ErrInvalidSourceRange, e.Value)
	}
}

// Unwrap lets callers match with errors.Is(err, ErrInvalidSourceRange).
func (e *SourceRangeError) Unwrap() error { return ErrInvalidSourceRange }

// UserMessage is the sentence the developer sees.
func (e *SourceRangeError) UserMessage() string {
	switch {
	case e.TooMany:
		return fmt.Sprintf("Allow at most %d ranges.", MaxSourceRanges)
	case e.Unexposed:
		return "Allowed addresses apply to a public database only."
	case e.Missing:
		return "Enter at least one address or range."
	default:
		return fmt.Sprintf("`%s` is not an IP address or CIDR range.", e.Value)
	}
}

// NormalizeSourceRanges checks an allow list and writes every entry as a CIDR
// range with its host bits cleared. A bare address becomes a single-host range,
// which is what the providers do with one anyway, so the spec states what is
// enforced. Blank entries are dropped and duplicates collapse; nil means the
// list was empty.
func NormalizeSourceRanges(in []string) ([]string, error) {
	var out []string
	seen := map[string]struct{}{}

	for _, raw := range in {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}

		prefix, err := parseRange(value)
		if err != nil {
			return nil, &SourceRangeError{Value: value}
		}

		normalized := prefix.Masked().String()
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if len(out) > MaxSourceRanges {
		return nil, &SourceRangeError{TooMany: true}
	}

	return out, nil
}

// CheckAccess validates the access half of a create and returns the exposure
// the engine should write. Every database gets a load balancer, private unless
// public was asked for.
//
// Ranges on a database that is not public are refused rather than dropped,
// because a caller who sent them believes they apply.
func CheckAccess(in CreateInput) (Exposure, error) {
	ranges, err := NormalizeSourceRanges(in.SourceRanges)
	if err != nil {
		return Exposure{}, err
	}

	access := in.Access
	switch access {
	case "", AccessPrivate, AccessPublic:
	default:
		return Exposure{}, &AccessError{Value: string(access)}
	}

	if access != AccessPublic {
		if len(ranges) > 0 {
			return Exposure{}, &SourceRangeError{Unexposed: true}
		}

		return Exposure{External: true, Private: true}, nil
	}

	if len(ranges) == 0 {
		return Exposure{}, &SourceRangeError{Missing: true}
	}

	return Exposure{External: true, SourceRanges: ranges}, nil
}

func parseRange(value string) (netip.Prefix, error) {
	if strings.Contains(value, "/") {
		return netip.ParsePrefix(value)
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}

	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
