#!/usr/bin/env python3
"""csb load driver. Runs inside a node; stdlib only.

The contract this exists for: state a rate, hold it, and report honestly when it
could not. A driver that silently falls behind does not merely under-load the
cluster -- it makes every figure measured beside it a figure about the driver.
"""
import array
import json, os, random, subprocess, sys, threading, time

spec = json.load(open(sys.argv[1]))
status_path = sys.argv[2]

profile = spec["profile"]
rate = float(spec.get("rate") or 0)          # 0 means "as fast as it goes"
workers = int(spec.get("concurrency") or 1)
# Where this driver's workers sit in the global interleave, and how wide that
# interleave is. With one client they are 0 and `workers`, which is what the
# single-driver arithmetic below already did. With several clients each takes a
# disjoint slice, so two drivers writing the same table never choose the same
# primary key -- which would not be a load, it would be a benchmark of the
# engine's error path.
# Each driver writes its own RANGE of the key space, and finds where to resume
# inside it. An interleave was tried first and was wrong: every driver read
# MAX(i) at a different moment, so their offsets were relative to different
# origins and they collided anyway -- 146 unique-constraint violations out of
# 1025 statements, on the first run with two clients. A range needs no
# coordination and survives a restart.
key_lo = int(spec.get("key_lo") or 0)
duration = float(spec.get("for_seconds") or 0)
table = spec.get("table") or "csb_load"
db = spec["db"]
seed = int(spec.get("seed") or 42)
batch = max(1, int(spec.get("batch") or 1))

state = {"sent": 0, "errors": 0, "started": time.time(), "last_error": "", "lat_dropped": 0}

# Statement latencies in milliseconds. array('f') is four bytes a sample, so the
# cap is about four megabytes -- past it the samples stop being complete and the
# report says so rather than quietly becoming an estimate.
lat = array.array("f")
LAT_CAP = 1000000
lock = threading.Lock()
stop = threading.Event()


def csql(sql, timeout=60):
    p = subprocess.run(["csql", "-u", "dba", "-t", "-N", "-c", sql, db],
                       capture_output=True, text=True, timeout=timeout)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def prepare():
    """Create the table if it is not there, and find where to start writing.

    Keys continue from the table's current maximum rather than from a fixed
    base. Restarting the driver against a table it has written before otherwise
    replays the same keys and every statement fails on the primary key -- which
    is not a load, it is a benchmark of the engine's error path.
    """
    if profile in ("insert", "update", "mixed"):
        csql("CREATE TABLE %s (i INT PRIMARY KEY, pad VARCHAR(200));" % table)
    if profile in ("update", "mixed"):
        # update needs rows to update; seed a deterministic block of them
        rows = ",".join("(%d,'%s')" % (n, "x" * 180) for n in range(1000))
        csql("INSERT INTO %s VALUES %s;" % (table, rows))
    rc, out = csql("SELECT NVL(MAX(i),%d) FROM %s WHERE i >= %d;" % (key_lo, table, key_lo))
    if rc == 0:
        for tok in out.split():
            if tok.strip().lstrip("-").isdigit():
                return max(int(tok.strip()) + 1, key_lo + 1)
    return key_lo + 1


def db_statement(rnd, n):
    pad = "x" * 180
    if profile == "insert":
        # One statement, `batch` rows. The rate contract counts statements; rows
        # per second is rate x batch, and both are reported, because a driver
        # that conflates them cannot say which one it failed to hold.
        vals = ",".join("(%d,'%s')" % (n + k * workers * 1000003, pad) for k in range(batch))
        return "INSERT INTO %s VALUES %s;" % (table, vals)
    if profile == "update":
        return "UPDATE %s SET pad='%s' WHERE i=%d;" % (table, pad, rnd.randrange(1000))
    # mixed: the shape a real application has, in stated proportions
    r = rnd.random()
    if r < 0.6:
        return "INSERT INTO %s VALUES (%d,'%s');" % (table, n, pad)
    if r < 0.9:
        return "UPDATE %s SET pad='%s' WHERE i=%d;" % (table, pad, rnd.randrange(1000))
    return "DELETE FROM %s WHERE i=%d;" % (table, rnd.randrange(1000))


def db_worker(wid, base):
    rnd = random.Random(seed + wid)
    # Per-worker pacing. This is rate CONTROL, not a simulated delay: the target
    # is divided by the workers and each one waits that long between statements.
    # Network latency is a different thing with a different verb (fault lag
    # --mechanism delay, which puts netem on the interface).
    per = (rate / workers) if rate else 0
    interval = (1.0 / per) if per else 0
    # Interleaved by worker across every driver, so the workers never collide
    # with each other, the clients never collide with each other, and the run
    # never collides with what a previous run left behind.
    n = base + wid - workers
    nxt = time.time()
    while not stop.is_set():
        n += workers
        t0 = time.time()
        rc, out = csql(db_statement(rnd, n))
        ms = (time.time() - t0) * 1000.0
        with lock:
            if rc == 0:
                state["sent"] += 1
                # Every latency is kept, not sampled, so the percentiles are the
                # real ones rather than an estimate -- up to a cap, past which
                # the report says the distribution is a sample and stops
                # pretending otherwise. Four bytes each: a million statements is
                # four megabytes.
                if len(lat) < LAT_CAP:
                    lat.append(ms)
                else:
                    state["lat_dropped"] += 1
            else:
                state["errors"] += 1
                state["last_error"] = out.strip().splitlines()[-1][:200] if out.strip() else "rc=%d" % rc
        if interval:
            nxt += interval
            delay = nxt - time.time()
            if delay > 0:
                stop.wait(delay)
            else:
                nxt = time.time()  # behind: do not build a debt we cannot repay


