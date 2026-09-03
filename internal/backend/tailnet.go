package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// The tailnet network, which is an option and not a backend.
//
// It changes four of the eleven operations in the backend contract -- the
// network, the address, the ping host, and what an unreachability is expressed
// against -- and leaves the other seven alone
// (docs/design/ADR-002-backend-contract.md). In particular **the cut is
// unchanged**: a tailnet address is reached through an interface like any other,
// so `ip route add blackhole 100.x` and `iptables -A OUTPUT -d 100.x -j DROP`
// still mean exactly what they mean on the bridge, and still mean two different
// things from each other.
//
// What it buys is the one structural limit this tool has: `ha_node_list` works
// today because both containers sit on one host's bridge. On a tailnet the nodes
// are members, so a topology can span machines.

// TailnetUp brings one node onto the tailnet and returns the name it is known
// by there.
//
// The auth key is passed on the command line inside the node and never stored:
// it is a credential, and `describe` is an artifact people paste into issues.
func (d *Docker) TailnetUp(ctx context.Context, node, authKey, hostname string) (string, error) {
	if authKey == "" {
		return "", fmt.Errorf("a tailnet needs an auth key: pass --ts-authkey or set CSB_TS_AUTHKEY")
	}
	// tailscaled needs the tun device and the capability; both are given to the
	// node at create time when the topology asks for a tailnet.
	start := "tailscaled --state=/work/" + node + "/tailscaled.state " +
		"--socket=/var/run/tailscale/tailscaled.sock > /work/" + node + "/tailscaled.log 2>&1 &"
	if res, err := d.Privileged(ctx, node, "mkdir -p /var/run/tailscale && "+start+" sleep 2"); err != nil {
		return "", err
	} else if res.ExitCode != 0 {
		return "", fmt.Errorf("%s: tailscaled did not start: %s", node, tailTrim(res.Stderr+res.Stdout))
	}
	res, err := d.Privileged(ctx, node, fmt.Sprintf(
		"tailscale up --authkey=%s --hostname=%s --accept-dns=false --timeout=60s", authKey, hostname))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s: tailscale up failed: %s", node, tailTrim(res.Stderr+res.Stdout))
	}
	return hostname, nil
}

// TailnetAddr is the node's address on the tailnet. It is the address the peers
// use and the address a cut is expressed against.
func (d *Docker) TailnetAddr(ctx context.Context, node string) (string, error) {
	res, err := d.Privileged(ctx, node, "tailscale ip -4")
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(res.Stdout)
	if res.ExitCode != 0 || ip == "" {
		return "", fmt.Errorf("%s is not on the tailnet: %s", node, tailTrim(res.Stderr+res.Stdout))
	}
	return strings.Fields(ip)[0], nil
}

// Addr resolves by the topology's network kind, so callers ask for "the address
// a peer is reached at" and do not care which network answers.
func (d *Docker) AddrOn(ctx context.Context, t *topology.Topology, node string) (string, error) {
	if t.NetworkKind == topology.NetTailnet {
		return d.TailnetAddr(ctx, node)
	}
	return d.Addr(ctx, t.Network, node)
}

// TailnetPingHost is this machine's own tailnet address, and it is the answer to
// the one question the tailnet option actually has to decide.
//
// The ping host must sit OUTSIDE the pair and survive a cut between the two
// nodes, or neither side can tell "the peer is gone" from "I am gone" -- that is
// what makes `ping-survives` and `no-ping-hosts` different scenarios at all. On
// the bridge that is the docker gateway. On a tailnet there is no gateway, so it
// is a third member; this host is one, it is already on the tailnet by the time
// it is provisioning nodes, and a cut between two nodes does not touch it.
//
// A different member can be named with --ping-host when this host is not the
// right witness -- for a cluster spanning machines it should be a third machine
// rather than either of the two.
func TailnetPingHost(ctx context.Context, r *run.Runner) (string, error) {
	res, err := r.Run(ctx, "tailscale", "ip", "-4")
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(res.Stdout)
	if res.ExitCode != 0 || ip == "" {
		return "", fmt.Errorf("this host is not on a tailnet, so there is no witness for a partition: %s",
			tailTrim(res.Stderr+res.Stdout))
	}
	return strings.Fields(ip)[0], nil
}

func tailTrim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
