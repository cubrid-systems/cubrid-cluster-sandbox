---
title: Failback — what the field actually asks for
category: requirements
project: cluster-sandbox
summary: Requirements gathered from CUBRID's internal RND tracker (jira.cubrid.com) for DESIGN.md §9 OQ8. The engine-level return-to-original-master this project modelled is the smaller half; the field's failback problems are repeated unnecessary failover under load, a ping-host setting with three states and three different failure modes, and a slave-rebuild script with a long history of trouble. One ticket is a direct commission for this project.
created: 2026-08-28
updated: 2026-08-28
lang: en
sources:
  - RND-49, RND-1509, RND-1766, RND-2518, RND-1809, RND-2504, RND-2244, RND-2236, RND-2712, RND-1491, RND-1808, RND-2647 (jira.cubrid.com)
  - CUBRID-2275, CUBRID-2323 (operations tickets, same tracker)
  - OFFICE-281, OFFICE-299, OFFICE-418, OFFICE-434, OFFICE-458 (same tracker)
---

# Failback — what the field actually asks for

[`../DESIGN.md`](../DESIGN.md) §9 OQ8 says this project does not know what the
technical team requires of failback, and that
[`../../harness/failback.sh`](../../harness/failback.sh) is the instrument for
asking. Before sending it, the internal RND tracker was searched for what the
team has already written down. It has written down a great deal.

> **Handling note.** These are internal tickets. Only ticket identifiers and
> technical content are reproduced here — customer contact details and the
> credentials that appear in some comment threads are deliberately excluded.

**The headline: the operation this project modelled is not the one that hurts.**
`failback.sh` models a *deliberate* return to the original master after a clean
failover. The field's failback pain is (1) failover that should never have
happened, repeatedly, and (2) getting a slave back into the group afterwards.

## 1. Unnecessary failover, in a loop — RND-49

Four customer sites reported *frequent* failover and consequent failback.

> 네크웍 오류는 없어 보이는 데 **하루에도 10번 이상 failover / split-brain /
> failback 현상이 반복적으로 발생**하고 있으며, standby 장비의 log 적용 failure
> 횟수가 증가하고 있음

No `PING failed for` in the log, so not a network fault. The diagnosis in the
ticket: heartbeat responses do not arrive inside the configured window, the
slave starts a failover, split brain is then detected, and it fails back —
over and over. The damage is second-order: the replication processes stop and
restart repeatedly, so apply falls behind and the failure count climbs.

Two things this project should take from it.

**The repro recipe is already written, and it is a sandbox workload.** From the
ticket: *"가상머신에서 HA 구성을 하고 큐브리드 빌드(Thread 20~40) 등의 로드 심한
로드 상황을 발생시키면 재현 가능"* — HA on VMs plus a heavy load. That is a
scenario, not a bug report, and it is one `cluster-sandbox` should ship.

**The proposed fix is a settings policy**, not a code fix: revisit the failover
decision window and the polling count. Which means the thing that has to be
*varied and measured* is configuration — see §3.

## 2. `ha_ping_hosts` has three states and all three fail — CUBRID-2275, RND-1766, and this project's own measurement

| State | Outcome | Evidence |
|---|---|---|
| **Unreachable** host | after a failover the slave **cannot promote** — both nodes standby, **service down** | CUBRID-2275 |
| **Unset** | partition is never diagnosed → **split brain**, persisting until an operator intervenes | RND-1766 states this is why it must be set; measured here at 13 s |
| **Set and reachable** | **split brain anyway**, when the ping host survives the partition | measured here at 9 s ([`../findings/split-brain.md`](../findings/split-brain.md)) |

CUBRID-2275's resolution is worth quoting because it is what a support engineer
actually did:

> ping_host 설정이 ping이 되지 않는 ip로 설정되어 있어 failover 이후 slave에서
> master로 변경되지 못함. (ping host 설정 삭제하고 재 시작 후 서비스 확인 완료)

