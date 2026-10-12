#!/usr/bin/env python3
"""Whole-suite scorer, written by sleipnir taskgen (internal/rl/env/taskgen/suite.go) as a hidden verifier file.

It runs the test commands of spec.json (next to this file) from the directory it is started in, works out which tests
passed from each command's report, and prints the score as the last line of standard output:

    {"score": s, "units": {"<unit>": {"passed": n, "total": m, "weight": w}}, "guards": {"kept": k, "total": g}}

score = (sum over units of weight * passed/total, divided by the sum of the weights) * (kept guards / guards), and a
test that no report mentions has failed. Each command runs in a session of its own with standard input closed and its
output in private files, and every process still in its process group is killed before the next command runs, so
nothing the code under test prints, now or later, reaches standard output, where this script writes one line, last.

go test stops a package's tests at the first one that crashes the test binary (a panic, a fatal error), and the tests
after it would go unreported. A go test command is therefore run again for each package that crashed, through
SUITE_ARGS (-skip the tests the package already reported, and -run the sample in progress mode, then the package), until
a run reports nothing new, the package stops crashing, MAX_CRASH_RERUNS runs were made or the deadline passes. A crash
that go test attributes to a test costs that test, not every test after it; one it attributes to no test (a panic in
another goroutine) is hit again by the rerun, which reports nothing new, and the tests after it stay unreported.

Environment:
    SUITE_MODE=record  print {"suite_record": 1, "commands": [...]} instead of a score: every test each command
                       reported, with pass, fail or skip (units and guards are not needed)
    SUITE_SAMPLE=N     progress mode: score at most N tests of each unit and N guards, chosen by a hash of the test id,
                       and pass each command the selection in SUITE_ARGS (shell words) so it can run only those
    SUITE_BUDGET_S=S   a deadline in seconds, used when it is shorter than the spec's deadline_s
Commands get SUITE_TMP (a private directory), SUITE_DIR (this file's directory) and SUITE_REPORT (where a junit command
writes its report). Python 3.8 or later, standard library only.
"""
import hashlib
import json
import math
import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import xml.etree.ElementTree as ET
from fractions import Fraction

PASS, FAIL, SKIP = "pass", "fail", "skip"
PARSERS = ("gotest-json", "junit", "cargo")
DEFAULT_DEADLINE_S = 1800
MAX_FAILURE_LINES = 200
MAX_CRASH_RERUNS = 40
TAIL_BYTES = 3000
RANK = {PASS: 0, SKIP: 1, FAIL: 2}

# The process group of the command running now, so a SIGTERM to the scorer takes it down too.
CURRENT = [None]


def log(msg):
    sys.stderr.write(msg + "\n")


def merge(results, test_id, outcome):
    """Records one report of a test. A test passes only when every report of it says so: fail beats skip beats pass."""
    prev = results.get(test_id)
    if prev is None or RANK[outcome] > RANK[prev]:
        results[test_id] = outcome


# ---- parsers ----

def parse_gotest(path, results, crashed=None):
    """go test -json: the pass, fail and skip events of top-level tests, as <import path>.<TestName>. Output events
    (everything a test prints, including lines that look like test2json's own) are not results; a line of a package's
    output that starts with "panic: " or "fatal error: " adds the package to crashed, when given."""
    with open(path, "rb") as f:
        for raw in f:
            if not raw.startswith(b"{"):
                continue
            try:
                ev = json.loads(raw.decode("utf-8", "replace"))
            except ValueError:
                continue
            if not isinstance(ev, dict):
                continue
            action, test, pkg = ev.get("Action"), ev.get("Test"), ev.get("Package")
            if action == "output" and crashed is not None and isinstance(pkg, str) and pkg:
                out = ev.get("Output")
                if isinstance(out, str) and (out.startswith("panic: ") or out.startswith("fatal error: ")):
                    crashed.add(pkg)
                continue
            if action not in (PASS, FAIL, SKIP) or not isinstance(test, str) or not isinstance(pkg, str):
                continue
            if not test or not pkg or "/" in test:
                continue
            merge(results, pkg + "." + test, action)


