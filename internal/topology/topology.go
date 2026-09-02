// Package topology turns "a preset, a count and some overrides" into the thing
// the rest of the tool builds against, and into the describe artifact that
// reproduces it elsewhere (docs/design/02-topology.md).
package topology

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/engine"
)

const Schema = "csb/v1"

type Node struct {
	Name string `json:"name"`
	Role string `json:"role"` // the role at create time, not now
}

type Resources struct {
	CPUs    float64 `json:"cpus,omitempty"`
	ShmSize string  `json:"shm_size,omitempty"`
}

type Params struct {
	Common map[string]string `json:"common,omitempty"` // cubrid.conf
	HA     map[string]string `json:"ha,omitempty"`     // cubrid_ha.conf
	Hidden map[string]string `json:"hidden,omitempty"` // written unvalidated, on request
}

// Topology is also the describe artifact: the same value the tool builds from is
// the one it hands to the next person, so they cannot drift.
type Topology struct {
	Schema     string           `json:"schema"`
	Cluster    string           `json:"cluster"`
	Preset     string           `json:"preset"`
	DB         string           `json:"db"`
	Network    string           `json:"network"`
	Image      string           `json:"image"`
	PingMode   string           `json:"ping_mode"`
	WithBroker bool             `json:"with_broker"`
	Nodes      []Node           `json:"nodes"`
	Engine     *engine.Identity `json:"engine,omitempty"`
	Resources  Resources        `json:"resources,omitempty"`
	Parameters Params           `json:"parameters,omitempty"`
}

type Options struct {
	Name       string
	Preset     string
	Nodes      int
	DB         string
	Image      string
	PingMode   string
	WithBroker bool
	CPUs       float64
	ShmSize    string
	Set        []string // key=value, validated
	SetHidden  []string // key=value, written unvalidated
	Engine     *engine.Identity
}

// haKeys is cubrid_ha.conf's surface. The list is the one the field's own
// documentation review settled, including the two ping parameters it had to add.
var haKeys = map[string]bool{
	"ha_mode": true, "ha_node_list": true, "ha_replica_list": true, "ha_db_list": true,
	"ha_port_id": true, "ha_ping_hosts": true, "ha_tcp_ping_hosts": true,
	"ha_copy_sync_mode": true, "ha_copy_log_base": true, "ha_copy_log_max_archives": true,
	"ha_apply_max_mem_size": true, "ha_applylogdb_ignore_error_list": true,
	"ha_applylogdb_retry_error_list": true, "ha_replica_delay": true,
	"ha_replica_time_bound": true, "ha_delay_limit": true, "ha_delay_limit_delta": true,
	"ha_copy_log_timeout": true, "ha_check_disk_failure_interval": true,
	"ha_unacceptable_proc_restart_timediff": true, "ha_enable_sql_logging": true,
	"ha_sql_log_max_size_in_mbytes": true, "ha_sql_log_max_count": true, "ha_sql_log_path": true,
}

// commonKeys is what the engine ships in its own cubrid.conf. It is not the full
// parameter set -- the engine has hundreds and advertises them only to a running
// server -- so it is a floor, and --set-hidden is the documented way past it.
var commonKeys = map[string]bool{
	"service": true, "server": true, "data_buffer_size": true, "log_buffer_size": true,
	"sort_buffer_size": true, "max_clients": true, "cubrid_port_id": true,
	"db_volume_size": true, "log_volume_size": true, "log_max_archives": true,
	"ha_mode": true, "force_remove_log_archives": true,
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

const (
	PingICMP = "icmp"
	PingTCP  = "tcp"
	PingNone = "none"
)

// Resolve validates the options and derives everything a name can derive.
func Resolve(o Options) (*Topology, error) {
	name := o.Name
	if name == "" {
		name = "hadb"
	}
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("cluster name %q must be lowercase letters, digits and dashes, starting with a letter", name)
	}

	preset := o.Preset
	if preset == "" {
		preset = "ha"
	}
	count := o.Nodes
	switch preset {
	case "ha":
		if count == 0 {
			count = 2
		}
		if count < 2 {
			return nil, fmt.Errorf("preset ha needs at least 2 nodes, got %d", count)
		}
	case "single":
		if count == 0 {
			count = 1
		}
		if count != 1 {
			return nil, fmt.Errorf("preset single is one node, got %d", count)
		}
	default:
		return nil, fmt.Errorf("unknown preset %q (want ha or single)", preset)
	}

	ping := o.PingMode
	if ping == "" {
		ping = PingICMP
	}
	if ping != PingICMP && ping != PingTCP && ping != PingNone {
		return nil, fmt.Errorf("unknown --ping-mode %q (want icmp, tcp or none)", ping)
	}
	if preset == "single" && ping != PingNone {
		ping = PingNone // a lone node has no partition to diagnose
	}

	t := &Topology{
		Schema: Schema, Cluster: name, Preset: preset,
		DB: firstNonEmpty(o.DB, name), Network: name + "-net",
		Image: o.Image, PingMode: ping, WithBroker: o.WithBroker,
		Engine:    o.Engine,
		Resources: Resources{CPUs: o.CPUs, ShmSize: firstNonEmpty(o.ShmSize, "1g")},
		Parameters: Params{
			Common: map[string]string{}, HA: map[string]string{}, Hidden: map[string]string{},
		},
	}
	for i := 1; i <= count; i++ {
		role := "slave"
		if i == 1 {
			role = "master"
		}
		if preset == "single" {
			role = "standalone"
		}
		t.Nodes = append(t.Nodes, Node{Name: fmt.Sprintf("%s-n%d", name, i), Role: role})
	}

	for _, kv := range o.Set {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		switch {
		case haKeys[k]:
			t.HAParam(k, v)
		case commonKeys[k]:
			t.Parameters.Common[k] = v
		default:
			return nil, fmt.Errorf("unknown parameter %q; if the engine has it but does not advertise it, use --set-hidden %s", k, kv)
		}
	}
	for _, kv := range o.SetHidden {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		t.Parameters.Hidden[k] = v
	}
	return t, nil
}

func (t *Topology) HAParam(k, v string) { t.Parameters.HA[k] = v }

// NodeNames, in ha_node_list order.
func (t *Topology) NodeNames() []string {
	out := make([]string, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		out = append(out, n.Name)
	}
	return out
}

// HANodeList is the same on every node -- that is how each learns who its peer
// is (docs/design/03-assembly.md §5).
func (t *Topology) HANodeList() string {
	return "cubrid@" + strings.Join(t.NodeNames(), ":")
}

// HiddenKeys, sorted, for the note that says a cluster carries unvalidated
// parameters and may be in a state the engine's documentation does not describe.
func (t *Topology) HiddenKeys() []string {
	out := make([]string, 0, len(t.Parameters.Hidden))
	for k := range t.Parameters.Hidden {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func split(kv string) (string, string, error) {
	i := strings.Index(kv, "=")
	if i <= 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", kv)
	}
	return strings.TrimSpace(kv[:i]), strings.TrimSpace(kv[i+1:]), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