They deleted the setting and restarted — which moves the cluster from the
first row of that table to the second. **Both are broken; they differ in how.**
This is exactly the asymmetry in `hb_cluster_job_check_ping`: a master cancels
its failback when the ping *succeeds*, a slave cancels its failover only when
its own ping *fails*.

**Requirement.** The three states are three scenarios, and the tool must be able
to produce all of them and tell them apart. Outcome alone cannot: rows 2 and 3
both give two masters, and only the `[Failback] [Cancelled]` reason
distinguishes them ([`../design/04-faults.md`](../design/04-faults.md) §5).

### `ping` is an OS permission, and it has blocked deployments — RND-1766

At sites where security policy denies the DB account permission to run `ping`,
setting `ha_ping_hosts` makes the engine **fail to start** — reported from a
Daegu platform deployment, with the customer stating that further CUBRID
adoption needs a non-ICMP path. `hb_check_ping` hardcodes the command
(`popen("ping …")`), and the ticket's proposed workaround was `tcping`;
`ha_tcp_ping_hosts` exists in the engine today.

This is the field form of a constraint this project met in a container: an image
without `iputils-ping` returns 127, which the caller reads as
`HB_PING_FAILURE`, so **every master demotes itself on any heartbeat loss**.
Same root cause, two environments.

**Requirement.** Model both ping modes (`ha_ping_hosts` and
`ha_tcp_ping_hosts`) and make "the ping mechanism is unavailable" a reproducible
condition, not an accident.

## 3. The settings that cause a switchover are legacy, unvalidated, and mostly unlogged — RND-1509

This is the team's own requirement, and part of it is addressed to a tool that
did not exist when it was written. The trigger: with a 2 GB `data_buffer`, a
`cub_server` restart after a core took about 23 seconds against a 20-second
threshold, so the cluster switched over — and *nobody knows when or by whom the
20 seconds was set*.

> 그렇다면 상당수의 환경에서는 cub_server core 가 발생하면 절체가 될 수 있다는
> 부분입니다. 따라서 이 부분은 현행화가 필요합니다.
>
> 또한 **어떤 상황에 절체가 될수있는지 로그에 남지 않는 부분들이 많습니다.**

Three asks, verbatim in structure:

1. **Explain the settings that influence switchover.**
2. **Validate that those settings are appropriate** — and the ticket is explicit
   that developers cannot do this: *"이건 개발자가 하기엔 어려울 겁니다.
   개발자의 눈은 한계가 있기 때문입니다. 사용자 입장에서 사용자의 환경에서
   적절하게 동작하는지 … 검증이 필요합니다. 이건 제품에 대한 검증이므로 QA 에서
   이루어져야 할 것입니다."*
3. **Log it when a setting caused the switchover**, so a user can at least know
   that is what happened.

**Ask 2 is a commission for this project.** "Does this threshold behave
appropriately in a user's environment" is a question you answer by standing up
that environment, varying one setting, and watching what the cluster does — with
a load heavy enough to matter (§1). That is what `cluster-sandbox` is.
[`../ROADMAP.md`](../ROADMAP.md) should carry it as a phase-2 use case rather
than leaving it implicit.

Ask 3 is an engine change and belongs elsewhere, but it bounds what the tool can
observe today: **a switchover caused by a threshold may leave nothing in the
log**, so the tool must record the *inputs* (settings, load, timings) alongside
the outcome, or a reproduced switchover is unattributable.

## 4. The rejoin half has a name, and a history — `ha_make_slavedb.sh`

`failback.sh` STEP 6 asks: *"How do you detect that the old master's log
diverged before rejoining it, and what do you do when it has?"* The field's
answer is the online HA rebuild script, and the tracker shows it is fragile:

| Ticket | What goes wrong |
|---|---|
| OFFICE-458 | `backup_dest_path` / `backup_option` not applied; port 22 unusable and the port is awkward to change; `ssh`-then-`cubrid` on the master unreliable — so a **manual** rebuild procedure was needed |
| OFFICE-299 | the SSH port is hardcoded across `ha_make_slavedb.sh` and its `expect` helpers (`scp_from.exp`, `scp_to.exp`, `ssh.exp`) |
| OFFICE-418, RND-1809 | online rebuild applies transaction logs it should not, producing **Fail Count** |
| RND-2504 | apply delay after an online rebuild |
| RND-2244 | a second slave built with the script is recognised as a *replica* |
| RND-2236, RND-2712, RND-1491, RND-1808 | multi-DB argument handling, archive copy errors, backup directory misconfiguration |
| RND-2647 | multi-slave / multi-replica rebuild was never in scope; a spec is being written |

**This is the same operation as `cluster-sandbox`'s assembly.**
[`../design/03-assembly.md`](../design/03-assembly.md) builds a slave from the
master's volumes and owns the ordering traps; `ha_make_slavedb.sh` does it over
SSH on real hosts. The overlap is not accidental and should be deliberate:

**Requirements.**
- The failback script's rejoin step must offer *both* paths — resume replication,
  or rebuild — and must say how it decided.
- The known rebuild failure modes above are **scenarios worth reproducing**,
  because a sandbox is where you can afford to break a rebuild.
- Ports and paths that OFFICE-299 had to patch by hand in the field are exactly
  the parameters the topology model already treats as configuration
  ([`../design/02-topology.md`](../design/02-topology.md) §5).

## 5. Fail count is the alarm, and its diagnostics are thin

OFFICE-281 asks for **detailed `applylogdb` error records so that a Fail Count
can be acted on quickly**. RND-2518 (still in progress) tracks fail-count growth
after an abnormal shutdown; RND-1809 and OFFICE-418 tie fail counts to online
rebuilds; RND-1516 records an `applylogdb` core that broke synchronisation
outright.

This confirms the choice in [`../design/05-inspect.md`](../design/05-inspect.md)
— `fail_counter` is what separates *broken* from *behind* — and adds to it:

**Requirement.** When `fail_counter` moves, the tool surfaces **why**, by
pulling the applier's error log alongside the counter. A number that has gone up
with no reason attached is the state OFFICE-281 is complaining about.

## 6. Bulk load outrunning the applier is a field case with a written repro — OFFICE-434

`loaddb` on the active node produced apply delay on the standby at a real site.
The ticket carries the full reproduction procedure and `applyinfo` output as the
observable.

That is the same shape this project measured under its own load
([`../findings/replication-lag.md`](../findings/replication-lag.md) §"Capacity"),
and the same worry the roadmap raised about bulk import at `--degree=4`.

**Requirement.** Ship it as a named scenario. The observable is already agreed on
in the field — the two `applyinfo` delay figures — which is a reason for the
tool's own `repl status` to report *those two stages* and not a single invented
number.

## 7. What changes in `failback.sh`

The script goes to the technical team either way; these are the edits to make
first, because the tracker already answers them and asking again wastes the one
round of attention this gets.

1. **Add a step 0: "should this failback be happening at all?"** RND-49 says the
   most common failback is one that should never have started. Ask what triggered
   the failover, and whether it recurs.
2. **Step 2's "is the target caught up" must read `fail_counter`, not only lag**
   (§5), and must say that a just-demoted node has no row at all — which this
   project measured and which the script currently reports as `<none>`.
3. **Step 6 must name the rebuild path explicitly** (`ha_make_slavedb.sh`) and
   ask which of the OFFICE-458 problems the team hits, rather than asking the
   open-ended "how do you detect divergence".
4. **Add a ping-host question.** Given §2, the team has met at least two of the
   three states in production; which, and what did they do.

## 8. What is still genuinely unknown

The tracker says a great deal about *what goes wrong* and very little about
**what an operator decides**. Nothing found answers:

- the threshold for "caught up enough" to switch back;
- whether write traffic is quiesced first, and how;
- who authorises a failback, and on what evidence;
- whether the original master is preferred at all, or whether sites simply run
  on whichever node holds the service.

Those four remain the reason to send the script. §9 OQ8 stays open, but it is
now a narrower question asked of people who have already written down the
context.