def local_name(tag):
    return tag.rsplit("}", 1)[-1] if isinstance(tag, str) else ""


def junit_id(classname, name, file):
    """The test id of a junit testcase: file::Class::name when the classname is the file's module (as pytest writes
    it with junit_family=xunit1, which is pytest's node id), else classname::name."""
    if file:
        f = file.replace("\\", "/")
        mod = (f[:-3] if f.endswith(".py") else f).replace("/", ".")
        if classname == mod:
            return f + "::" + name
        if classname.startswith(mod + "."):
            return f + "::" + classname[len(mod) + 1:].replace(".", "::") + "::" + name
    if classname:
        return classname + "::" + name
    return name


def parse_junit(path, results):
    """A junit XML report: a testcase with a failure or error child failed, one with a skipped child was skipped."""
    if not os.path.isfile(path):
        return
    try:
        for _, el in ET.iterparse(path, events=("end",)):
            if local_name(el.tag) != "testcase":
                continue
            name = el.get("name") or ""
            if name:
                outcome = PASS
                for child in el:
                    t = local_name(child.tag)
                    if t in ("failure", "error"):
                        outcome = FAIL
                    elif t == "skipped" and outcome == PASS:
                        outcome = SKIP
                merge(results, junit_id(el.get("classname") or "", name, el.get("file") or ""), outcome)
            el.clear()
    except (ET.ParseError, OSError, ValueError) as e:
        log("suite: the junit report %s is unreadable: %s" % (os.path.basename(path), e))


CARGO_RUNNING = re.compile(r"^\s+Running (?:unittests )?(\S+)(?: \((\S+)\))?\s*$")
CARGO_DOCTESTS = re.compile(r"^\s+Doc-tests \S+")
CARGO_TEST = re.compile(r"^test (\S+) \.\.\. (ok|FAILED|ignored)(?:,.*)?$")
CARGO_HASH = re.compile(r"-[0-9a-f]{16}$")


def cargo_target(src, binary):
    """Names a test binary of cargo's: the target's name, with its source file when that is neither the library nor
    an integration test (a binary's unit tests share the crate's name)."""
    name = binary.replace("\\", "/").rsplit("/", 1)[-1]
    if name.endswith(".exe"):
        name = name[:-4]
    name = CARGO_HASH.sub("", name)
    if src and not src.endswith("src/lib.rs") and not src.startswith("tests/"):
        name += "@" + src
    return name


CARGO_OUTCOME = {"ok": PASS, "FAILED": FAIL, "ignored": SKIP}


def parse_cargo(path, results):
    """cargo test output (both streams, in order): the result lines of each test binary as <target>::<test path>.
    A binary's results are read from its "running N tests" line up to "failures:" or its end ("test result:", or
    cargo's "error: test failed" when it crashed), so what a failing test printed, which cargo shows under failures:,
    is never read as a result, and a target header is only taken between two binaries. Doc-tests are skipped."""
    target, state = None, "between"
    with open(path, "rb") as f:
        for raw in f:
            line = raw.decode("utf-8", "replace").rstrip("\r\n")
            if state in ("between", "header"):
                m = CARGO_RUNNING.match(line)
                if m:
                    src, binary = (m.group(1), m.group(2)) if m.group(2) else (None, m.group(1))
                    target, state = cargo_target(src, binary), "header"
                elif CARGO_DOCTESTS.match(line):
                    target, state = None, "header"
                elif state == "header" and line.startswith("running "):
                    state = "results"
                continue
            if line.startswith("test result: ") or line.startswith("error: test failed"):
                target, state = None, "between"
            elif state == "results" and line == "failures:":
                state = "failures"
            elif state == "results" and target is not None:
                m = CARGO_TEST.match(line)
                if m:
                    merge(results, target + "::" + m.group(1), CARGO_OUTCOME[m.group(2)])


