package backend

import (
	"os"
	"os/exec"
	"strconv"
)

// Kind is the container backend a node is made with: docker or podman.
//
// Called a backend rather than an engine because in this project "engine" is
// CUBRID -- Topology.Engine is the build under test -- and ADR-002 already
// calls this layer the backend.
//
// # Why this is a field and not an interface
//
// ADR-002 named eleven operations a backend must provide and said the Go
// interface gets declared when a second implementation exists. A second one
// exists now, and it turns out not to want an interface — because it is not a
// second implementation. Measured against podman 4.9.3, every flag the docker
// backend passes is accepted verbatim: --init, --cap-add, --shm-size, --ulimit,
// --label, --device, --cpus, -v, --network, --hostname. `podman network create`
// is `docker network create`, `podman exec` is `docker exec`, and the labels and
// the gateway are the same idea with the same syntax.
//
// Four things differ, and each is a flag or a template rather than a code path.
// Two implementations of eleven operations, 95% identical, would put the
// differences in the two places a reader has to diff. A parameter puts them in
// one place with the measurement beside them.
//
// # The four
//
//  1. **Rootless podman needs --userns=keep-id where docker needs --user.**
//     This is not cosmetic: it decides operation 11. Measured on this machine,
//     with a bind mount and a file written inside the node:
//
//     --user 1000:1000   ->  touch: /work/a: Permission denied
//     --userns=keep-id   ->  host sees uid=1000 gid=1000, and can edit it
//
//     `--user` under rootless podman names a uid *inside* the user namespace,
//     which maps to a subuid on the host that this user does not own. keep-id
//     maps the invoking user to the same uid inside, which is what NodePlan's
//     own comment asks for: "files stay editable on the host".
//
//  2. **A rootless container has no ICMP unless NET_RAW is added.** Measured:
//     `ping 127.0.0.1` fails inside a plain rootless container and succeeds with
//     --cap-add=NET_RAW. That matters because the engine's own split-brain
//     discrimination is a ping (`ha_ping_hosts`, and csb's --ping-mode defaults
//     to icmp), so without the capability the default flavour cannot work and
//     would look like a finding about CUBRID.
//
//  3. **`network inspect` reports its gateway under a different key.** docker
//     puts it at `.IPAM.Config[0].Gateway`; podman puts it at `.Subnets[].Gateway`.
//     Found by running it rather than by reading: the cluster came up serving and
//     then said `no_ping_host`, which is operation 3 failing quietly — and a
//     cluster with no witness cannot tell "the peer is gone" from "I am gone",
//     so the two split-brain flavours stop being different scenarios.
//
//  4. **`ps --format` reaches a container's labels differently.** docker's
//     formatter takes a method, `{{.Label "csb.role"}}`, and exposes .Labels as
//     the joined string `k=v,k=v`. podman's psReporter has no .Label at all and
//     exposes .Labels as a map, so the key is reached with
//     `{{index .Labels "csb.role"}}`. Measured, on the same two containers:
//
//     docker  {{.Label "csb.role"}}          ->  master
//     docker  {{index .Labels "csb.role"}}   ->  cannot index slice/array with type string
//     podman  {{.Label "csb.role"}}          ->  can't evaluate field Label in type containers.psReporter
//     podman  {{index .Labels "csb.role"}}   ->  master
//
//     This one was added after the list said there were three. It decides
//     whether a node is seen at all: Nodes() and RunningClusters() both read a
//     label, and podman answers the docker spelling with an error on stdout
//     rather than a non-zero exit -- so a running pair reported LIVE=no and
//     `ls` reported it had no containers, which reads as a cluster that did not
//     come up rather than a template that cannot be parsed.
//
//  5. **Nothing else.** Recorded as a finding rather than an assumption: the
//     network itself and name resolution both work. aardvark-dns resolves peer
//     names, which the assembly depends on because ha_node_list is written with
//     names and not addresses.
type Kind string

