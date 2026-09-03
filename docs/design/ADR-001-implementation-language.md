---
title: cluster-sandbox — ADR-001 Implementation language
category: design
project: cluster-sandbox
summary: Go for the provisioner, shell for the operator-facing scripts. Accepted 2026-09-02, superseding a proposal of Python written five days earlier — the decisive argument against Go was that nobody in this project's ecosystem writes it, and that stopped being true when cubrid-testkit, the primary consumer, chose Go under the same maintainer.
created: 2026-08-28
updated: 2026-09-02
lang: en
---

# ADR-001 — Implementation language

**Status**: **Accepted 2026-09-02.** Supersedes the proposal of 2026-08-28, which
recommended Python. What changed is in §Decision.

## Context

Three things the language has to carry, and they pull differently:

- **A structured contract.** Every command has a `--json` form, and `describe`
  and the run record are schemas ([`01-cli.md`](01-cli.md) §4–5,
  [`07-record.md`](07-record.md)). This is the part shell is worst at.
- **Distribution to three audiences.** Engine developers (have a build
  toolchain), QA (have a controlled environment), and external contributors
  (have nothing you can assume).
- **Orchestration and concurrency.** Driving `docker` and the engine's CLIs,
  starting the heartbeat on both nodes *at once*, sampling replication while a
  load runs — and, since [`06-traffic.md`](06-traffic.md), **holding a stated rate and
  reporting when it could not.**

And one thing that is easy to miss: **who reads the artifact matters more than
who writes it.** The whole premise of `harness/failback.sh` is that the
technical team marks it up. The tracker shows what that team already reads and
edits — the online rebuild script and its `expect` helpers, patched by hand in
the field down to the SSH port
([`../requirements/01-failback-field-evidence.md`](../requirements/01-failback-field-evidence.md) §4).
They read shell. That settles the operator-facing half in every option below,
and it settles nothing about the provisioner.

## Options

### A. Shell (POSIX sh / bash)

**For.** Zero new dependencies on a machine that already has Docker. The phase-0
spike already works in it (`harness/lib.sh` is the assembly). It is the language
the operator-facing artifacts have to be in anyway, and the language the field's
existing HA tooling is written in — so a support engineer can read the tool's
actual behaviour without a translation step.

**Against.** The JSON contract and the topology model are precisely what shell
cannot hold: no data structures, `jq` as a hard dependency, quoting bugs at every
boundary. Error handling degrades into `set -e` and hope. This project has
already paid for that twice in a week — a `tail -1` picking up csql's trailing
blank line, and a `pkill -f` pattern that matched its own shell. Neither is a
mistake a typed structure permits.

**Verdict.** Correct for the operator-facing scripts. Not viable for the
provisioner.

### B. Python

**For.** Present on every CUBRID development machine. JSON and YAML are stdlib
or one dependency. Fast to write, which matters while the design is still
moving — phase 1 exists partly to discover what phase 2's verbs should be.
`subprocess` plus `concurrent.futures` covers the orchestration honestly, and
the one genuinely concurrent step in phase 1 (heartbeat on both nodes) is two
threads.

**Against.** Needs a runtime and, if the dependency list grows, an environment on
the target machine. Packaging for the external contributor is the weak point —
solvable by staying stdlib-only and shipping a single file, but a constraint to
accept up front. Type safety is by convention, and the two schemas here are a
contract another project builds against. And on inspection its ecosystem
foothold is thinner than it looks: the Python in the engine repository lives in
`contrib/python`, `contrib/scripts` and `contrib/python-obsolete`, not in the
core test tooling.

### C. Go

**For.** The strongest answer to distribution: one static binary, no runtime,
nothing to install — which is exactly the external-contributor half of the
audience. Concurrency is native, and both patterns this design needs are
idiomatic: sampling while loading, and a driver that holds a rate. Typed structs
give the `describe` and record schemas compile-time stability, which matters
because they are a contract rather than an output. And **the neighbouring tools
are Go**: `cubrid-operator` is Go, and `cubrid-testkit` — the consumer this
project's G6 exists for — accepted Go on 2026-09-02.

**Against.** The engine is C and C++, its drivers and test assets are Java, the
field's HA scripts are shell; a person arriving from the engine side is not a Go
author. Compile-edit-run is slower than a scripting loop while the design churns.
The operator scripts stay shell regardless, so Go does not reduce the number of
languages in the repository — it replaces one of them.

## Decision

**Go for the provisioner; shell for the operator-facing scripts.**

### What changed, and why the earlier draft said Python

The 2026-08-28 draft rejected Go on one argument, stated plainly at the time:
Go wins on distribution and *loses on who can fix it* — "a contributor fixing a
provisioning bug would be the only Go author in their week."

That argument no longer holds, for two reasons that arrived together.

1. **The ecosystem is no longer Go-free.** `cubrid-operator` was already Go. On
   2026-09-02 `cubrid-testkit` accepted Go as well, on drivers that read almost
   identically to this project's: single-binary distribution for a one-person
   operation, an orchestrator whose real work is spawning processes and parsing
   their output, and learning curve as the dominant cost. Two of the three
   neighbouring tools are now Go.
2. **The person who will fix this tool is the person writing that Go.** These
   projects share a maintainer. The "only Go author in their week" is not a
   stranger arriving from the engine side; it is the same developer, and after
   testkit they are writing Go weekly. The argument inverted rather than weakened.

The rest of the earlier draft survives intact: shell for anything an operator
reads, edits, or runs on a real host; and a boundary between the halves that is
a data contract rather than a function call.

### Constraints that make this hold rather than drift

1. **The boundary with testkit stays the CLI, not a package.** Sharing a
   language creates an obvious temptation to share Go types for the `csb/v1`
   envelope, `describe` and the record, and to let testkit import them. That
   would convert a process boundary into a build-time dependency — the coupling
   the JSON contract exists to prevent, and which testkit's own ADR forbids from
   its side by fixing its IPC model to subprocess and stdout. A published types
   package is permitted as a **convenience**; the contract is the JSON and the
   exit codes, and it must stay usable from a language that is not Go.
2. **`docker` is driven as a subprocess, not through the SDK.** Unchanged from
   the earlier draft and now a deliberate cost: the Docker SDK is a dependency,
   and the CLI is what a user can reproduce by hand — worth something for a tool
   whose `--verbose` output is meant to teach the assembly.
3. **The shell half is a first-class deliverable, not a shim.** `failback.sh`
   and anything else an operator is meant to read, edit, or run on a real host
   stays shell. It is what the field reads.
4. **Standard library first.** The same discipline the Python draft imposed for
   a different reason: a small dependency set is what keeps a build reproducible
   for someone who has nothing installed.

### Risk, and how it is checked

The accepted risk is the one testkit named for itself: the maintainer's Go
fluency is not yet demonstrated on this codebase. The mitigation is the same,
and it is deliberately not a spike — **M1.1, the command surface and its JSON
envelope, is the validation slice.** It is small, it is on the critical path
anyway, and it exercises the three things that decide whether the choice was
right: the schema types, the subprocess orchestration, and the build-and-ship
story. If it goes badly, this ADR is amended rather than defended.

## Why not Python, stated plainly

Python would be quicker to write for the next four weeks and slower to trust for
the next four quarters. The two artifacts this tool exists to hand to other
people — `describe` and the record — are schemas that another project builds
against, and the language that checks them at compile time is worth more than
the language that writes them faster. The distribution problem was always Go's
to win. What kept Python ahead until now was a maintenance argument that a
sibling project's decision has since reversed.