# ---- running ----

def kill_group(pgid):
    try:
        os.killpg(pgid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError, OSError):
        pass


def wait_exit(p, until):
    """Waits until p exits or until passes (True: it timed out). The child is not reaped, so its process id, which
    names its process group, cannot be reused before the group is killed."""
    while True:
        if hasattr(os, "waitid"):
            try:
                if os.waitid(os.P_PID, p.pid, os.WEXITED | os.WNOWAIT | os.WNOHANG) is not None:
                    return False
            except ChildProcessError:
                return False
        elif p.poll() is not None:
            return False
        if time.monotonic() >= until:
            return True
        time.sleep(0.05)


def tail(paths):
    out = b""
    for p in paths:
        try:
            with open(p, "rb") as f:
                f.seek(0, 2)
                n = f.tell()
                f.seek(max(0, n - TAIL_BYTES))
                out += f.read()
        except OSError:
            pass
    return out[-TAIL_BYTES:].decode("utf-8", "replace")


def exec_command(cmd, tag, tmp, project, deadline, base_env, args):
    """Runs cmd once, its output in the files of tag, with SUITE_ARGS set to args when it is not None. Returns
    (exit status, timed out, seconds, report path, output path, error path), or a note when it could not run."""
    report = os.path.join(tmp, "junit-%s.xml" % tag)
    out_path = os.path.join(tmp, "out-%s.log" % tag)
    err_path = os.path.join(tmp, "err-%s.log" % tag)
    for p in (report, out_path, err_path):
        try:
            os.unlink(p)
        except OSError:
            pass
    remaining = deadline - time.monotonic()
    limit = cmd.get("timeout_s") or remaining
    limit = min(limit, remaining)
    if limit <= 0:
        return None, "not run: the deadline passed"
    env = dict(base_env)
    env["SUITE_REPORT"] = report
    if args is not None:
        env["SUITE_ARGS"] = args
    cwd = os.path.join(project, cmd.get("cwd") or ".")
    merged = cmd["parser"] == "cargo"  # cargo's target headers (stderr) must stay in order with the results (stdout)
    started = time.monotonic()
    with open(out_path, "wb") as out, open(err_path, "wb") as err:
        try:
            p = subprocess.Popen(cmd["run"], shell=True, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                 stdout=out, stderr=out if merged else err, start_new_session=True)
        except OSError as e:
            return None, "could not start: %s" % e
        CURRENT[0] = p.pid
        timed_out = wait_exit(p, started + limit)
        kill_group(p.pid)
        p.wait()
        CURRENT[0] = None
    return (p.returncode, timed_out, round(time.monotonic() - started, 3), report, out_path, err_path), None


def go_rerun_args(pkg, skip, chosen):
    """SUITE_ARGS that run package pkg again without the tests skip (names), and only the sampled tests of chosen."""
    words = []
    if chosen is not None:
        names = sorted({t.rsplit(".", 1)[1] for t in chosen if "." in t and t.rsplit(".", 1)[0] == pkg})
        words += ["-run", shlex.quote("^(" + "|".join(names) + ")$")]
    words += ["-skip", shlex.quote("^(" + "|".join(sorted(skip)) + ")$"), shlex.quote(pkg)]
    return " ".join(words)


