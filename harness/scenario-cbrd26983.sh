#!/bin/bash
# The CBRD-26983 scenario set, through csb verbs only.
#
# The question that started this project: can a failover and a failback hand out
# an AUTO_INCREMENT value the other node already issued? Answering it by hand in
# August took a two-node cluster assembled step by step. This is the same
# scenario expressed as five verbs, and its acceptance is the id sequence the
# original session measured -- 1, 2, 21, 22, 41, 42, 61 -- which is what a serial
# cache of 20 produces when the role changes three times.
set -uo pipefail
CSB=${CSB:-./bin/csb}
ENGINE=${ENGINE:-/data/workspace/for-plan/importdb/cubrid/install.out}
NAME=${NAME:-cbrd}
export CSB_CLUSTER=$NAME
DB=$NAME

sql () { $CSB node exec master -- "csql -u dba -t -N -c \"$1\" $DB" --timeout 60s 2>/dev/null | tr -d '\r' | grep -vE 'NOTIFICATION|boot_cl|Program|^$'; }
role () { $CSB ha status --json --timeout 30s 2>/dev/null | python3 -c 'import json,sys
d=json.load(sys.stdin)["data"]["nodes"]
print(" ".join(n["name"].split("-")[-1]+"="+((n["role"] or "-") if n["live"] else "dead") for n in d))'; }
wait_master () { for _ in $(seq 1 20); do sleep 3; $CSB ha status --json --timeout 30s 2>/dev/null | grep -q '"role": *"active"' && return 0; done; return 1; }

echo "== 0/5  start from nothing (the scenario asserts an id sequence, so it needs a fresh serial)"
$CSB cluster destroy --cluster "$NAME" --purge --timeout 180s >/dev/null 2>&1

echo "== 1/5  build the cluster"
$CSB cluster create --name "$NAME" --build "$ENGINE" --timeout 600s | tail -1

echo "== 2/5  a table with an AUTO_INCREMENT, and two rows"
sql "CREATE TABLE t(i INT AUTO_INCREMENT PRIMARY KEY, node VARCHAR(20));" >/dev/null
for _ in 1 2; do sql "INSERT INTO t(node) VALUES('phase1');" >/dev/null; done
echo "   ids so far: $(sql 'SELECT i FROM t ORDER BY i' | tr -d ' ' | tr '\n' ' ')"

phase=2
for round in 1 2 3; do
  old=$($CSB node exec master -- hostname --timeout 30s 2>/dev/null | tr -d '\r\n')
  echo "== 3/5  role change $round: kill $old"
  $CSB node kill master --timeout 60s >/dev/null
  wait_master || { echo "   !! no master after the kill"; break; }
  echo "   roles now: $(role)"

  n=2; [ $round = 3 ] && n=1
  for _ in $(seq 1 $n); do sql "INSERT INTO t(node) VALUES('phase$((phase+1))');" >/dev/null; done
  phase=$((phase+1))
  echo "   ids so far: $(sql 'SELECT i FROM t ORDER BY i' | tr -d ' ' | tr '\n' ' ')"

  if [ $round -lt 3 ]; then
    echo "   bringing $old back so the next role change has somewhere to go"
    $CSB node start "$old" --timeout 120s >/dev/null; sleep 10
  fi
done

echo "== 4/5  the sequence"
got=$(sql 'SELECT i FROM t ORDER BY i' | tr -d ' ' | tr '\n' ' ' | sed 's/ *$//')
want="1 2 21 22 41 42 61"
echo "   measured: $got"
echo "   expected: $want"
[ "$got" = "$want" ] && echo "   ✓ reproduced" || echo "   ✗ differs"

echo "== 5/5  what the record says about it"
$CSB record export --out /tmp/cbrd-run.json --timeout 90s
python3 - <<'PY'
import json
d=json.load(open('/tmp/cbrd-run.json'))
print("   role changes:", len(d["role_changes"]))
for rc in d["role_changes"]:
    print(f'     {rc["node"]}  measured={rc.get("measured")}  predicted={rc["predicted"]}  trigger={rc.get("trigger")}')
PY
