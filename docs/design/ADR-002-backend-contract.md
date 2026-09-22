---
title: ADR-002 — What a backend has to provide
category: design
project: cluster-sandbox
summary: The eleven operations a backend must offer, derived from what the docker one already does rather than invented for backends that do not exist yet. Evaluated against a tailnet and against Kubernetes/cubrid-operator. A tailnet changes four of the eleven and leaves the fault verbs intact, which is why it is a network for a backend rather than a backend. Kubernetes collides with two founding constraints and with the operator's own purpose, which is the strongest argument for OQ4's second reading. Since measured against podman: the eleven held, the interface was not declared, and a backend is a parameter carrying four differences.
created: 2026-09-03
updated: 2026-09-21
status: accepted
lang: en
---

# ADR-002 — What a backend has to provide

## Status

**Accepted 2026-09-03.** The contract is named and the docker backend is moved
onto it. No Go interface is declared yet; see *Why no interface yet*.

**Amended 2026-09-21.** A second backend exists — podman — and it did not want
the interface that section predicted. See *Measured against podman 4.9.3*.

## Context

Two questions arrived together — whether a topology should be able to span
machines with a tailnet ([`../DESIGN.md`](../DESIGN.md) §9 OQ11), and whether
`cubrid-operator` is a second backend or a component under test (§9 OQ4) — and
both were unanswerable for the same reason: **nothing said what a backend is.**

The argument against a second backend had been that the fault verbs are defined
against the docker network's cut, so they would have to be re-invented rather
than ported. That argument was wrong, and it was wrong in a way the code made
easy to believe: `internal/fault` reached around `internal/backend` and shelled
out to `docker` itself — an address lookup, the cut, and three privileged execs.
Backend knowledge sat in two packages, and would have sat in four the moment a
second backend existed.

**Expressed by what it means rather than by how it is done, the cut is portable.**
"Make this peer unreachable from this node, by this mechanism" is a sentence about
the protocol. Whether it is a blackhole route, a packet filter, a NetworkPolicy or
a tailnet ACL is the backend's business.

## Decision

A backend provides these eleven operations. They are derived from what the docker
backend already does, not designed for a hypothetical one.

| # | Operation | What it means | Why the fault verbs need it |
|---|---|---|---|
| 1 | `BaseImage` | a runtime for a node, built from a recipe the tool carries | the recipe is hashed, so an unchanged recipe is never rebuilt |
| 2 | `EnsureNetwork` | a private network only these nodes share | `ha_node_list` is written against it |
| 3 | `NetworkGateway` | **an address outside the pair that survives a cut between them** | it is the ping host, and it is what makes `ping-survives` and `no-ping-hosts` different scenarios at all |
| 4 | `Addr` | how a peer is named on that network | an unreachability is expressed against it |
| 5 | `CreateNode` | a node with the engine tree read-only, a writable state directory, a reaping PID 1, raised shared memory, packet-level privileges, a fixed `TZ`, and labels carrying cluster and role | every one of those is a trap this project paid for (`03-assembly.md` §4) |
| 6 | `Exec` | a shell inside a node with the engine's environment | every inspection and every assembly step |
| 7 | `Privileged` | uid 0 inside a node | routes, packet filters, `tc`, and the mode of a file the image installed |
| 8 | `Unreach` / `Reach` | one direction unreachable, and back — **with the mechanism named** | `drop` keeps the route and discards packets so `connect()` hangs; the default removes the route so it fails at once. Different engine code paths, so the mechanism is part of the operation |
| 9 | `Nodes` | what is actually running | cluster state comes from the world, never from a lock file |
| 10 | `Destroy` | removal, reporting what was removed | |
| 11 | **host-side access to each node's database directory** | seeding, the slave rebuild, and `node logs` read and write those files from outside the node | this is the one that does not survive contact with Kubernetes |

### Why no interface yet

An interface with one implementation is indirection with no reader. The contract
is named here and the operations are in one package; the Go `interface` gets
declared when a second implementation exists, which is the moment it costs
nothing to be wrong about its shape. What has already been paid for is the part
that mattered: `internal/fault` no longer knows what docker is.

## A tailnet is a network for a backend, not a backend

It changes **four** of the eleven — 2, 3, 4 and the addresses 8 operates on — and
leaves the rest untouched.

