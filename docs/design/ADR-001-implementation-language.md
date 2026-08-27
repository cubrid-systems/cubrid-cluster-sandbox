---
title: ADR-001 — Implementation language
category: design
project: cluster-sandbox
status: proposed
summary: Python for the provisioner, shell for the operator-facing scripts. Go is the strongest single answer on distribution and is rejected on ecosystem grounds; shell alone cannot carry the JSON contract; the split follows from who reads which artifact.
created: 2026-08-28
updated: 2026-08-28
lang: en
---

# ADR-001 — Implementation language

**Status**: proposed. Must be decided before M1.1
([`../ROADMAP.md`](../ROADMAP.md)).

## Context

Three things the language has to carry, and they pull differently:

- **A structured contract.** Every command has a `--json` form and `describe`
  is a schema ([`01-cli.md`](01-cli.md) §4–5). This is the part shell is worst at.
- **Distribution to three audiences.** Engine developers (have a build
  toolchain), QA (have a controlled environment), and external contributors
  (have nothing you can assume).
- **Orchestration and concurrency.** Driving `docker` and the engine's CLIs,
  starting the heartbeat on both nodes *at once*, and sampling replication while
  a load runs.

And one thing that is easy to miss: **who reads the artifact matters more than
who writes it.** The whole premise of `harness/failback.sh` is that the
technical team marks it up. The tracker shows what that team already reads and
edits — `ha_make_slavedb.sh` and its `expect` helpers, patched by hand in the
field down to the SSH port
([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §4).
They read shell.

## Options

### A. Shell (POSIX sh / bash)

**For.** Zero new dependencies on a machine that already has Docker. The Phase-0
spike already works in it (`harness/lib.sh` is the assembly). It is the language
the operator-facing artifacts have to be in anyway, and the language the field's
existing HA tooling is written in — so a support engineer can read the tool's
actual behaviour without a translation step.

**Against.** The JSON contract and the topology model are precisely what shell
cannot hold: no data structures, `jq` as a hard dependency, quoting bugs at every
boundary. Error handling degrades into `set -e` and hope. This project has already
paid for that twice in a week — a `tail -1` picking up csql's trailing blank
line, and a `pkill -f` pattern that matched its own shell. Neither is a mistake a
typed structure permits.

**Verdict.** Correct for the operator-facing scripts. Not viable for the
provisioner.

### B. Python

**For.** Present on every CUBRID development machine, and already in the
organization (`cubrid-spatial-stack`) and in CUBRID's own test tooling. JSON and
YAML are stdlib or one dependency. Fast to write, which matters while the design
is still moving — phase 1 exists partly to discover what phase 2's verbs should
be. `subprocess` plus `concurrent.futures` covers the orchestration honestly,
and the one genuinely concurrent step (heartbeat on both nodes) is two threads.

**Against.** Needs a runtime and, if the dependency list grows, an environment on
the target machine. Version skew across the distributions CUBRID supports is
real. Packaging for the external contributor is the weak point — solvable by
staying **stdlib-only** and shipping a single file, or with `uv`, but it is a
constraint to accept up front rather than discover.

### C. Go

**For.** The strongest answer to distribution: one static binary, no runtime,
nothing to install — which is exactly the external-contributor half of the
audience. Concurrency is native, and the sampling-while-loading pattern is
natural. Docker has a first-class Go SDK, so container work stops being
`docker` subprocess parsing. And the organization is not Go-free: **`cubrid-operator`
is Go**, and it is the neighbouring provisioning tool — the one this project may
eventually share a topology model with
([`../DESIGN.md`](../DESIGN.md) §9 OQ4).

**Against.** Nothing else in the ecosystem this project sits in is Go — the
engine is C, the drivers and test tooling are Java, the analysis tooling is
Python, the field's HA scripts are shell. A contributor fixing a provisioning bug
would be the only Go author in their week. Compile-edit-run is slower than the
design's current rate of change, and phase 1 is going to churn. The operator
scripts stay shell regardless, so Go does not remove the second language; it adds
a third.

## Decision

**Python for the provisioner; shell for the operator-facing scripts.**

Constraints that make this hold rather than drift:

1. **Stdlib only for the core.** No dependency the external contributor has to
   install. `docker` is driven as a subprocess — the Docker SDK is a dependency,
   and the CLI is what a user can reproduce by hand anyway, which is worth
   something for a tool whose `--verbose` output is meant to teach the assembly.
2. **The shell half is a first-class deliverable, not a shim.** `failback.sh`
   and anything else an operator is meant to read, edit, or run on a real host
   stays shell. It is what the field reads.
3. **The boundary is the JSON contract.** The Python core emits it; the shell
   scripts consume it or stand alone. Neither reaches into the other's internals.
4. **Revisit if the operator half grows.** If shell accumulates enough that the
   two halves duplicate logic, Go becomes the better answer — one binary that is
   both — and the cost of switching is bounded because the contract in
   [`01-cli.md`](01-cli.md) is language-independent.

## Why not Go, stated plainly

Go wins on the criterion this project cares most about — handing a working thing
to someone with nothing installed — and loses on the criterion that decides
whether the thing survives: **who can fix it**. This tool's value is a body of
knowledge about CUBRID's HA assembly, and it will need editing every time the
engine's sequences move. The people who will notice are engine developers, QA,
and support engineers. None of them writes Go today.

If `cubrid-operator` and this project converge on a shared topology model
(§9 OQ4), that is the moment to reopen this — and the JSON contract is what makes
reopening cheap.
