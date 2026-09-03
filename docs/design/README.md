---
title: cluster-sandbox — Design
category: design
project: cluster-sandbox
summary: The design below the architecture. DESIGN.md §4 fixes five layers and their boundaries; these documents specify what crosses them — the command surface, the topology model, the assembly, the fault vocabulary, and what the inspector may and may not claim.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# Design

[`../DESIGN.md`](../DESIGN.md) §4 names five layers and fixes their boundaries.
It stops there. These documents specify the interfaces across those boundaries,
which is what has to exist before anything is built.

| Document | Layer | What it fixes |
|---|---|---|
| [`01-cli.md`](01-cli.md) | consumers | The command surface, its output contract, and its exit codes — the thing `cubrid-testkit` will call |
| [`02-topology.md`](02-topology.md) | 1 | What a topology *is*: presets, counts, overrides, and the `describe` artifact that reproduces one |
| [`03-assembly.md`](03-assembly.md) | 2, 3 | The state machine from empty directory to serving cluster, and every trap it owns on the user's behalf |
| [`04-faults.md`](04-faults.md) | 4 | The verb vocabulary: events, conditions, and what it means to clear one |
| [`05-inspect.md`](05-inspect.md) | 5 | What the inspector reads, and what it is not allowed to claim |
| [`06-traffic.md`](06-traffic.md) | **6** | The workload driver — two kinds of load, and the rate contract that decides whether a measurement means anything |
| [`07-record.md`](07-record.md) | — | The evidence artifact: what happened to the cluster, and both intervals for every role change |
| [`ADR-001-implementation-language.md`](ADR-001-implementation-language.md) | — | Go for the provisioner, shell for the operator-facing scripts. **Accepted 2026-09-02** |

[`../DESIGN.md`](../DESIGN.md) §4 named five layers. **Load is a sixth**, and it
is a late addition: phase 0 assumed a scenario brings its own traffic. The
requirements pass showed that assumption is what left the field's own threshold
measurement unusable for four years, so the driver is a component with a
contract rather than a loop in each scenario's shell script.

## Seven principles, and where each came from

These are not preferences. Each is a conclusion from either the survey or from
running the thing, and each one rules something out.

**1. The tool owns the traps.** Every ordering constraint and configuration
subtlety in CUBRID's HA assembly is the provisioner's problem, not the user's.
This is most of the value and it is also what makes the tool brittle to an
engine change — [`../DESIGN.md`](../DESIGN.md) §6 accepts that trade.

**2. Address by role, never by identity.** A pid does not survive a restart, a
container id means nothing to a scenario, and `master` moves. Every verb takes
a role selector and resolves it at call time. The survey's verdict against pid
addressing is unanimous (`survey/01-04-tidb.md` §4 I3).

**3. Faults have two shapes, and conflating them is a design error.** `kill` is
an *event*: it happens and it is over. `lag` is a *condition*: it is entered,
it holds, and something must clear it. A condition needs an owner, a reversal,
and a record — see [`04-faults.md`](04-faults.md).

**4. Never report a number its source cannot support.** `db_ha_apply_info`
freezes during an apply stall, *falls* during a copy stall, and is absent across
a role change ([`../findings/replication-lag.md`](../findings/replication-lag.md),
[`../findings/failback.md`](../findings/failback.md)). A monitor built on it
alone shows an operator the opposite of the truth. The inspector reports what it
can defend and says so when it cannot.

**5. Machine-readable is not a later mode.** `cubrid-testkit` consumes this tool
from phase 2, so every command has a structured form from the first release, and
the exit code is part of the contract rather than an afterthought.

**6. Correct by default; deviant only by request, and never silently.** The
provisioner writes a configuration a user cannot get wrong. A scenario may need
a wrong one — that deviation belongs to the scenario, is named, and travels in
`describe`, or the artifact reproduces a different cluster than the one that
found the bug.

**7. A measurement that cannot state its inputs is not a measurement.** The
field measured a role change at 8–11 s against an arithmetic 2.5 s, three times,
and could not say whether it was the engine, the parameter, or the network that
afternoon — so the ticket is still open four years later. Everything that follows
from that is structural, not diligence: the load has a stated rate and reports
whether it held it, the role change records both intervals and the settings that
decide them, and a run whose inputs were not what was asked for is marked
invalid rather than published ([`06-traffic.md`](06-traffic.md),
[`07-record.md`](07-record.md)).

## Not yet decided

**Whether `cubrid-testkit` can consume a cluster with no host-facing port.**
Access is `node exec` and `node shell`, which is what keeps port bookkeeping
absent ([`../DESIGN.md`](../DESIGN.md) §6). If testkit needs a socket instead,
the bookkeeping returns and [`03-assembly.md`](03-assembly.md) §6 is where it
lands. M2.4 decides.

**Whether `cubrid-contrib/sandbox`'s build-shell use case is a one-node topology
with a `build` role.** If it is, this project eventually subsumes it
([`../DESIGN.md`](../DESIGN.md) §9 OQ1). Nothing in phase 1 depends on the
answer.