def rerun_crashed(i, cmd, tmp, project, deadline, base_env, results, crashed, chosen):
    """Runs each package of crashed again without the tests it already reported (see the module's doc), merging what
    the reruns report into results. Returns how many reruns were made."""
    runs, pending = 0, sorted(crashed)
    while pending and runs < MAX_CRASH_RERUNS:
        pkg = pending.pop(0)
        reported = {t.rsplit(".", 1)[1] for t in results if t.rsplit(".", 1)[0] == pkg}
        if not reported:
            continue  # it crashed before any test reported: a rerun would crash at the same place
        runs += 1
        ran, note = exec_command(cmd, "%dr%d" % (i, runs), tmp, project, deadline, base_env, go_rerun_args(pkg, reported, chosen))
        if ran is None:
            log("suite: %s: rerun of %s %s" % (cmd["name"], pkg, note))
            break
        sub, again = {}, set()
        try:
            parse_gotest(ran[4], sub, again)
        except (OSError, ValueError) as e:
            log("suite: %s: the report of the rerun of %s is unreadable: %s" % (cmd["name"], pkg, e))
            continue
        new = [t for t in sub if t not in results and t.rsplit(".", 1)[0] == pkg]
        for t in new:
            merge(results, t, sub[t])
        log("suite: %s: %s crashed; run again without its %d reported tests: %d more reported%s" % (
            cmd["name"], pkg, len(reported), len(new), ", and it crashed again" if pkg in again else ""))
        if new and pkg in again:
            pending.append(pkg)
    return runs


def run_command(i, cmd, tmp, project, deadline, base_env, args, chosen=None):
    """Runs one command and returns what it reported: {"name", "parser", "exit", "timed_out", "seconds", "tests"}.
    A go test command is run again for the packages that crashed (rerun_crashed); chosen are its sampled tests in
    progress mode."""
    info = {"name": cmd["name"], "parser": cmd["parser"], "exit": None, "timed_out": False, "seconds": 0.0, "tests": {}}
    ran, note = exec_command(cmd, str(i), tmp, project, deadline, base_env, args)
    if ran is None:
        info["note"] = note
        log("suite: %s: %s" % (cmd["name"], note.replace("not run: the deadline passed", "not run, the deadline passed")))
        return info
    code, timed_out, seconds, report, out_path, err_path = ran
    info["exit"], info["timed_out"], info["seconds"] = code, timed_out, seconds
    results = info["tests"]
    crashed = set()
    try:
        if cmd["parser"] == "junit":
            parse_junit(report, results)
        elif cmd["parser"] == "cargo":
            parse_cargo(out_path, results)
        else:
            parse_gotest(out_path, results, crashed)
    except (OSError, ValueError) as e:
        log("suite: %s: the report is unreadable: %s" % (cmd["name"], e))
    if crashed and not timed_out:
        info["reruns"] = rerun_crashed(i, cmd, tmp, project, deadline, base_env, results, crashed, chosen)
    counts = {PASS: 0, FAIL: 0, SKIP: 0}
    for v in results.values():
        counts[v] += 1
    log("suite: %s: exit %s%s after %.1fs, %d tests reported (%d passed, %d failed, %d skipped)" % (
        cmd["name"], code, " (timed out)" if timed_out else "", info["seconds"], len(results),
        counts[PASS], counts[FAIL], counts[SKIP]))
    if timed_out or (code != 0 and not results):
        for line in tail([err_path, out_path]).splitlines()[-40:]:
            log("    | " + line)
    return info


# ---- selection (progress mode) ----

def rank_key(test_id):
    return hashlib.sha256(test_id.encode("utf-8")).hexdigest()


def sample(tests, n):
    return sorted(tests, key=lambda t: (rank_key(t), t))[:n]


def selection_args(parser, ids):
    """Shell words that make the default command of a parser run only ids."""
    ids = sorted(ids)
    if parser == "gotest-json":
        pkgs = sorted({i.rsplit(".", 1)[0] for i in ids if "." in i})
        names = sorted({i.rsplit(".", 1)[1] for i in ids if "." in i})
        return " ".join(["-run", shlex.quote("^(" + "|".join(names) + ")$")] + [shlex.quote(p) for p in pkgs])
    if parser == "cargo":
        return " ".join(["--", "--exact"] + [shlex.quote(i.split("::", 1)[1]) for i in ids if "::" in i])
    return " ".join(shlex.quote(i) for i in ids)


