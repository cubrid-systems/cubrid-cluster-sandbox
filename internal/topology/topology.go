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

	// Kind separates a database node from a client node. A client is part of the
	// CLUSTER -- same network, same labels, destroyed with it -- and is not part
	// of the HA GROUP: it never appears in ha_node_list, and putting it there
	// would be a configuration error rather than a bigger cluster.
	//
	// It exists because the load driver was running inside the master, competing
	// with the engine for the CPU quota given to the engine. That was a
	// compromise, not a design, and `driver_cost` was this project measuring its
	// own distortion. A client node also gives a JDBC or CCI client somewhere to
	// run that can reach the broker without publishing a port, and gives CTP's
	// ha_repl the controller its conf has always required beside the pair.
	Kind string `json:"kind,omitempty"` // db (default) | client

	// Host is the machine this node actually runs on, as that machine calls
	// itself. It is per node and not per cluster because the point of recording
	// it at all is the case where a cluster's nodes differ -- masters here,
	// slaves elsewhere -- and a cluster-level field would have to be taken away
	// again to get there.
	//
	// It is an observation, never an instruction. `create --from` re-observes it
	// for the same reason `ping_host` is re-resolved: a machine name is local to
	// whoever issued it, and an artifact rebuilt somewhere else that insisted on
	// the original would be naming a machine that is not there. Rebuilding a
	// two-machine cluster on one machine produces a one-machine cluster, and
	// says so, rather than failing.
	Host string `json:"host,omitempty"`
}

const (
	KindDB     = "db"
	KindClient = "client"
)

func (n Node) IsClient() bool { return n.Kind == KindClient }

type Resources struct {
	CPUs    float64 `json:"cpus,omitempty"`
	ShmSize string  `json:"shm_size,omitempty"`
	// CPUSet pins the database nodes to these CPUs, and ClientCPUSet the
	// clients. --cpus is a quota and says how much; these say where, which is
	// what a measurement on a two-CCD machine needs: the engine on one die, the
	// program driving it on the other, and neither migrating onto the other's
	// cache. Written as the runtime takes them: "0-7,16-23".
	CPUSet       string `json:"cpuset,omitempty"`
	ClientCPUSet string `json:"client_cpuset,omitempty"`
}

type Params struct {
	Common map[string]string `json:"common,omitempty"` // cubrid.conf
	HA     map[string]string `json:"ha,omitempty"`     // cubrid_ha.conf
	Hidden map[string]string `json:"hidden,omitempty"` // written unvalidated, on request
	// Broker is cubrid_broker.conf's [%csb] section, the keys a --broker-set
	// overrides. The file is written by this tool by construction (03-assembly.md
	// §6), so these are overrides of its own text rather than a parameter
	// surface of the engine's; a measurement wants a fixed CAS count and no SQL
	// log, and had no way to say so.
	Broker map[string]string `json:"broker,omitempty"`
	// Unverified names the --set keys this tool could not look up. They were
	// written all the same, to the file their name says, because the engine
	// refuses a name it does not know at server start -- so a typo is loud
	// there, and the only thing lost by writing it is this tool's say-so. The
	// list is kept so `describe` still says which keys that was.
	Unverified []string `json:"unverified,omitempty"`
}

// Topology is also the describe artifact: the same value the tool builds from is
// the one it hands to the next person, so they cannot drift.
type Topology struct {
	Schema      string `json:"schema"`
	Cluster     string `json:"cluster"`
	Preset      string `json:"preset"`
	DB          string `json:"db"`
	Network     string `json:"network"`
	Image       string `json:"image"`
	PingMode    string `json:"ping_mode"`
	PingHost    string `json:"ping_host,omitempty"`
	NetworkKind string `json:"network_kind,omitempty"` // bridge (default) | tailnet
	// Backend is the container backend that made this cluster: docker or
	// podman. Recorded rather than detected each time, because a cluster has to
	// be reached with what made it -- a machine that has both would otherwise
	// find a podman cluster with docker and report it gone.
	// Labels are recorded and reported and never interpreted. They exist because
	// the tools that drive this one have facts about a cluster that this one has
	// no business understanding -- which test run asked for it, above all, so
	// that the run can later destroy what it created and nothing else.
	//
	// Deliberately NOT carried by `create --from`. A label is a claim by whoever
	// ran create, not a property of the topology: a cluster rebuilt by hand from
	// somebody's artifact must not inherit their ownership and then be destroyed
	// out from under its new owner.
	Labels map[string]string `json:"labels,omitempty"`

	Backend string `json:"backend,omitempty"`
	// ClientImage is the image the client nodes run, when it is not the base
	// image. A client runs the user's program, and the base image is built for
	// the engine's needs -- ping, iptables, procps -- not for a JDBC driver
	// that wants a JDK. The image is the user's and must already exist: csb
	// builds the one recipe it wrote and no other.
	ClientImage string `json:"client_image,omitempty"`
	// HAMode is what the assembly writes for ha_mode: "on" for the ha preset,
	// "off" for single. It is derived from the preset and recorded rather than
	// left implicit, because the two presets are two different engines to the
	// tool -- one has a heartbeat, roles and a replication pipeline, the other
	// has a server. An artifact without the field predates it and was HA.
	HAMode     string           `json:"ha_mode,omitempty"`
	Tools      string           `json:"tools,omitempty"` // host directory the clients get read-only
	WithBroker bool             `json:"with_broker"`
	Nodes      []Node           `json:"nodes"`
	Engine     *engine.Identity `json:"engine,omitempty"`
	Resources  Resources        `json:"resources,omitempty"`
	Parameters Params           `json:"parameters,omitempty"`
}