const (
	// KindDocker is the backend csb was built against.
	KindDocker Kind = "docker"
	// KindPodman is the second, rootless by default.
	KindPodman Kind = "podman"
)

// BackendEnv names the backend explicitly, for a machine that has both.
const BackendEnv = "CSB_BACKEND"

// Kinds is every backend this tool knows how to drive.
var Kinds = []Kind{KindDocker, KindPodman}

// Cmd is the command to run.
func (e Kind) Cmd() string {
	if e == "" {
		return string(KindDocker)
	}
	return string(e)
}

// Valid reports whether this is a backend csb knows.
func (e Kind) Valid() bool {
	for _, k := range Kinds {
		if e == k {
			return true
		}
	}
	return false
}

// Rootless reports whether containers run in a user namespace of the invoking
// user's own, which is podman's default and never docker's.
//
// Asked of the backend rather than probed, because the two differences it
// decides -- the uid mapping and the missing ICMP -- are properties of that
// mode rather than of a particular host, and a probe that ran at create time
// would make a cluster's argv depend on when it was stood up.
func (e Kind) Rootless() bool { return e == KindPodman && os.Geteuid() != 0 }

// IdentityArgs is how a node ends up owning its files as the invoking user.
//
// The whole of difference 1, in one place. Everything else about NodePlan is
// the same argv for both backends.
func (e Kind) IdentityArgs(uid, gid int) []string {
	if e.Rootless() {
		// keep-id maps this user to the same uid inside, so a file the node
		// writes into its bind-mounted state directory is a file this user can
		// read and edit -- ADR-002 operation 11, which seeding, the slave
		// rebuild and `node logs` all depend on.
		return []string{"--userns=keep-id"}
	}
	return []string{"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid)}
}

// CapabilityArgs is the capabilities a node needs.
//
// NET_ADMIN is both backends': the fault verbs are route and qdisc operations.
// NET_RAW is difference 2 -- without it a rootless container cannot open an
// ICMP socket, and the engine's own ping-based split-brain discrimination is
// exactly that.
func (e Kind) CapabilityArgs() []string {
	args := []string{"--cap-add=NET_ADMIN"}
	if e.Rootless() {
		args = append(args, "--cap-add=NET_RAW")
	}
	return args
}

// GatewayTemplate is the Go template that pulls a network's gateway out of
// `network inspect`. Difference 3: the two engines report it under different
// keys, and a template that is wrong returns empty rather than failing -- which
// is how this cost a cluster its witness before it was noticed.
func (e Kind) GatewayTemplate() string {
	if e == KindPodman {
		return "{{range .Subnets}}{{.Gateway}}{{end}}"
	}
	return "{{(index .IPAM.Config 0).Gateway}}"
}

// LabelTemplate is the Go template that pulls one label off a container in
// `ps --format`. Difference 4: docker takes a method and podman takes a map,
// and the wrong one does not fail the command -- it writes an error where the
// value should be, which every caller then reads as an empty label.
func (e Kind) LabelTemplate(key string) string {
	if e == KindPodman {
		return "{{index .Labels \"" + key + "\"}}"
	}
	return "{{.Label \"" + key + "\"}}"
}

// Available reports whether this backend can be found on PATH.
func (e Kind) Available() bool {
	_, err := exec.LookPath(e.Cmd())
	return err == nil
}

// Detect picks the backend to use: CSB_BACKEND when it names one, otherwise
// whichever is installed, preferring docker because that is what every existing
// cluster on this machine was made with.
//
// A cluster records its backend in `describe`, so this only decides a new one --
// an existing cluster is reached with the backend that made it, and a machine
// that has since lost it says so rather than quietly using the other.
func Detect() Kind {
	if e := Kind(os.Getenv(BackendEnv)); e.Valid() {
		return e
	}
	if KindDocker.Available() {
		return KindDocker
	}
	if KindPodman.Available() {
		return KindPodman
	}
	return KindDocker
}