# ---- spec ----

class SpecError(Exception):
    pass


def is_seconds(v):
    return v is None or (isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) and v >= 0)


def load_spec(path, scoring):
    with open(path, "r", encoding="utf-8") as f:
        spec = json.load(f)
    if not isinstance(spec, dict):
        raise SpecError("spec.json is not an object")
    cmds = spec.get("commands")
    if not isinstance(cmds, list) or not cmds:
        raise SpecError("spec.json has no commands")
    names = set()
    for c in cmds:
        if not isinstance(c, dict) or not isinstance(c.get("name"), str) or not isinstance(c.get("run"), str):
            raise SpecError("a command needs a name and a run line")
        if c.get("parser") not in PARSERS or c["name"] in names:
            raise SpecError("command %r: unknown parser or duplicate name" % c.get("name"))
        cwd = c.get("cwd") or "."
        if not isinstance(cwd, str) or os.path.isabs(cwd) or ".." in cwd.replace("\\", "/").split("/"):
            raise SpecError("command %r: cwd must be a relative path inside the project" % c["name"])
        if not is_seconds(c.get("timeout_s", 0)):
            raise SpecError("command %r: timeout_s must be a number of seconds >= 0" % c["name"])
        names.add(c["name"])
    if not is_seconds(spec.get("deadline_s", 0)):
        raise SpecError("deadline_s must be a number of seconds >= 0")
    units = spec.get("units") or []
    if not isinstance(units, list) or (scoring and not units):
        raise SpecError("spec.json has no units to score")
    seen, unit_names = set(), set()
    for u in units:
        if not isinstance(u, dict) or not isinstance(u.get("name"), str) or not isinstance(u.get("tests"), list) or not u["tests"]:
            raise SpecError("a unit needs a name and tests")
        if u["name"] in unit_names:
            raise SpecError("unit %r is named twice" % u["name"])
        unit_names.add(u["name"])
        w = u.get("weight", 1)
        if not isinstance(w, (int, float)) or isinstance(w, bool) or not math.isfinite(w) or w < 0:
            raise SpecError("unit %r: the weight must be a number >= 0" % u["name"])
        for t in u["tests"]:
            if not isinstance(t, str) or t in seen:
                raise SpecError("unit %r: test ids must be strings, each in one unit" % u["name"])
            seen.add(t)
    if scoring and sum(u.get("weight", 1) for u in units) <= 0:
        raise SpecError("the units' weights add up to 0")
    guards = spec.get("guards") or []
    if not isinstance(guards, list) or not all(isinstance(g, str) for g in guards):
        raise SpecError("guards must be a list of test ids")
    if len(set(guards)) != len(guards) or any(g in seen for g in guards):
        raise SpecError("a guard is listed twice, or is also a unit's test")
    origin = spec.get("origin") or {}
    if not isinstance(origin, dict) or not all(v in names for v in origin.values()):
        raise SpecError("origin must map test ids to command names")
    return spec


# ---- main ----