def host_cpu_worker(wid):
    x = 0
    while not stop.is_set():
        for _ in range(200000):
            x = (x * 1103515245 + 12345) & 0x7FFFFFFF
        with lock:
            state["sent"] += 1


def host_io_worker(wid):
    path = "/db/.csb_load_io_%d" % wid
    buf = os.urandom(1 << 20)
    while not stop.is_set():
        with open(path, "wb") as f:
            for _ in range(16):
                f.write(buf)
                if stop.is_set():
                    break
            f.flush()
            os.fsync(f.fileno())
        with lock:
            state["sent"] += 1
    try:
        os.unlink(path)
    except OSError:
        pass


def percentiles(samples):
    """The distribution, or nothing at all.

    A percentile from three samples is not a percentile, and reporting one would
    be the same class of lie as a lag figure with no source. Below a floor the
    fields are absent rather than misleading.
    """
    n = len(samples)
    if n < 20:
        return None
    xs = sorted(samples)

    def at(q):
        # Nearest-rank, which is exact for the sample and does not invent a value
        # between two measurements.
        k = int(round(q * (n - 1)))
        return round(xs[k], 2)

    return {"count": n, "p50_ms": at(0.50), "p90_ms": at(0.90), "p99_ms": at(0.99),
            "min_ms": round(xs[0], 2), "max_ms": round(xs[-1], 2)}


def report():
    while True:
        now = time.time()
        with lock:
            sent, errors, err = state["sent"], state["errors"], state["last_error"]
        elapsed = max(now - state["started"], 1e-6)
        achieved = sent / elapsed
        with lock:
            snapshot = list(lat)
            dropped = state["lat_dropped"]
        dist = percentiles(snapshot)
        held = True if not rate else achieved >= rate * 0.95
        doc = {
            "profile": profile, "kind": "host" if profile.startswith("host-") else "db",
            "requested": (("%g/s" % rate) if rate else "max"),
            "achieved": "%.1f/s" % achieved,
            "achieved_value": round(achieved, 2), "requested_value": rate,
            "held": held, "sent": sent, "errors": errors, "last_error": err,
            "batch": batch, "rows_per_second": round(achieved * batch, 1) if profile != "update" else None,
            "elapsed_s": round(elapsed, 1), "concurrency": workers, "seed": seed,
            "running": not stop.is_set(),
            "driver_cost": driver_cost(),
            # Per STATEMENT, not per row: with --batch the statement carries many
            # rows and the two are different questions. It is also the latency of
            # a csql invocation, which includes starting the client -- that cost
            # is real for this driver and is reported separately as driver_cost
            # rather than subtracted from a number somebody might quote.
            "latency": dist,
            "latency_complete": dropped == 0,
        }
        tmp = status_path + ".tmp"
        with open(tmp, "w") as f:
            json.dump(doc, f, indent=2)
        os.replace(tmp, status_path)
        if stop.is_set():
            return
        stop.wait(1.0)


def driver_cost():
    # The driver consumes the resources it is measuring. Report that rather than
    # pretend otherwise (docs/design/06-load.md §6).
    try:
        with open("/proc/self/stat") as f:
            parts = f.read().split()
        ticks = int(parts[13]) + int(parts[14])
        hz = os.sysconf("SC_CLK_TCK")
        return {"cpu_seconds": round(ticks / hz, 2)}
    except Exception:
        return {}


def main():
    base = 1
    if profile in ("insert", "update", "mixed"):
        base = prepare()
    fn = {"insert": db_worker, "update": db_worker, "mixed": db_worker,
          "host-cpu": host_cpu_worker, "host-io": host_io_worker}[profile]
    args = (lambda i: (i, base)) if fn is db_worker else (lambda i: (i,))

    threads = [threading.Thread(target=fn, args=args(i), daemon=True) for i in range(workers)]
    rep = threading.Thread(target=report, daemon=True)
    rep.start()
    for t in threads:
        t.start()

    try:
        if duration:
            stop.wait(duration)
        else:
            while not stop.is_set():
                stop.wait(3600)
    except KeyboardInterrupt:
        pass
    stop.set()
    for t in threads:
        t.join(timeout=10)
    rep.join(timeout=5)


if __name__ == "__main__":
    main()
