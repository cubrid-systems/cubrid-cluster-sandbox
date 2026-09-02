---
title: HA role transition — the tracker's own vocabulary, and what it records
category: requirements
project: cluster-sandbox
summary: A second pass over CUBRID's internal tracker, searching its vocabulary instead of this project's. Three findings change existing documents rather than adding to them — "failback" means demotion to CUBRID and to the team, the switchover-threshold work has been open for four years and is stalled on reproducibility rather than knowledge, and a failover can stop half-finished in to_be_active for hours. One open ticket is a stalled test this tool exists to unblock.
created: 2026-08-28
updated: 2026-08-28
lang: en
sources:
  - CUBRID's internal tracker — sixteen development tickets, one QA record, one customer question and five operations records, on role transition, the heartbeat thresholds, to_be_active, fail-count repair and the HA startup paths. Identifiers are deliberately omitted; see the handling note
  - CBRD-23692, CBRD-23609, CBRD-24040, CBRD-20568, CUBRIDMAN-222 (the public tracker, referenced from the above)
---

# HA role transition — the tracker's own vocabulary, and what it records

[`01-failback-field-evidence.md`](01-failback-field-evidence.md) searched the
tracker for *failback* and came back with the rejoin path, the `fail_counter`
alarm, and the failover loop. It ended on four questions about what an
operator *decides*.

This is a second pass, searching the tracker's vocabulary rather than this
project's — `절체`, `원복`, `to_be_active`, the hidden heartbeat parameters, the
rebuild script, the fail count. It returns a larger set, and three items in it
**correct existing documents rather than adding to them**. The first one
explains why the obvious search was nearly empty: `text ~ "failback"` returns
**two** development tickets in a tracker with a decade of HA operations in it.

> **Handling note.** These are internal tickets, and this repository is not.
> Only technical content is reproduced here: **ticket identifiers are omitted**,
> and so are customer and site names, the people named in the threads, and
> credentials. Each record is cited by what it is about and when it was filed —
> "the hidden-parameter test", "the to-be-active report" — which is enough to
> tell two of them apart, to see when a claim was made, and to find either one
> again from inside the tracker.

## 1. "Failback" means demotion — to the engine, and to the team

The source is a set of **HA study notes** written inside the team in 2023 and
reviewed by the lab. Item 2 is one sentence and it invalidates a word this
project has been using throughout:

> 큐브리드에서 Fail Over는 슬레이브 노드가 마스터 노드가 되는 것을 의미하며,
> **Fail Back은 마스터 노드가 슬레이브 노드가 되는 것을 의미합니다.**
> — the team's HA study notes, 2023

The engine agrees, and this project already measured it agreeing:
[`../findings/split-brain.md`](../findings/split-brain.md) quotes
`[Failback] [Success] Current node has been successfully demoted to slave` from
the control arm, and `[Failback] [Cancelled] Ping check succeeded …` from the
two split-brain arms. Every `[Failback]` line in a CUBRID log is about a master
**stepping down**. [`../design/04-faults.md`](../design/04-faults.md) §7 states
this correctly in one clause and then uses "failback" the other way in the next.

So there are two operations and one word:

| | Engine and team call it | This project has been calling it |
|---|---|---|
| A master demotes itself because another master exists | **failback** (engine logs, study notes) | split-brain recovery |
| Service returns to the node that was master before the failover | **failback 작업** (operations, 95 tickets) | **failback** |

**Corrected 2026-09-02 — the second row does have a name, and this document had
it wrong.** The claim here was that the tracker has no term for returning to the
original master, inferred from `text ~ "failback"` returning two development
tickets. That search was the wrong shape: the operational usage is Korean-suffixed
and lives in the support project. **`failback 작업` appears in ninety-five
tickets**, the most recent from 2025, and one of them settles the meaning beyond
argument:

> 재구성 없이 1번을 마스터로 **failback**만 하는 작업 진행

That is the return trip, named, and explicitly distinguished from a rebuild.