**The cut survives, and this is the point that reverses the earlier objection.** A
tailnet address is reached through an interface like any other, so
`ip route add blackhole 100.x.y.z` and `iptables -A OUTPUT -d 100.x.y.z -j DROP`
both still do exactly what they do today, with the same distinction between them.
`Unreach(from, addr, mechanism)` is implemented by the same code; only `Addr`
returns something different.

What genuinely has to be decided is **operation 3**. The ping host is currently
the docker network's gateway, chosen because it sits outside the pair and
survives a cut between the nodes. On a tailnet there is no gateway; the ping host
becomes another tailnet member, and whether that member is reachable during the
partition is now a property of the tailnet rather than of a route table. The
split-brain flavours would have to be **re-measured** on that network — not
re-designed.

The other two costs stand and are smaller than they looked. An auth key is a
credential, so it must not reach `describe`; that is a rule, not an obstacle. And
a control plane in the path of `cluster create` is real, which is why this is an
option rather than the default: the offline path has to keep working, and the
run record's freedom from external references is asserted by the suite.

**Verdict: worth taking as a backend option.** The work is operations 2–4 plus a
decision about the ping host, and the fault verbs come along unchanged.

## Kubernetes and `cubrid-operator` — the contract says where it hurts

Three collisions, and they are not about effort.

**Operation 11 does not survive.** Seeding, the slave rebuild and `node logs` all
work on the host's copy of a node's database directory. In a pod there is no such
copy. This is repairable and arguably an improvement — the field's own
`ha_make_slavedb.sh` moves those files with `scp` between machines, so doing the
work inside the nodes is closer to what operators do than what this tool does.

**Operation 5 collides with a founding constraint.** *There is a base image and
there is never an engine image* (`03-assembly.md` §4), because the tool has to be
usable while you are changing the engine. A pod gets its engine from an image, a
`hostPath` that is not portable, or an initContainer that copies one in. The
operator ships images. Something gives, and it should be decided rather than
discovered.

**Operation 8 loses its mechanism.** `NetworkPolicy` is declarative and
namespace-scoped; it expresses "these pods cannot talk" and does not express
"keep the route, drop the packets". That distinction is not decoration — it is
the difference between two engine code paths, and the split-brain finding rests
on it. Keeping it means privileged pods with `NET_ADMIN`, which is exactly what
a cluster policy tends to forbid.

### And the deepest one is not in the table

**An operator's job is to repair what this tool deliberately breaks.** `node kill`
is a scenario here; to a reconciliation loop it is a fault to be corrected, and
the pod comes back. That is not an obstacle to be worked around — it is the
operator behaving correctly, and it means the two projects do not naturally meet
as *tool and backend*.

They meet as **tool and subject**. How fast the operator notices, what it does
about a split brain, whether its `CubridDB` status reports a divergence that
`repl diff` can see and its gauges cannot — those are measurements, and this tool
already makes that kind. §9 OQ4 offered two readings; the contract says the
second one is the one with something in it.

## The tailnet option, as built

`cluster create --network tailnet --ts-authkey <key>` (or `CSB_TS_AUTHKEY`).

- **The recipe is a second recipe, not a flag on the first.** The image tag is
  the hash of the recipe, so a tailnet image and a bridge image are different
  images that never collide, and a cluster that does not want a tailnet neither
  builds nor pulls one.
- **The nodes stay on the bridge and also join the tailnet.** What changes is
  what their names MEAN: every node's `/etc/hosts` points every peer name at its
  tailnet address. `ha_node_list` is still written with names and the assembly is
  untouched. Without this the names would still resolve — to bridge addresses —
  and the cluster would quietly keep talking over the bridge while believing it
  was on the tailnet, and a cut expressed against a tailnet address would cut
  nothing.
- **The witness is this host by default.** It is already on the tailnet by the
  time it is provisioning nodes, it sits outside the pair, and a cut between two
  nodes does not touch it. `--ping-host` names another; for a cluster spanning
  machines it should be a third machine rather than either of the two.