def main():
    here = os.path.dirname(os.path.abspath(__file__))
    mode = os.environ.get("SUITE_MODE", "")
    record = mode == "record"
    if mode not in ("", "score", "record"):
        log("suite: unknown SUITE_MODE %r" % mode)
        return 2
    try:
        spec = load_spec(os.path.join(here, "spec.json"), not record)
    except (OSError, ValueError, SpecError) as e:
        log("suite: cannot use spec.json: %s" % e)
        return 2
    n_sample = 0
    budget = 0.0
    if not record:
        try:
            n_sample = int(os.environ.get("SUITE_SAMPLE") or 0)
            budget = float(os.environ.get("SUITE_BUDGET_S") or 0)
        except ValueError:
            log("suite: SUITE_SAMPLE and SUITE_BUDGET_S must be numbers")
            return 2
    started = time.monotonic()
    deadline_s = spec.get("deadline_s") or DEFAULT_DEADLINE_S
    if budget > 0:
        deadline_s = min(deadline_s, budget)
    deadline = started + deadline_s

    units = spec.get("units") or []
    guards = list(spec.get("guards") or [])
    if n_sample > 0:
        units = [dict(u, tests=sample(u["tests"], n_sample)) for u in units]
        guards = sample(guards, n_sample)

    # The selection each command gets in progress mode: its own share of the sampled tests.
    selections = None
    if n_sample > 0:
        origin = spec.get("origin") or {}
        selections = {}
        for t in [t for u in units for t in u["tests"]] + guards:
            if t in origin:
                selections.setdefault(origin[t], []).append(t)

    project = os.getcwd()
    tmp = tempfile.mkdtemp(prefix="suite-")
    base_env = dict(os.environ)
    for k in ("SUITE_MODE", "SUITE_SAMPLE", "SUITE_BUDGET_S", "SUITE_ARGS", "SUITE_REPORT"):
        base_env.pop(k, None)
    base_env["SUITE_TMP"], base_env["SUITE_DIR"] = tmp, here

    reports = []
    try:
        for i, cmd in enumerate(spec["commands"]):
            args, chosen = None, None
            if selections is not None and spec.get("origin"):
                chosen = selections.get(cmd["name"])
                if not chosen:
                    log("suite: %s: not run, none of its tests is in the sample" % cmd["name"])
                    continue
                args = selection_args(cmd["parser"], chosen)
            reports.append(run_command(i, cmd, tmp, project, deadline, base_env, args, chosen))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    if record:
        line = {"suite_record": 1, "commands": reports}
        sys.stderr.flush()
        sys.stdout.write(json.dumps(line, sort_keys=True, separators=(",", ":")) + "\n")
        sys.stdout.flush()
        return 0

    results = {}
    for r in reports:
        for t, o in r["tests"].items():
            merge(results, t, o)

    failures = []
    num, den = Fraction(0), Fraction(0)
    out_units = {}
    for u in units:
        tests = u["tests"]
        if not tests:
            continue
        w = u.get("weight", 1)
        passed = 0
        for t in tests:
            o = results.get(t)
            if o == PASS:
                passed += 1
            else:
                failures.append("FAIL %s: %s (%s)" % (u["name"], t, o or "not reported"))
        out_units[u["name"]] = {"passed": passed, "total": len(tests), "weight": w}
        num += Fraction(w) * Fraction(passed, len(tests))
        den += Fraction(w)
    share = num / den if den else Fraction(0)
    kept = 0
    for g in guards:
        o = results.get(g)
        if o == PASS:
            kept += 1
        else:
            failures.append("BROKEN GUARD %s (%s)" % (g, o or "not reported"))
    factor = Fraction(kept, len(guards)) if guards else Fraction(1)
    score = float(share * factor)

    for line in failures[:MAX_FAILURE_LINES]:
        log(line)
    if len(failures) > MAX_FAILURE_LINES:
        log("... and %d more" % (len(failures) - MAX_FAILURE_LINES))
    log("suite: score %.6f: %d of %d units' tests passed, %d of %d guards kept%s" % (
        score, sum(u["passed"] for u in out_units.values()), sum(u["total"] for u in out_units.values()),
        kept, len(guards), " (sample of %d per unit)" % n_sample if n_sample > 0 else ""))

    line = {"score": score, "units": out_units}
    if guards:
        line["guards"] = {"kept": kept, "total": len(guards)}
    sys.stderr.flush()
    sys.stdout.write(json.dumps(line, sort_keys=True, separators=(",", ": ")) + "\n")
    sys.stdout.flush()
    return 0


def on_signal(signum, frame):
    if CURRENT[0] is not None:
        kill_group(CURRENT[0])
    os._exit(128 + signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGINT, on_signal)
    sys.exit(main())