So the word means two things inside one organization. The **engine** logs
`[Failback]` when a master steps down, and the team's own study notes define it
that way. **Operations** use `failback 작업` for putting the service back on the
node that was master. Both usages are current, and that is worse than either one
being wrong: a question asked with the bare word gets answered about whichever
the reader has in mind. The requirement below is unchanged and better founded —
the script has to say which operation it means.

**Requirements.**

- `harness/failback.sh` needs a fifth edit before it goes to the team, ahead of
  the four in [`01`](01-failback-field-evidence.md) §7: **say which operation it
  means, in the team's vocabulary.** A script that asks "what do you require of
  failback" will be read as asking about demotion, and the four judgement
  questions will be answered about the wrong operation.
- This project should stop using the bare word. `04-faults.md` §7 keeps
  `ha failback` as a CLI verb only if it is disambiguated in the text; the
  operation itself is better named **return-to-original-master**, which is what
  it is.

The same notes state the score-based demotion path in plain language, and it
matches the code reading in [`../findings/split-brain.md`](../findings/split-brain.md):

> 마스터 노드에서 calculate score 과정을 수행할 때 자신보다 minScore에 가까운
> 노드가 있을 경우 Fail Back(마스터 → 슬레이브)이 필요한데, 이 경우 Ping Check를
> 통해 자신의 네트워크를 점검하여 실패할 경우 Fail Back을 수행합니다. **핑 체크가
> 활성화 되어 있지 않을 경우 Fail Back을 실행하지 않습니다.**

That last sentence is the `no-ping-hosts` flavour, written down by the team in
2023 and independently measured here at 13 s. The notes add a third use of
`ha_ping_hosts` this project had not recorded: an **hourly** ping sweep of the
registered hosts, falling back to a 5-minute recheck once all of them fail.

## 2. The switchover-threshold test already exists, is four years open, and stalled on reproducibility

[`../ROADMAP.md`](../ROADMAP.md) M2.5 rests on the **switchover-settings
ticket** (2021), which asks for the switchover settings to be documented and
validated. That one is resolved; **the hidden-parameter test `Blocks` it and is
still open, unchanged since February 2022.**

The hidden-parameter test is an attempt at exactly the measurement M2.5
proposes. Three hidden parameters, absent from `paramdump` — which the lab
confirmed in 2023, answering a customer who asked how failover is triggered:
`ha_heartbeat_interval_in_msecs` (500 ms), `ha_max_heartbeat_gap` (5),
`ha_calc_score_interval_in_msecs` (3000 ms).

| What was varied | Expected | Measured role change |
|---|---|---|
| nothing (defaults) | 2.5 s — 5 × 500 ms | **8–11 s**, three runs |
| `ha_max_heartbeat_gap` 5 → 10 | later than default | ~10 s — *unchanged* |
| `+ ha_ping_hosts` | later still | ~10 s — *unchanged* |
| `ha_calc_score_interval_in_msecs` raised | later | visibly later — the only parameter that moved it |

Two facts sit in that table. The **documented** trigger — 500 ms × 5 consecutive
misses, which the lab restated to that customer in 2023 — predicts 2.5 s and
the cluster takes three to four times that. And the two parameters an operator
would reach for to delay a failover **do not delay it**, while the one that does
is the one nobody understands.

The ticket then records three anomalies under a raised `calc_score` interval,
and this is the part that matters most:

> 특이사항 2) Active-Active 상태 (양방향 데이터 동기화 가능)
> ha_calc_score_interval_in_msecs 파라미터 적용 후 … 2번 서버에서 Standby →
> Active로 승격한 상태에서 다시 네트워크가 정상적으로 동작할 때 **파라미터 적용
> 시간만큼 Active-Active로 운영됨**

Plus a node reporting itself as `to-be-master` while it is master, and a `csql`
connection succeeding without a hostname. All three are recorded as
`특이사항` — noticed, not explained.

And the ticket's own closing uncertainty is the reason it is still open:

> 1) 테스트 케이스 문제? 2) ha_max_hearbeat_gap 파라미터 값을 너무 작게 설정?
> 3) 시간에 따라 네트워크 속도 차이 문제?

**This is not a knowledge gap, it is a reproducibility gap** — a VM pair, one
`service network stop`, and no way to tell an engine behaviour from a test
artefact. It is the most direct commission for this tool found in either pass,
and it is more specific than the switchover-settings ticket's "validate in a
user's environment":
settle whether the 8–11 s is real, whether `ha_max_heartbeat_gap` is inert, and
whether the Active-Active window exists.

**Requirements.**

- **A third split-brain flavour.**
  [`../design/04-faults.md`](../design/04-faults.md) §5 carries two flavours,
  both fault-induced, both producing two masters with replication running in one
  direction. This one is **parameter-induced**, arrives *after* the network
  heals, and is reported as **bidirectional**. If it reproduces, it is a
  different anomaly wearing the same name.
- **Every role change records its inputs and both times.** The settings in
  force, the load, the *measured* interval, and the interval the parameters
  predict. The hidden-parameter test stalled precisely because it could not
  separate those.
- **The hidden parameters are part of the topology surface.** They are absent
  from `paramdump`, so a `describe` artifact that omits them does not reproduce
  the cluster that produced the result.

## 3. A failover can stop half-way, for hours — the to-be-active report (2023)

At a public-sector deployment a failover started and did not finish: the new
master sat in **`to-be-active` from 01:00 to 09:00**, refusing writes.

> failover 발생하면서 기존 master노드가 standby의 로그를 반영하지 못해
> to-be-active상태로 지속 — 원인 : **잘못된 db_ha_apply_info 정보로 인하여
> 삭제된 아카이브 로그를 찾음**

The lab's explanation supplies a piece of the state machine this project's
design does not have:

> copylogdb에서 master db와 연결 끊기거나 fail-over를 인지하면 replication log에
> **dead 라는 로그**를 남기게 됩니다. slave 서버는 cub_master의 fail-over 동작에
> 의해 to-be-active 상태로 전환된 후 **applylogdb가 dead log를 만났을때**
> to-be-active를 active로 전환하라고 요청하게 됩니다.

So the promotion completes only when the applier *reaches* the dead record. Any
condition that stops it short — a missing archive, a wrong `db_ha_apply_info`
row, a stalled applier — leaves the node holding the service and refusing
writes, indefinitely. The engine will not shortcut it, and that refusal is
deliberate: forcing `active` would apply replication log over data written after
the switch (`데이터 정합성`). The product screening meeting of 2024-04-16
rejected a forced
transition and assigned two things to the lab instead:

> 어떠한 원인으로 to-be-active를 active로 강제 변경하는 것은 다소 무리가 있다
> 판단 함. 그러나, **to-be-active가 발생하는 조건/요건에 대한 것은 분석이
> 필요**하다고 판단됩니다. … 에러 메시지를 상세히 출력이 가능한지도 검토 요청

