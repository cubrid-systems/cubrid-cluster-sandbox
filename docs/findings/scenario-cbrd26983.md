---
title: The scenario that started the project, replayed through the tool
category: findings
project: cluster-sandbox
summary: The CBRD-26983 verification — whether a failover can hand out an AUTO_INCREMENT value the other node already issued — reproduced through five csb verbs, unattended. The id sequence matches the August session exactly, twice. The runs also produced six measurements of a crash-triggered failover, spread over 5.1–7.9 s against the 2.5 s the settings predict.
created: 2026-09-02
updated: 2026-09-02
lang: en
---

# The scenario that started the project, replayed through the tool

[`../DESIGN.md`](../DESIGN.md) §1 opens with what one HA question cost: a
two-node cluster assembled by hand, two configuration files per node, a
four-step slave chain, an ordering constraint, two undocumented traps, and a
failover induced with `docker network disconnect` and `pkill cub_master` because
the supported commands refuse to do it. §2 G3 turns that into an acceptance
criterion — re-running the same scenario through role-addressed verbs has to
reproduce the measured id sequence `1, 2, 21, 22, 41, 42, 61`.

It does. [`../../harness/scenario-cbrd26983.sh`](../../harness/scenario-cbrd26983.sh)
is the whole thing, and it is five verbs:

```
csb cluster create --name cbrd --build <install.out>
csb node exec master -- "csql ... INSERT ..."
csb node kill master
csb node start <the node that was master>
csb record export --out run.json
```

```
   ids after the table is created and two rows inserted:  1 2
   role change 1: kill cbrd-n1  ->  n2 active                21 22
   role change 2: kill cbrd-n2  ->  n1 active                41 42
   role change 3: kill cbrd-n1  ->  n2 active                61

   measured: 1 2 21 22 41 42 61
   expected: 1 2 21 22 41 42 61
```

The jump of twenty at every role change is the serial cache: a node that takes
over starts a fresh block rather than continuing the one its peer was issuing
from. That is the behaviour the original verification was there to check, and
reproducing it is now a five-minute unattended run rather than a day.

**`master` is a query, and this is where that pays.** The script never names a
node to write to. `node exec master` addresses whichever machine is active at
that moment, so the same three lines run before the first failover, between the
second and third, and after the last — which is the whole content of G3 and the
reason the survey rejected pid addressing unanimously.

## What the record caught while it ran

Six crash-triggered failovers across two runs of the same script, each measured
by the tool from its own `node kill` to the engine's own `[Failover] [Success]`
line:

| Run | Role changes, in order | Predicted from the settings |
|---|---|---|
| first | **7.2 s**, **6.8 s**, **7.9 s** | 2.5 s |
| second | **5.1 s**, **5.9 s**, **6.9 s** | 2.5 s |

Nothing about the cluster differed between the runs; the script rebuilds it from
nothing each time. So the spread — 5.1 s to 7.9 s — is the variance of the
measurement itself, and recording it is the point: a single number from a single
run would have looked like a finding rather than like a range.

The predicted figure is arithmetic — `ha_max_heartbeat_gap` × `ha_heartbeat_interval_in_msecs`,
5 × 500 ms — and it is not a claim about the engine. The measurements are two to three times it, on a cluster whose settings the
record carries.

**This does not settle the field's question, and saying so matters.** The
stalled measurement that [`../requirements/02-ha-role-transition-field-evidence.md`](../requirements/02-ha-role-transition-field-evidence.md) §2
describes saw 8–11 s and could not say whether it was the engine, the parameter
or the network that afternoon. Its trigger was a network stop; this one is a
process kill, which is a different path through the heartbeat — a dead process is
noticed, an unreachable peer has to time out. What this run establishes is
narrower and still useful: the gap between the arithmetic and the observation is
real, reproducible, and now recorded automatically with the settings that decide
it, which is the apparatus that measurement never had.

Varying the three parameters against this baseline is M2.5, and it needs the load
driver first ([`../design/06-load.md`](../design/06-load.md)).
