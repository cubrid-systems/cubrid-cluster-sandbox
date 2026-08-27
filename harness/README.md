# `cluster-sandbox` — N65's experiment harness

Design docs: [`../docs/`](../docs/). Roadmap pointer:
`roadmap/projects/30-graduated/N65-cluster-sandbox/`. This directory
is **not** the product. It is the Phase-0 spike the foundation §8 calls for — the
manual assembly written down once, so that the open questions can be answered by
running them instead of by reading more code.

Everything here derives from N54's WU-51b harness
(`for-plan/importdb/m5/ha51b-docker`, outside this repo), which established the container substrate:
a host-built CUBRID tree bind-mounted read-only, a writable `$CUBRID` assembled
over it out of symlinks, and a stock `ubuntu:24.04` base with no engine
dependencies to install.

## What is here

| File | What it is |
|---|---|
| `Dockerfile` | the node image — 51b's, plus `iputils-ping` (required: see findings) and `iproute2` for the fault mechanisms |
| `entrypoint.sh` | one node's HA configuration, plus `HA_PING_HOSTS` and the `createdb` sentinel |
| `lib.sh` | the four-step assembly (`cs_up`) and the fault primitives (`cs_cut` / `cs_heal`) |
| `oq9-splitbrain.sh` | OQ9 — three arms. **Answered**, see `findings/oq9-splitbrain.md` |
| `oq7-lag.sh` | OQ7 — seven phases over one cluster. **Answered**, see `findings/oq7-lag.md` |
| `failback.sh` | **G8** — the semi-automatic failback script. This is the artifact for the technical team, not a tool |
| `failback-demo.sh` | drives a cluster into a failed-over state and runs `failback.sh` against it, so "runnable" is checkable |

## Running

```bash
bash oq9-splitbrain.sh A|B|C     # ~4 min per arm
bash oq7-lag.sh                  # ~7 min
bash failback.sh --db hadb --current <node> --target <node> [--auto]
bash failback-demo.sh            # ~5 min: set up the failed-over state, then run failback.sh
```

Each script builds its own network and containers and removes them on exit.
Artifacts land under `out/`.

The node containers need `--cap-add=NET_ADMIN` and `--init`, and both are
requirements for the eventual provisioner rather than harness shortcuts.
`NET_ADMIN` because both fault mechanisms (`ip route add blackhole`,
`tc qdisc netem`) are route/qdisc operations. `--init` because without a reaping
PID 1, `cubrid heartbeat stop` never returns — see the requirements below.

## Requirements this harness has already produced

- **The partition verb must operate at route level, not interface level.**
  `docker network disconnect` cannot express "cut the peer but keep the ping host",
  and that distinction is the whole of OQ9.
- **`ping` belongs in the image.** `hb_check_ping` shells out to it; without it,
  every master demotes itself on any heartbeat loss.
- **Seeding must wait for a `createdb` completion signal, not for
  `databases.txt`.** The entry appears first, and copying then produces a slave
  that cannot recover.
- **A replication monitor needs a master-side reference.** `db_ha_apply_info` is
  written by `applylogdb`, so it cannot report a stall of the process writing it,
  and during a copy stall the lag it reports *falls*. See `findings/oq7-lag.md`.
- **`cubrid applyinfo -L` is the copied-log path, not the database directory**,
  and the error for getting it wrong also covers "catalog is empty".
- **The container needs a reaping PID 1 (`--init`).** `us_hb_deactivate` polls
  "is any `cub_server` still running" on a one-second sleep
  (`util_service.c:3995-4004`), and a zombie `cub_server` answers yes forever.
  Without an init process, `cubrid heartbeat stop` does all of its work — the
  master log says *Command execution: deactivate. Success.*, the peer is
  promoted — and then hangs in `hrtimer_nanosleep` indefinitely. Measured
  2026-08-27 at five minutes before it was killed.
- **Anything driving the switch must be bounded and judge by roles, not by exit
  status.** Following from the above: the command's return says nothing useful.
