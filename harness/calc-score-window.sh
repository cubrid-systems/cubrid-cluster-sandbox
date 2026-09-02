#!/bin/bash
# Is there an Active-Active window after the network heals?
#
# The third split-brain flavour in docs/design/04-faults.md §5 is the only row
# of that table this project has never reproduced. It comes from the field's own
# hidden-parameter test, which reports that with ha_calc_score_interval_in_msecs
# raised, a cluster whose slave was promoted during a partition runs
# Active-Active for the length of that interval ONCE THE NETWORK HEALS, with
# data syncing both ways -- and separately, a master describing itself as
# `to-be-master`. Both are recorded there as 특이사항 in a test that could not
# tell an engine behaviour from a test artefact, which is why that ticket has
# been open since 2022.
#
# The claim is about the window AFTER the heal, so that is what this measures.
# Every second for --window seconds after `fault clear`:
#
#   does this node accept a write        the Active-Active claim
#   does it hold the other node's row    the both-ways-sync claim
#   what does changemode call it         the `to-be-master` claim
#
# Two arms, because a window that also exists at the default interval is not
# about the parameter:
#
#   baseline  ha_calc_score_interval_in_msecs left at its default (3000)
#   raised    ha_calc_score_interval_in_msecs=15000
#
# Repeat it. This project has already published one effect from a single sample
# and had to shrink it on repetition (findings/switchover-threshold.md).
set -uo pipefail
CSB=${CSB:-./bin/csb}
ENGINE=${ENGINE:-/data/workspace/for-plan/importdb/cubrid/install.out}
OUT=${OUT:-harness/results/calc-score-window.tsv}
WINDOW=${WINDOW:-40}
REPEATS=${REPEATS:-3}
RAISED=${RAISED:-15000}

mkdir -p "$(dirname "$OUT")"
[ -s "$OUT" ] || printf 'arm\trun\tmasters\tboth_write_s\tn1_saw_n2\tn2_saw_n1\tto_be_master\tsettled_s\troles_after\n' > "$OUT"

# sql runs one statement on one node. stderr is merged, because "this node
# refused the write" is the measurement and it arrives on stderr.
sql () { $CSB node exec "$1" --timeout 60s -- "csql -u dba -t -N -c \"$2\" $CSB_CLUSTER 2>&1"; }

# wrote returns 0 when the node accepted an INSERT. A standby and a to_be_active
# node both refuse one, which is exactly the line this experiment is drawn on.
wrote () {
  local node=$1 key=$2
  local out; out=$(sql "$node" "INSERT INTO w VALUES ($key, '$node')")
  case "$out" in
    *ERROR*) return 1 ;;
  esac
  return 0
}

# count reads one number and nothing else.
#
# The first draft merged stderr into this and matched the output against *1*.
# csql writes a NOTIFICATION line carrying a pid and a port, so any digit
# anywhere read as "the row is there" -- a check that answers yes to a question
# it never asked. The whole both-ways-sync claim would have rested on it.
count () {
  local node=$1 key=$2 out
  out=$($CSB node exec "$node" --timeout 60s -- \
    "csql -u dba -t -N -c \"SELECT count(*) FROM w WHERE i=$key\" $CSB_CLUSTER" 2>/dev/null)
  echo "$out" | tr -d '\r' | grep -E '^[[:space:]]*[0-9]+[[:space:]]*$' | tr -d '[:space:]' | tail -1
}

has_row () {
  local n; n=$(count "$1" "$2")
  [ -n "$n" ] && [ "$n" -ge 1 ]
}

run_arm () {
  local arm=$1 runno=$2
  local name="csw$(date +%s%N | tail -c 6)"
  export CSB_CLUSTER=$name
  echo "== arm $arm run $runno ($name)"

  local setflag=()
  [ "$arm" = raised ] && setflag=(--set-hidden "ha_calc_score_interval_in_msecs=$RAISED")
  if ! $CSB cluster create --name "$name" --build "$ENGINE" "${setflag[@]}" --timeout 900s >/dev/null 2>&1; then
    echo "   !! create failed"; return 1
  fi
  local n1 n2
  n1=$($CSB node exec master -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')
  n2=$($CSB node exec slave  -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')

  sql "$n1" "CREATE TABLE w (i INT PRIMARY KEY, who VARCHAR(32))" >/dev/null 2>&1
  sql "$n1" "INSERT INTO w VALUES (1, 'before')" >/dev/null 2>&1
  sleep 5

  # The split, and its flavour named by the tool rather than assumed.
  local masters
  masters=$($CSB fault splitbrain --json --timeout 120s 2>/dev/null |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["masters"])' 2>/dev/null)
  masters=${masters:-0}
  if [ "$masters" -lt 2 ]; then
    echo "   no split brain (masters=$masters); nothing to heal"
    printf '%s\t%s\t%s\t-\t-\t-\t-\t-\tno split brain\n' "$arm" "$runno" "$masters" >> "$OUT"
    $CSB cluster destroy --cluster "$name" --purge --timeout 300s >/dev/null 2>&1
    return 0
  fi

  # One distinct row per side, so "synced both ways" is a fact about rows and
  # not an impression about roles.
  sql "$n1" "INSERT INTO w VALUES (101, '$n1')" >/dev/null 2>&1
  sql "$n2" "INSERT INTO w VALUES (201, '$n2')" >/dev/null 2>&1

  $CSB fault clear --timeout 120s >/dev/null 2>&1
  local t0=$SECONDS both=0 settled="" tbm=no key=1000
  while [ $((SECONDS - t0)) -lt "$WINDOW" ]; do
    local el=$((SECONDS - t0)) a=0 b=0
    key=$((key + 1)); wrote "$n1" "$key" && a=1
    key=$((key + 1)); wrote "$n2" "$key" && b=1
    if [ "$a" = 1 ] && [ "$b" = 1 ]; then
      both=$el
    elif [ -z "$settled" ]; then
      settled=$el
    fi
    case "$($CSB node exec all -- "cubrid changemode $name" --timeout 30s 2>/dev/null)" in
      *to-be-master*|*to_be_master*) tbm=yes ;;
    esac
    sleep 1
  done

  # Each side's row on the other side. 101 was written on n1 and 201 on n2
  # while neither could reach the other, so either one appearing across the
  # partition is replication that ran after the heal.
  local saw12=no saw21=no
  has_row "$n1" 201 && saw12=yes
  has_row "$n2" 101 && saw21=yes
  local roles; roles=$($CSB ha status --timeout 60s 2>/dev/null | awk 'NF{printf "%s=%s ", $1, $3}')

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$arm" "$runno" "$masters" "$both" "$saw12" "$saw21" "$tbm" "${settled:-never}" "${roles% }" >> "$OUT"
  echo "   masters=$masters both_write_s=$both n1_saw_n2=$saw12 n2_saw_n1=$saw21 to_be_master=$tbm settled=${settled:-never}"
  echo "   roles: $roles"

  $CSB cluster destroy --cluster "$name" --purge --timeout 300s >/dev/null 2>&1
}

for i in $(seq 1 "$REPEATS"); do
  run_arm baseline "$i"
  run_arm raised   "$i"
done
echo
column -t -s $'\t' "$OUT"
