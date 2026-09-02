// Package selector parses the role selectors every verb that acts on a node
// takes. A selector is a query resolved at call time, not a label: after a
// failover "master" names the other machine, and a scenario script that ran
// before the failover runs unchanged after it (docs/design/01-cli.md §2).
package selector

import (
	"fmt"
	"regexp"
	"strconv"
)

type Kind int

const (
	Master  Kind = iota // the node that is active now
	Slave               // the single standby; ambiguous if there is more than one
	Replica             // a replica node, always indexed
	Name                // a node by name, when the scenario genuinely means that node
	All                 // every node
)

type Selector struct {
	Kind    Kind
	Index   int    // for slave[n] / replica[n]; -1 when unindexed
	NodeRaw string // for Kind == Name
	Raw     string
}

var indexed = regexp.MustCompile(`^(slave|replica)\[([0-9]+)\]$`)
var nodeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Parse turns one selector token into a Selector, or reports why it cannot.
func Parse(s string) (Selector, error) {
	switch s {
	case "":
		return Selector{}, fmt.Errorf("empty selector")
	case "master":
		return Selector{Kind: Master, Index: -1, Raw: s}, nil
	case "slave":
		return Selector{Kind: Slave, Index: -1, Raw: s}, nil
	case "all":
		return Selector{Kind: All, Index: -1, Raw: s}, nil
	case "replica":
		return Selector{}, fmt.Errorf("replica must be indexed, as replica[0]")
	}
	if m := indexed.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return Selector{}, fmt.Errorf("bad index in %q", s)
		}
		k := Slave
		if m[1] == "replica" {
			k = Replica
		}
		return Selector{Kind: k, Index: n, Raw: s}, nil
	}
	if nodeName.MatchString(s) {
		return Selector{Kind: Name, Index: -1, NodeRaw: s, Raw: s}, nil
	}
	return Selector{}, fmt.Errorf("not a selector: %q (want master, slave, slave[n], replica[n], a node name, or all)", s)
}

func (s Selector) String() string { return s.Raw }