type Options struct {
	Name         string
	Preset       string
	Nodes        int
	DB           string
	Image        string
	PingMode     string
	Network      string // bridge (default) | tailnet
	Backend      string // docker (default) | podman
	Clients      int    // client nodes beside the HA group
	Tools        string // a host directory the clients get read-only
	ClientImage  string // the image the client nodes run; empty is the base image
	WithBroker   bool
	CPUs         float64
	CPUSet       string // CPUs the database nodes are pinned to, "0-7,16-23"
	ClientCPUSet string // CPUs the client nodes are pinned to
	ShmSize      string
	Set          []string // key=value, validated
	SetHidden    []string // key=value, written unvalidated
	BrokerSet    []string // KEY=VALUE for cubrid_broker.conf's [%csb] section
	Labels       []string // key=value, recorded and never interpreted
	Host         string   // the machine standing this up, as it calls itself
	Engine       *engine.Identity
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

// brokerOwned are the cubrid_broker.conf keys the tool decides for itself, with
// the reason an override would fight it. Everything else in the [%csb] section
// -- CAS counts, SQL_LOG, timeouts -- is the user's to set.
var brokerOwned = map[string]string{
	"ACCESS_MODE":        "quiesce and resume write it",
	"BROKER_PORT":        "33000 is what every client and document reaches the broker at",
	"SERVICE":            "the broker exists because --with-broker was given",
	"MASTER_SHM_ID":      "a shared-memory id this tool keeps distinct per cluster",
	"APPL_SERVER_SHM_ID": "a shared-memory id this tool keeps distinct per cluster",
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// cpusetRe is the list form every runtime takes for --cpuset-cpus: numbers
// and ranges, comma-separated, no spaces.
var cpusetRe = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

const (
	// The network a topology's nodes address each other on. `docker` is one
	// host's bridge; `tailnet` makes each node a member of a tailnet, which is
	// the only one of the two that can span machines
	// (docs/design/ADR-002-backend-contract.md).
	// NetBridge is one host's own container network. It was called "docker"
	// until a second backend existed, at which point the name said the tool
	// rather than the thing -- podman's bridge is a bridge too. The old spelling
	// is still accepted, because it is in every describe artifact written so far.
	NetBridge    = "bridge"
	NetBridgeWas = "docker"
	NetTailnet   = "tailnet"

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

	net := o.Network
	if net == "" || net == NetBridgeWas {
		net = NetBridge
	}
	if net != NetBridge && net != NetTailnet {
		return nil, fmt.Errorf("unknown --network %q (want bridge or tailnet)", net)
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
	// single is ha_mode=off, as 02-topology.md has always said. For a long time
	// the assembly wrote ha_mode=on for it regardless and started a heartbeat for
	// a group of one, which made "a server without HA" impossible to stand up:
	// every write carried replication log, and `master` was a heartbeat query
	// against a node that had no group to be registered in.
	haMode := HAModeOn
	if preset == "single" {
		haMode = HAModeOff
	}

	t := &Topology{
		Schema: Schema, Cluster: name, Preset: preset,
		DB: firstNonEmpty(o.DB, name), Network: name + "-net",
		Image: o.Image, PingMode: ping, NetworkKind: net, Backend: o.Backend, HAMode: haMode, WithBroker: o.WithBroker,
		Engine:    o.Engine,
		Resources: Resources{CPUs: o.CPUs, ShmSize: firstNonEmpty(o.ShmSize, "1g"), CPUSet: o.CPUSet, ClientCPUSet: o.ClientCPUSet},
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
		t.Nodes = append(t.Nodes, Node{
			Name: fmt.Sprintf("%s-n%d", name, i), Role: role, Kind: KindDB, Host: o.Host})
	}
	// Clients are named apart from the database nodes so that neither the eye
	// nor a selector can confuse them, and they carry no HA role at all.
	for i := 1; i <= o.Clients; i++ {
		t.Nodes = append(t.Nodes, Node{
			Name: fmt.Sprintf("%s-c%d", name, i), Kind: KindClient, Host: o.Host})
	}
	for name, set := range map[string]string{"--cpuset": o.CPUSet, "--client-cpuset": o.ClientCPUSet} {
		if set != "" && !cpusetRe.MatchString(set) {
			return nil, fmt.Errorf("%s wants a CPU list the runtime takes, like 0-7,16-23; got %q", name, set)
		}
	}
	if o.ClientCPUSet != "" && o.Clients == 0 {
		return nil, fmt.Errorf("--client-cpuset pins the client nodes, and this cluster has none (--clients N)")
	}
	t.Tools = o.Tools
	if o.ClientImage != "" && o.Clients == 0 {
		return nil, fmt.Errorf("--client-image names an image for the client nodes, and this cluster has none (--clients N)")
	}
	t.ClientImage = o.ClientImage

	for _, kv := range o.Set {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		// The tables route; they do not gate. Which file a key belongs to is
		// decided by its name -- ha_* is cubrid_ha.conf's -- and a key neither
		// table knows is written to that file and listed as unverified. The
		// refusal this used to be was built on "the engine accepts a file with a
		// key it ignores", which is wrong for cubrid.conf: prm_find fails on a
		// name it does not know and the server does not start (system_parameter.c,
		// PRM_ERR_UNKNOWN_PARAM). So a typo is loud at the engine, and refusing
		// it here only kept out the hundreds of real parameters the engine has
		// and the shipped conf does not mention.
		isHA := haKeys[k] || strings.HasPrefix(k, "ha_")
		if isHA && t.HAOff() {
			// The engine does not read cubrid_ha.conf when ha_mode is off, so
			// the value would be written and never take effect -- that is the
			// one silence worth refusing.
			return nil, fmt.Errorf("preset single runs with ha_mode=off, so %s has no effect; use the ha preset for HA parameters", k)
		}
		switch {
		case isHA:
			t.HAParam(k, v)
		default:
			t.Parameters.Common[k] = v
		}
		if !haKeys[k] && !commonKeys[k] {
			t.Parameters.Unverified = append(t.Parameters.Unverified, k)
		}
	}
	sort.Strings(t.Parameters.Unverified)
	for _, kv := range o.SetHidden {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		t.Parameters.Hidden[k] = v
	}
	if len(o.BrokerSet) > 0 && !o.WithBroker {
		return nil, fmt.Errorf("--broker-set configures the broker, and this cluster has none (--with-broker)")
	}
	for _, kv := range o.BrokerSet {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		k = strings.ToUpper(k)
		if why, owned := brokerOwned[k]; owned {
			return nil, fmt.Errorf("%s is written by csb itself and cannot be overridden: %s", k, why)
		}
		if t.Parameters.Broker == nil {
			t.Parameters.Broker = map[string]string{}
		}
		t.Parameters.Broker[k] = v
	}
	for _, kv := range o.Labels {
		k, v, err := split(kv)
		if err != nil {
			return nil, err
		}
		if t.Labels == nil {
			t.Labels = map[string]string{}
		}
		t.Labels[k] = v
	}
	return t, nil
}

func (t *Topology) HAParam(k, v string) { t.Parameters.HA[k] = v }

const (
	HAModeOn  = "on"
	HAModeOff = "off"
)

// HAOff reports whether this topology runs without HA: no heartbeat, no roles,
// no replication -- a server. Everything that reads a heartbeat state asks this
// first. An artifact that predates the field was HA, so empty means on.
func (t *Topology) HAOff() bool { return t.HAMode == HAModeOff }

// HAModeValue is what cubrid.conf gets: the recorded mode, or "on" for an
// artifact written before the field existed.
func (t *Topology) HAModeValue() string {
	if t.HAOff() {
		return HAModeOff
	}
	return HAModeOn
}

// NodeNames, in ha_node_list order.
func (t *Topology) NodeNames() []string {
	out := make([]string, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		out = append(out, n.Name)
	}
	return out
}

// DBNodes are the nodes that form the HA group. Everything about the assembly --
// the config, the seeding, the node list, the roles -- is about these and not
// about the clients standing beside them.
func (t *Topology) DBNodes() []Node {
	out := make([]Node, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		if !n.IsClient() {
			out = append(out, n)
		}
	}
	return out
}

func (t *Topology) Clients() []Node {
	var out []Node
	for _, n := range t.Nodes {
		if n.IsClient() {
			out = append(out, n)
		}
	}
	return out
}

// HANodeList is the same on every node -- that is how each learns who its peer
// is (docs/design/03-assembly.md §5).
func (t *Topology) HANodeList() string {
	var names []string
	for _, n := range t.DBNodes() {
		names = append(names, n.Name)
	}
	return "cubrid@" + strings.Join(names, ":")
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
