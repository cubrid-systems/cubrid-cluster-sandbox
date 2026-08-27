# docs

| Path | What it is |
|---|---|
| [`DESIGN.md`](DESIGN.md) | The design document — problem, goals, architecture, alternatives, decisions. Start here. |
| [`ROADMAP.md`](ROADMAP.md) | Phases, milestones, and where the project actually is |
| [`design/`](design/) | The design below the architecture: command surface, topology model, assembly, faults, inspection |
| [`survey/`](survey/) | How PostgreSQL, MySQL, MongoDB and TiDB solved the same problem, and where CUBRID stands |
| [`findings/`](findings/) | What running it showed — including the two places it contradicted the design |

## Reading order

`DESIGN.md` §1–§3 says what the project is for. §4 fixes the component
boundaries; [`design/`](design/) specifies the interfaces across them. If you
want the evidence rather than the conclusions, [`survey/`](survey/) is the
comparable-engine material the architecture rests on and
[`findings/`](findings/) is what measurement added or took away.

Two findings are worth reading even if you read nothing else, because they
change how the tool has to behave:

- **Split brain needs no misconfiguration** ([`findings/split-brain.md`](findings/split-brain.md))
  — a correctly configured CUBRID cluster reaches two masters in nine seconds
  when the ping host survives the partition.
- **`db_ha_apply_info` cannot measure replication lag on its own**
  ([`findings/replication-lag.md`](findings/replication-lag.md)) — it is written
  by the process it would be reporting on, and during a copy stall the number it
  reports *falls*.
