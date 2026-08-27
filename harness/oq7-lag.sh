#!/bin/bash
# N65 OQ7 — how is lag injected, and does the heartbeat permit it?
#
# The foundation names two candidate mechanisms and says they are not equivalent:
#   stage suspension  SIGSTOP on copylogdb or applylogdb. Precise, instant,
#                     reversible, and the ONLY one that separates the two stages
#                     the engine reports separately (util_cs.c:3893-3924).
#   network delay     tc netem on the node interface. Realistic, stage-agnostic.
# and it flags the unverified part: both processes are heartbeat-managed
# (heartbeat.h:62-70), and the heartbeat noticed a DEAD process within 10 ms in the
# CBRD-26983 session. A process that is alive but not progressing may be treated the
# same way, in which case suspension fights the engine.
#
# The observable is the slave's db_ha_apply_info sampled at 0.5 s:
#   eof_lsa_pageid    how far copylogdb has FETCHED
#   final_lsa_pageid  how far applylogdb has APPLIED
#   eof - final       the apply lag. NOTE this is blind to a copy stall, which is
#                     one of the things this run is here to show.
#
# Usage: bash oq7-lag.sh
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
NET=n65-oq7-net; N1=n65-n1; N2=n65-n2
WORK=${WORK:-$HERE/out/oq7}
export N1 N2
. "$HERE/lib.sh"

cleanup () { docker rm -f "$N1" "$N2" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; }
trap cleanup EXIT
cleanup
rm -rf "$WORK"; mkdir -p "$WORK"

echo "############ OQ7 lag ############"
cs_build || exit 2
cs_up "$NET" "$WORK" "" || { echo "!! cluster never came up"; exit 1; }

echo "== schema on the master (PK present on purpose: row replication records are"
echo "   written only while walking a PK index, locator_sr.c:8038 -- N54 C-053)"
# NOT -S. Standalone mode mounts the volumes directly and cannot open a database
# whose server is already running -- the DDL silently never happens and the load
# then fails on Unknown class "dba.t".
cs_ex "$N1" "csql -u dba -c 'CREATE TABLE t (i INT PRIMARY KEY, pad VARCHAR(200));' $DB" 2>&1 | tail -2 | sed 's/^/   /'
sleep 3
if ! cs_ex "$N1" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB" >/dev/null 2>&1; then
  echo "!! CREATE TABLE did not take on the master -- stopping"; exit 1
fi
cs_ex "$N2" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB" 2>&1 | tr -d '\r' | sed 's/^/   slave sees table t: /'

echo "== replication processes on the slave"
docker exec "$N2" ps -eo pid,args | grep -E "copylogdb|applylogdb" | grep -v grep | sed 's/^/   /'
COPY_PID=$(docker exec "$N2" ps -eo pid,args | grep copylogdb | grep -v grep | awk '{print $1}' | head -1)
APPLY_PID=$(docker exec "$N2" ps -eo pid,args | grep applylogdb | grep -v grep | awk '{print $1}' | head -1)
echo "   copylogdb=$COPY_PID  applylogdb=$APPLY_PID"

