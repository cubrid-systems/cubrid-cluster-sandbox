#!/bin/sh
# An example loader. Replace it.
#
# It exists so that trying this project out does not require writing one first,
# and so that you can see the whole path in forty lines: your program lives in
# /tools, runs on a client node with `node exec`, talks to the database over the
# cluster's own network, and writes where the output outlives the cluster.
#
#   sh /tools/example.sh <db> <master-node> [rows] [per-second] [rows-per-statement]
#
# Whatever you write instead -- python, a jar, sysbench, loaddb over a dump you
# put in /tools -- goes in by the same route and needs nothing from us.
set -eu
DB=$1
MASTER=$2
ROWS=${3:-500}
RATE=${4:-20}
BATCH=${5:-1}

TARGET="$DB@$MASTER"
PAUSE=$(awk "BEGIN{printf \"%.3f\", 1/$RATE}")
PAD=$(printf 'x%.0s' $(seq 1 180))

csql -u dba -c "CREATE TABLE example (i INT PRIMARY KEY, pad VARCHAR(200))" "$TARGET" >/dev/null 2>&1 || true

# Continue from whatever is there, so running this twice is not a benchmark of
# the engine's primary-key error path.
# Take the line that is ONLY digits. csql prints a NOTIFICATION carrying a pid
# and a port, and scraping every digit out of its output gives you those too --
# which is how this script first "loaded" rows at key 8752931523.
FROM=$(csql -u dba -t -N -c "SELECT NVL(MAX(i),0)+1 FROM example" "$TARGET" 2>/dev/null \
       | awk '/^[0-9]+$/ { n = $1 } END { print (n ? n : 1) }')

echo "loading $ROWS rows from $FROM at about $RATE/s into $TARGET"
i=$FROM
END=$((FROM + ROWS))
OK=0
while [ "$i" -lt "$END" ]; do
  VALUES=""
  k=0
  while [ "$k" -lt "$BATCH" ] && [ "$((i + k))" -lt "$END" ]; do
    [ -n "$VALUES" ] && VALUES="$VALUES,"
    VALUES="$VALUES($((i + k)),'$PAD')"
    k=$((k + 1))
  done
  if csql -u dba -c "INSERT INTO example VALUES $VALUES" "$TARGET" >/dev/null 2>&1; then
    OK=$((OK + k))
  fi
  i=$((i + k))
  sleep "$PAUSE"
done

printf '%s rows=%s ok=%s\n' "$(date -u +%FT%TZ)" "$ROWS" "$OK" >> /results/example.log
echo "$OK row(s) in; appended to /results/example.log"