"Analyse the conditions that produce `to_be_active`" is a sandbox task. The
conditions are constructible: fail over with the applier suspended
([`../findings/replication-lag.md`](../findings/replication-lag.md)'s mechanism),
or with the archive the row points at removed.

**Requirements.**

- `to_be_active` is a **first-class HA role** in the inspector, not an
  in-between to be smoothed over. A node in it is live, holds the service, and
  cannot serve writes — [`../design/05-inspect.md`](../design/05-inspect.md) §5's
  status table has no way to say that today.
- Ship "failover that does not complete" as a named scenario, with the two
  constructions above.
- It belongs next to return-to-original-master in the documents. Both are the
  case where the roles are not what anyone wants and the engine will not move
  them.

## 4. Divergence repair: what the field does today, and what it asked for

[`01`](01-failback-field-evidence.md) §4 named `ha_make_slavedb.sh` as the
rejoin path and asked what happens when the old master's log diverged. Two open
requests — a **fail-count utility request** (2024) and a **`checksumdb`
proposal** (2025), since merged into the first — answer it for the *slave* side,
and they answer it with what an engineer does by hand:

> 현 CUBRID HA 환경에서 Fail count 발생될 경우 기술본부 엔지니어는 Fail count
> 발생한 테이블 잘 찾아 **대상 테이블만 재구성하거나 아니면 슬레이브 DB를
> 재구성하는 두 가지 선택**입니다.

with an informal cut-off between them:

> 특히 DB 사이즈가 큰 사이트에서 file count 1,000건 단위로 넘어 가는 경우
> **대부분 HA 재구성**하는 비효일적인 기술지원이 많이 발생되고 있습니다.

The requested utility (`checksumdb` extended, or a new `harebuild`; backport
targets 10.2, 11.0, 11.3, 11.4) reads the table and PK out of
`applylogdb.err`, diffs those rows against the master, and repairs row-by-row
(`--rebuild-row`) or by table (`--rebuild-table`), verifying up to three times.

**Two things in that thread matter beyond the utility.**

**It names the quiesce mechanism**, which [`01`](01-failback-field-evidence.md)
§8 listed as unknown. Step 4-1 of the proposal:

> 이 명령어 수행 전 대상 DB에 **외부 입력 차단을 위해 브로커 ACCESS_MODE는 RO/SO
> 변경** 합니다.

The field blocks writes by moving the broker's access mode, not by stopping the
server. That is a verb this tool should have, and it is how a
return-to-original-master would quiesce traffic too.

**And it states why `fail_counter` must keep lying upward rather than be
cleared** — an argument that strengthens
[`../design/05-inspect.md`](../design/05-inspect.md) §3:

> 제 생각에는 applylogdb에서 fail count를 최대한 발생하지 않게 조치 … 하지 않은
> 이유가 운영자에게 fail count와 applylogdb.err를 통해 fail count가 발생한
> 테이블에 대해 동기화를 확인하게 한 것 같습니다. 즉, **fail count가 0로
> 표시되면, 운영자는 master와 slave의 모든 테이블이 동기화가 되었다고 오판**할 수
> 있을 것 같네요.

The counter is a *deliberately* unresolved alarm. A tool that reports it must
not present zero as agreement — and the field admits it sometimes zeroes the
row by hand when the diff comes back clean, which is a state the tool can
produce and should be able to name.

Status, so the roadmap does not assume it is coming: the PM team's weekly
meeting ranked PK constraint removal, LOB constraint removal and replication
performance as priority 1 for 11.5 Guava, and full-sync verification /
per-table resync as priority 2. The utility is behind three engine changes.

Two rebuild foot-guns not in [`01`](01-failback-field-evidence.md)'s table,
both from 2019: online rebuild deleted the volumes of a *similarly named*
database rather than the target; and dropping `db_ha_apply_info` during a manual
rebuild made the database unopenable in 10.x (recoverable in 9.x), fixed by
forbidding system-catalog drops (CBRD-24040). Both are the class of accident a
sandbox is for.

## 5. Two traps for the assembly, and a second reason to cut routes

**The single-node start report (2019), fixed as CBRD-23692 — the errno decides
whether replication survives.** A
master alone could not start HA when the slave sat on another network with no
route, and could when the route existed but the peer did not answer. The
mechanism is `connect()`'s error:

| Reachability | `connect()` | Effect |
|---|---|---|
| no route to the peer's network | `ENETDOWN` → timeout | copylogdb **exits**; `heartbeat start` fails |
| route present, peer absent | `EINPROGRESS` → `ERR_CSS_TCP_CANNOT_CONNECT_TO_MASTER` | copylogdb **retries**; HA starts |

CBRD-23692 made master-only start succeed in both cases in 11.0, *with different
error messages* (`-353 … Operation now in progress` versus
`-1144 Timed out attempting to connect … (timeout: 5 sec(s))`).

This is a second, independent reason for
[`../design/04-faults.md`](../design/04-faults.md) §3's route-level cut, and it
adds a requirement: **the tool must be able to produce both shapes.** A
blackhole route produces the first; "route intact, peer gone" is a different
fault and a different code path. And a master must be able to start alone —
an assembly requirement that the happy path never exercises.

**A mixed configuration makes HA fail silently at startup** (2016). With one
HA database and one non-HA database in the same `cubrid.conf`, `cubrid service
start` brings up the local server and then:

```
The server was not configured for HA.
++ cubrid heartbeat start: fail
```

Heartbeat starts **last**, and the per-database `[@dbname] ha_mode=off` section
applied while the local server started is never restored, so heartbeat reads
`ha_mode=off` and declines. `cubrid hb start` afterwards works. Filed upstream
as CBRD-20568, **rejected as not a product issue**, and closed in 2017 for
10.1 compatibility — so it is live behaviour, not history.

Two rules follow, and they are the reason the harness already works: never
write `[@dbname]` sections carrying `ha_mode`, and start the heartbeat
explicitly rather than through `cubrid service start`. That belongs in
[`../design/03-assembly.md`](../design/03-assembly.md) §2 as a trap the tool
owns, especially once `single` and `ha` presets can coexist on one machine.

## 6. Only four HA settings can be changed without a restart — the dynamic-settings review

QA compiled a table of `cubrid_ha.conf` parameters marking about twenty as
dynamically changeable; the lab rejected the verification method and reduced it
to four confirmed in code — `ha_node_list`, `ha_replica_list`, `ha_ping_hosts`,
`ha_tcp_ping_hosts` — with the scope of `reload` stated exactly:

> reload는 **cub_master의 system parameter를 변경하는 것**으로 다른 process
> (copylogdb, applylogdb, cub_server)는 영향을 미치는 것은 아닙니다.

The manual was corrected accordingly (CUBRIDMAN-222).

**Requirements.**

- The three `ha_ping_hosts` states in
  [`01`](01-failback-field-evidence.md) §2 can be entered **live**, on a running
  cluster, via `cubrid heartbeat reload`. That makes them cheap conditions
  rather than rebuild-and-restart scenarios.
- Every other threshold sweep — including all of §2's hidden parameters — costs
  **one cluster per value**. That is the cost model M2.5 has to be planned
  against, and it is an argument for `create`/`destroy` being fast and
  scriptable rather than merely correct.
- Two more failover triggers surface from the same table:
  `ha_check_disk_failure_interval` (15 s), whose cause is now recorded in the
  log since a 2019 request landed as CBRD-23609 (11.0, 10.2.1, 10.1.4), and
  `ha_unacceptable_proc_restart_timediff` (120 s) — the window that decides
  whether a restarting HA process is treated as unacceptable, which is the
  mechanism the failover-loop report's ten-cycles-a-day runs through.

## 7. The inspector's own sources have bugs

[`../design/05-inspect.md`](../design/05-inspect.md) §3 gives three ways
`db_ha_apply_info` misleads about lag. The tracker adds a fourth category: the
sources are **defective**, independently of what they mean.

| Reported | Source | Defect |
|---|---|---|
| 2025 | `cubrid applyinfo` | output is malformed once a dba password is set |
| 2024 | `db_ha_apply_info` | **two rows** after `ha_copy_log_base` changes without the old row being deleted |
| 2025 | `cubrid hb status` | replica nodes' HA-Process Info state is displayed wrongly |
| 2022 | `cubrid hb status` | the manual's own description of the applylogdb/copylogdb fields was wrong |

The second one is load-bearing: a reader that assumes one row per database
silently picks whichever it gets. **Requirement:** T2 reads defensively — count
the rows and report `ambiguous_apply_info` rather than choosing, and treat a
non-empty `applyinfo` parse as untrusted (which §3's rule already implies, since
the tool computes from SQL and the master's append position instead).

Two smaller gains from the study notes worth keeping:

- **`ha_enable_sql_logging`** writes the SQL that `applylogdb` applies, under
  `ha_copy_log_base/sql_log`, with per-statement timestamps. It is human
  formatted, so it is not an inspector source — but it is the best available
  answer to "what was replication actually doing", and the tool should be able
  to turn it on per scenario and hand a user the path.
- **A fail-count recipe**, which the tool currently has no way to produce:
  create a table, insert rows, *then* add the primary key, then delete the
  pre-key rows on the master. The slave's applier fails with
  `ERROR CODE = -1034 … failed to apply delete replication log. class: "public.users",
  key: "1", server error: -711` (`log_applier.c:4796`). `fail_counter` is
  [`05-inspect.md`](../design/05-inspect.md)'s separator between *broken* and
  *behind*, and this is how a scenario moves it deliberately.

The notes also record `csql --write-on-standby` as not working (11.2, with
`--sysadm` too), unresolved. Anything in this tool that needs a write on a
standby should not rely on it.

## 8. Dead ends, recorded so nobody searches them twice

- **A 2017 operations record** titled
  `HA Fail-Over / Fail-Back 빈번한 이슈 사항` looks like the failover-loop
  report's field twin. It has a one-line description and one comment
  (*"해당 결과 나오면 공유 부탁 드립니다"*). There is nothing in it.
- **The 장애복구 모의훈련 records** — four of them, 2017 to 2018 — are the most
  promising-sounding search result in the tracker and contain **no procedure**:
  drill dates, muster locations, contact names, DB size, and a closing comment.
  Quarterly failover drills at government sites are where a written failback
  procedure would live, and it is not in the tracker.
- **A 2020 QA effort** tried to write the HA constraint checklist and
  inspection method the field wanted. It has a taxonomy, one worked example
  (Manager query automation configured only on the master breaks after a
  failover, because write queries do not follow the active node), and then:
  *"이후에는 진행사항이 없었습니다. 이 이슈는 종료하겠습니다."* The checklist does
  not exist.

The pattern across all three is the same one [`01`](01-failback-field-evidence.md)
§8 found: the tracker records **what went wrong** in detail and **what an
operator decided** almost not at all.

## 9. What this changes

| Document | Change |
|---|---|
| [`../design/04-faults.md`](../design/04-faults.md) | §5 gains a third split-brain flavour (§2 here), parameter-induced and possibly bidirectional; §3 gains the two reachability shapes (§5 here); §7's use of "failback" is disambiguated (§1 here) |
| [`../design/03-assembly.md`](../design/03-assembly.md) | a seventh trap: mixed HA/non-HA configuration plus `cubrid service start` (§5 here) |
| [`../design/05-inspect.md`](../design/05-inspect.md) | `to_be_active` as a reported role (§3 here); defective sources as a fourth caution, with `ambiguous_apply_info` (§7 here) |
| [`../design/01-cli.md`](../design/01-cli.md) | `--flavour` list follows `04-faults.md` |
| [`../ROADMAP.md`](../ROADMAP.md) | M2.5's real blocker is the hidden-parameter test, and its cost model is one cluster per value (§6 here) |
| [`../DESIGN.md`](../DESIGN.md) | §9 OQ8 is asked in the wrong vocabulary; `failback.sh` needs a fifth edit before it goes out |

## 10. What is still unknown

[`01`](01-failback-field-evidence.md) §8 listed four. One moved:

- ~~whether write traffic is quiesced first, and how~~ — **the broker's
  `ACCESS_MODE`, set to RO/SO** (§4). Not proven to be what a site does during a
  return to the original master, but it is what the field does before touching
  replicated data.
- the threshold for "caught up enough" to switch back — still open. §4 supplies
  only a repair-versus-rebuild cut-off (~1,000 fail rows), which is a different
  decision.
- who authorises it, and on what evidence — still open.
- whether the original master is preferred at all — still open, and §1 makes the
  silence more interesting rather than less: the team has no word for the
  operation.