- **The auth key is never stored.** It is a flag or an environment variable, used
  at create and not written to `describe`, which is an artifact people paste into
  issues. **Use an ephemeral key.** `tailscale logout` expires a node's key and
  does *not* delete the device: a non-ephemeral node stays in the tailnet's
  device list until somebody removes it from the admin console or the API, and
  this tool has neither. `cluster destroy` logs each node out and then says which
  ones will remain, because the moment to tell somebody is now rather than when
  they next open the console and find machines they do not recognise.

## Measured on a real tailnet, 2026-09-03

A two-node HA pair reached `serving` on a tailnet with this host as the witness.
The replication connections were on tailnet addresses in both directions —
`100.107.179.126:39994 → 100.68.97.118:31523` and back, on the server's own port
— which is the check that matters, because names that resolve to the bridge would
look identical from outside and would make every cut a no-op.

**And the first cut WAS a no-op, which is why it was run.** `partition` adds a
blackhole route, and on a tailnet that lands in the `main` table:

```
5270:  from all lookup 52        ← tailscale's rule, consulted first
32766: from all lookup main      ← where the blackhole went
```

`ip route get` still answered `dev tailscale0 table 52`, the peer stayed
reachable, and the fault verb did nothing at all — the worst outcome available to
a fault injector, and the same class of failure as the missing `iptables` that
the end-to-end suite caught. **A mechanism nobody has run is a mechanism nobody
has**, and this is the second time that sentence has earned its place.

The fix is table-agnostic and says the same thing: a policy rule at priority
1000, below tailscale's 5210–5270, so there is no route at all and `connect()`
fails at once rather than hanging. The packet-level mechanism needs no change,
because netfilter does not care which table would have carried the packet.

With that, on the tailnet:

| | |
|---|---|
| peer after the cut | unreachable |
| witness after the cut | still reachable — which is the whole point of choosing one outside the pair |
| two masters | within ~5 s |
| the engine's own words | `[Failback] [Cancelled] Ping check succeeded for the hosts registered in ha_ping_hosts, determining that it is not a network partition.` |
| after `fault clear` | one master and one standby again, ~10 s, original roles |

So the `ping-survives` flavour reproduces on a tailnet with a tailnet witness.
**The mechanism changed and the meaning did not**, which is what the contract was
written to make possible. The sampling here is at five-second granularity and is
not a comparison against the bridge's 9 s: that would need the switchover
harness, and it is the next thing worth running rather than a number to quote.

## Measured against podman 4.9.3, 2026-09-21

The second implementation arrived, which is the moment §"Why no interface yet"
said the `interface` would cost nothing to be wrong about. It was not declared,
because the second backend is not a second implementation.

Every flag the docker backend passes, podman accepts verbatim — `--init`,
`--cap-add`, `--shm-size`, `--ulimit`, `--label`, `--device`, `--cpus`, `-v`,
`--network`, `--hostname`. `podman network create` is `docker network create`;
`podman exec` is `docker exec`. None of the eleven operations needed a second
code path. Four things differ, and each is a flag or a template:

| | difference | docker | podman (rootless) | operation |
|---|---|---|---|---|
| 1 | owning files as the invoking user | `--user 1000:1000` | `--userns=keep-id` | 11 |
| 2 | opening an ICMP socket | already permitted | `--cap-add=NET_RAW` | 3 |
| 3 | a network's gateway | `.IPAM.Config[0].Gateway` | `.Subnets[].Gateway` | 3 |
| 4 | a container's label in `ps` | `{{.Label "k"}}` | `{{index .Labels "k"}}` | 9 |

So the prediction the list was written to test — that a second backend is eleven
things rather than a rewrite — held. What it got wrong is the shape of the
answer: a parameter, not an implementation. Two 95%-identical implementations
would have put the four differences in the two files a reader has to diff.

### The part the contract did not say

**Three of the four fail silently, and that is the finding.** A wrong template
does not fail the command — it returns empty, or writes a template error where
the value should be, and exits 0:

- difference 3 gave a cluster no ping host, so `no_ping_host` appeared after it
  came up serving, and the two split-brain flavours stopped being different
  scenarios;
- difference 4 made a running pair report `LIVE no` and made `cluster ls` say it
  had no containers, which reads as a cluster that did not come up;
- difference 1 surfaces as `touch: /work/a: Permission denied` inside a node,
  three operations away from the flag that caused it.

