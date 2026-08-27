#!/bin/bash
# N65 — drive a cluster into a failed-over state, then run failback.sh against it.
#
# Exists to make G8's "runnable" acceptance checkable: the script has to work on a
# cluster that is actually in the state it claims to fix. The setup is OQ9 arm C
# (cut the master from BOTH the peer and the ping host, so it demotes itself
# cleanly), then heal -- which measurably does NOT restore the original roles.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
NET=n65-fb-net; N1=n65-n1; N2=n65-n2; PINGHOST=n65-ping
WORK=${WORK:-$HERE/out/failback}
export N1 N2 PINGHOST
. "$HERE/lib.sh"

cleanup () { docker rm -f "$N1" "$N2" "$PINGHOST" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; }
trap cleanup EXIT
cleanup; rm -rf "$WORK"; mkdir -p "$WORK"

echo "############ failback demo ############"
cs_build || exit 2
docker network create "$NET" >/dev/null 2>&1
docker run -d --name "$PINGHOST" --hostname "$PINGHOST" --network "$NET" ubuntu:24.04 tail -f /dev/null >/dev/null
cs_up "$NET" "$WORK" "$PINGHOST" || { echo "!! cluster never came up"; exit 1; }

echo "== write something, so the failback has data to be careful about"
cs_ex "$N1" "csql -u dba -c 'CREATE TABLE t (i INT PRIMARY KEY, pad VARCHAR(50));' $DB" >/dev/null 2>&1
cs_ex "$N1" "csql -u dba -c \"INSERT INTO t VALUES (1,'a'),(2,'b'),(3,'c');\" $DB" >/dev/null 2>&1
sleep 5
for c in "$N1" "$N2"; do printf "   %-8s rows=" "$c"; cs_ex "$c" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB 2>/dev/null" 2>/dev/null | tr -d '\r' | awk 'NF' | tail -1; done

IP1=$(cs_ip "$N1" "$NET"); IP2=$(cs_ip "$N2" "$NET"); IPP=$(cs_ip "$PINGHOST" "$NET")
echo "== forcing a clean failover (OQ9 arm C): cut $N1 from the peer AND the ping host"
cs_cut "$N1" "$IP2"; cs_cut "$N2" "$IP1"; cs_cut "$N1" "$IPP"
for i in $(seq 1 30); do [ "$(cs_short_mode $N2)" = active ] && break; sleep 3; done
echo "   after $((i*3))s:  $N1=$(cs_short_mode $N1)  $N2=$(cs_short_mode $N2)"
echo "== healing the network"
cs_heal "$N1" "$IP2"; cs_heal "$N2" "$IP1"; cs_heal "$N1" "$IPP"
sleep 30
echo "   30 s after heal: $N1=$(cs_short_mode $N1)  $N2=$(cs_short_mode $N2)   <-- still swapped, and stays that way"

echo
echo "############ running failback.sh ############"
bash "$HERE/failback.sh" --db "$DB" --current "$N2" --target "$N1" --auto
RC=$?
echo
echo "== final state: $N1=$(cs_short_mode $N1)  $N2=$(cs_short_mode $N2)   (failback.sh rc=$RC)"
for c in "$N1" "$N2"; do printf "   %-8s rows=" "$c"; cs_ex "$c" "csql -u dba -t -N -c 'SELECT count(*) FROM t' $DB 2>/dev/null" 2>/dev/null | tr -d '\r' | awk 'NF' | tail -1; done
exit $RC