echo "== seeding t with 20k rows (setup, not measured)"
cs_ex "$N1" "python3 -c \"
import sys
w=sys.stdout.write
for i in range(20000):
    w(\\\"INSERT INTO t VALUES (%d,'%s');\\n\\\" % (i, 'x'*180))
    if i%1000==999: w('COMMIT;\\n')
w('COMMIT;\\n')
\" > /work/$N1/seed.sql; csql -u dba --no-auto-commit -i /work/$N1/seed.sql $DB 2>&1 | tail -1" 2>&1 | sed 's/^/   /'
cs_ex "$N1" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB" 2>&1 | tr -d '\r' | awk 'NF' | sed 's/^/   master rows after seed: /'

# A SUSTAINED load. The first attempt used a fixed 150k-row file which finished in
# about 40 s, so phases 2-7 measured an idle cluster and every lag read 0. This
# loop copies a 20k-row slice back into the table at a fresh offset and keeps going
# until the run drops a stop file, so write pressure lasts as long as the phases do.
cat > "$WORK/$N1/loadloop.sh" <<'LOOP'
#!/bin/bash
n=0
while [ ! -e /work/stop ]; do
  off=$(( 20000 + n * 20000 ))
  csql -u dba --no-auto-commit -c "INSERT INTO t SELECT i + $off, pad FROM t WHERE i < 20000; COMMIT;" hadb >/dev/null 2>&1
  n=$((n+1))
done
echo "batches: $n" > /work/loadloop.done
LOOP
chmod +x "$WORK/$N1/loadloop.sh"

echo "== sampler on the slave (0.5 s) + load on the master"
docker exec -d -e CUBRID="/work/$N2/cubrid" -e CUBRID_DATABASES=/db \
  -e CUBRID_CONF_FILE="/work/$N2/cubrid/conf/ha.conf" -e CUBRID_HA_CONF_FILE="/work/$N2/cubrid/conf/cubrid_ha.conf" \
  -e PATH="/work/$N2/cubrid/bin:/usr/bin:/bin" -e LD_LIBRARY_PATH="/work/$N2/cubrid/lib:/work/$N2/cubrid/cci/lib" \
  "$N2" bash -lc 'for i in $(seq 1 600); do printf "%s %s\n" "$(date +%s.%N)" "$(csql -u dba -t -N -c "SELECT eof_lsa_pageid, final_lsa_pageid, insert_counter, fail_counter FROM db_ha_apply_info" hadb 2>/dev/null | tr -s " " | tr -d " " | tr "\n" " ")"; sleep 0.5; done > /work/apply.log 2>&1'

rm -f "$WORK/stop" "$WORK/loadloop.done"
cs_ex "$N1" "cd /work/$N1 && nohup bash loadloop.sh > load.out 2>&1 &" >/dev/null 2>&1
sleep 10
B0=$(cs_ex "$N1" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB" 2>/dev/null | tr -d '\r' | awk 'NF' | tail -1)
sleep 10
B1=$(cs_ex "$N1" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB" 2>/dev/null | tr -d '\r' | awk 'NF' | tail -1)
echo "   master rows: $B0 -> $B1 over 10 s"
if [ "${B0:-0}" = "${B1:-0}" ]; then echo "!! the load is not writing -- stopping"; tail -3 "$WORK/$N1/load.out" | sed 's/^/     /'; exit 1; fi
T0=$(date +%s)
mark () { echo "$(( $(date +%s) - T0 ))s  $*" | tee -a "$WORK/marks.log"; }
lag () { docker exec -e CUBRID=/work/$N2/cubrid -e CUBRID_DATABASES=/db \
    -e CUBRID_CONF_FILE=/work/$N2/cubrid/conf/ha.conf -e CUBRID_HA_CONF_FILE=/work/$N2/cubrid/conf/cubrid_ha.conf \
    -e PATH=/work/$N2/cubrid/bin:/usr/bin:/bin -e LD_LIBRARY_PATH=/work/$N2/cubrid/lib:/work/$N2/cubrid/cci/lib \
    "$N2" bash -lc "csql -u dba -t -N -c 'SELECT eof_lsa_pageid, final_lsa_pageid, eof_lsa_pageid-final_lsa_pageid, insert_counter FROM db_ha_apply_info' $DB" 2>/dev/null | tr -d '\r' | tr '\t' ' ' | awk 'NF' | tail -1; }
hbproc () { docker exec "$N2" ps -eo pid,args 2>/dev/null | grep -E "copylogdb|applylogdb" | grep -v grep | awk '{printf "%s:%s ", $3, $1}'; echo; }

mark "PHASE 1 baseline (20 s, no injection)"; sleep 20; mark "   eof/final/lag/ins = $(lag)"

mark "PHASE 2 SIGSTOP applylogdb (pid $APPLY_PID) for 30 s"
docker exec "$N2" kill -STOP "$APPLY_PID" 2>&1 | sed 's/^/     /'
sleep 15; mark "   at +15s  eof/final/lag/ins = $(lag)"
mark "   heartbeat status on the slave:"; cs_ex "$N2" "cubrid heartbeat status 2>&1 | tail -12" 2>/dev/null | sed 's/^/     /'
sleep 15; mark "   at +30s  eof/final/lag/ins = $(lag)"
mark "   replication procs now: $(hbproc)"
mark "   heartbeat log tail:"; cs_hblog "$N2" 3 | sed 's/^/     /'

mark "PHASE 3 SIGCONT applylogdb, drain 25 s"
docker exec "$N2" kill -CONT "$APPLY_PID" 2>&1 | sed 's/^/     /'
for i in 5 10 15 20 25; do sleep 5; mark "   +${i}s  eof/final/lag/ins = $(lag)"; done

mark "PHASE 4 SIGSTOP copylogdb (pid $COPY_PID) for 30 s"
docker exec "$N2" kill -STOP "$COPY_PID" 2>&1 | sed 's/^/     /'
sleep 15; mark "   at +15s  eof/final/lag/ins = $(lag)   <-- watch the apply lag, not the truth"
# -L is the COPIED LOG path (/db/<db>_<peer>), not the database directory. Passing
# the directory yields "Can't generate the applied info due to an invalid path of
# the -L option or no related information in the db_ha_apply_info catalog table",
# which conflates a wrong path with an empty catalog.
mark "   applyinfo against the master (the only view that sees a COPY stall):"
cs_ex "$N2" "timeout 8 cubrid applyinfo -L /db/${DB}_$N1 -r $N1 -a -i 1 $DB 2>&1 | head -60" 2>/dev/null | sed 's/^/     /'
sleep 15; mark "   at +30s  eof/final/lag/ins = $(lag)"
mark "   replication procs now: $(hbproc)"

mark "PHASE 5 SIGCONT copylogdb, drain 25 s"
docker exec "$N2" kill -CONT "$COPY_PID" 2>&1 | sed 's/^/     /'
for i in 5 10 15 20 25; do sleep 5; mark "   +${i}s  eof/final/lag/ins = $(lag)"; done

mark "PHASE 6 tc netem delay 200ms on the slave's eth0 for 30 s"
docker exec -u 0 "$N2" tc qdisc add dev eth0 root netem delay 200ms 2>&1 | sed 's/^/     /'
sleep 15; mark "   at +15s  eof/final/lag/ins = $(lag)"
sleep 15; mark "   at +30s  eof/final/lag/ins = $(lag)"
docker exec -u 0 "$N2" tc qdisc del dev eth0 root 2>&1 | sed 's/^/     /'
mark "PHASE 7 netem removed, drain 30 s"; sleep 30; mark "   eof/final/lag/ins = $(lag)"

touch "$WORK/stop"; sleep 8
mark "== master row count vs slave row count"
for c in "$N1" "$N2"; do printf "   %-8s " "$c"; cs_ex "$c" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB 2>/dev/null | tr -d ' '" 2>/dev/null | tr -d '\r\n'; echo; done
cp -f "$WORK/apply.log" "$WORK/apply.captured" 2>/dev/null
echo "== artifacts: $WORK (apply.captured, marks.log)"
