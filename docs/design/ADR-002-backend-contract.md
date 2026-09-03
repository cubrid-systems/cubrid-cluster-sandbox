---
title: ADR-002 — What a backend has to provide
category: design
project: cluster-sandbox
summary: The eleven operations a backend must offer, derived from what the docker one already does rather than invented for backends that do not exist yet. Evaluated against a tailnet and against Kubernetes/cubrid-operator. A tailnet changes four of the eleven and leaves the fault verbs intact, which is why it is a network for a backend rather than a backend. Kubernetes collides with two founding constraints and with the operator's own purpose, which is the strongest argument for OQ4's second reading.
created: 2026-09-03
updated: 2026-09-03
status: accepted
lang: en
---

# ADR-002 — What a backend has to provide

## Status

**Accepted 2026-09-03.** The contract is named and the docker backend is moved
onto it. No Go interface is declared yet; see *Why no interface yet*.

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
  issues.

## Consequences

1. `internal/fault` no longer contains the word `docker`. The cut, the privileged
   exec and the address lookup are the backend's, named for what they mean.
2. OQ11 has an answer: **a tailnet is worth a spike as a backend option**, and the
   spike's scope is operations 2–4 and the ping host.
3. OQ4 has a recommendation rather than a deferral: **the operator is a subject,
   not a backend**, and the argument is its own reconciliation loop.
4. A second backend is now a list of eleven things rather than a rewrite, and the
   list is short enough to disagree with.