An operation that cannot fail loudly needs a test that asserts its spelling
rather than a comment recording it. The four are pinned in
`internal/backend/engine_test.go`.

### Every fault verb, executed on a rootless pair

A backend whose fault verbs are unexecuted is a backend that has not been tested
for what this tool is for, and this project has shipped exactly that before:
`partition --mechanism drop` and `ping-unavailable --mechanism icmp` were merged,
documented and never run, because iptables was missing from the base image. So
all of them were run against a rootless pmha pair, each injected, observed at the
mechanism, and cleared:

| verb | mechanism | observed |
|---|---|---|
| `ping-unavailable` | `iptables -A OUTPUT -p icmp -j DROP` | 100% loss with the rule, 0.025 ms after `clear` |
| `ping-unavailable` | `chmod 000` | 755 → 0, `ping: Permission denied`, → 755 |
| `partition` | `iptables -A OUTPUT -d <peer> -j DROP` | peer unreachable, **witness still reachable** |
| `partition` | `ip route add blackhole <peer>` | route present, then absent |
| `lag --stage apply` | `kill -STOP` | applylogdb `Sl` → `Tl` → `Sl`; copylogdb untouched |
| `lag` | `tc qdisc netem delay` | 300 ms asked, 300.017 ms measured, 0.010 ms after `clear` |
| `contend --kind cpu` | busy loops | 2 workers at 100%, gone on `clear` |
| `contend --kind io` | `dd conv=fsync` | workers running, `/db/.csb_contend_*` removed on `clear` |
| `failcount` | SQL | `fail_counter` 0 → 5, and correctly refused as `not_clearable` |
| `splitbrain` | composite | 2 masters, flavour chosen as `ping-survives` |

`clear` reversed every one of them and left no residue: no iptables rules, no
blackhole routes, no netem qdisc on either node afterwards.

**The engine's own sentence is the same one.** Under a rootless podman bridge the
split brain produces, verbatim, what the tailnet run recorded above:

```
[Failback] [Diagnosis] The master node has failed to receive heartbeat messages
from all other slave nodes, resulting in a network partition.
[Failback] [Cancelled] Ping check succeeded for the hosts registered in
ha_ping_hosts, determining that it is not a network partition.
```

Which is the claim the contract was written to make testable: **the mechanism
changed and the meaning did not.**

### What is still not measured

Recovery after `clear` was timed once — 8 s from `fault clear` to one master and
one standby, via `fault splitbrain`, which is the verb that waits for the
two-master state rather than assuming it. That is one sample and not a
comparison against the tailnet's ~10 s; two further attempts were discarded
because the pair had not finished promoting when the clear went in, which is
variance in the engine and not in the backend.

Unexercised under podman: `partition --keep` and `--from`, `ha promote`,
`ha failback`, `scenario run`, and the e2e suite, which runs on whatever the
runner has.

### Two more, which are about the host and not the CLI

- **A cluster must be reached with the backend that made it.** On a machine with
  both installed, detection alone looks for a podman cluster with docker and
  reports it gone. The backend is therefore recorded in `describe` — the
  decision, not the flag, because `$CSB_BACKEND` and detection are how it is
  normally chosen and both leave the flag empty.
- **Operation 10 cannot delete a bind-mount source.** Rootless podman keeps a
  mount namespace alive between commands, so a directory that is removed and
  recreated is bound by its old inode in the next cluster: `/work` is empty
  inside the node while the host directory has the tree in it, and the first
  thing that fails is `createdb exited 127: cubrid: command not found`. docker
  survives the same destroy-and-create because its daemon resolves the path per
  container. `Destroy` empties the workdir and keeps it.

## Consequences

1. `internal/fault` no longer contains the word `docker`. The cut, the privileged
   exec and the address lookup are the backend's, named for what they mean.
2. OQ11 has an answer: **a tailnet is worth a spike as a backend option**, and the
   spike's scope is operations 2–4 and the ping host.
3. OQ4 has a recommendation rather than a deferral: **the operator is a subject,
   not a backend**, and the argument is its own reconciliation loop.
4. A second backend is now a list of eleven things rather than a rewrite, and the
   list is short enough to disagree with.
5. The list was tested and held; the `interface` was not declared. A backend is a
   parameter — `backend.Kind` — carrying four measured differences, and a
   cluster records which one made it.
