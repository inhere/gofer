#!/usr/bin/env python3
"""Spread the Go test suite of one CI leg over several runners.

Every package still runs on every leg: this script only decides *which runner*
runs which package (or which part of a package), so that no single job carries
the whole suite. Packages are packed with longest-processing-time first into
`--count` shards of roughly equal cost.

A package cannot be halved by `go test` on its own, so the few packages that
dominate a shard alone (`SPLIT`) additionally have their top-level tests cut
into equal-size groups by name, run as separate invocations of the same package
with `-run` filters. Test names are spread over the groups by a hash of the
name, not alphabetically: durations are uncorrelated with the name, and an
alphabetical cut would follow whatever order the file layout has.

A package in `EXCLUSIVE` instead gets a shard of its own and shares it with
nothing: `internal/commands` keeps package-level flag globals and has many
timing-sensitive tests, so running other packages (or halves of it) alongside
inflates it several times over.

Usage (from the repository root, after `go` is on PATH):

    python .github/scripts/ci_shard.py --shard 1 --count 5 -- go test -timeout 20m -p 2
    python .github/scripts/ci_shard.py --shard 1 --count 5 --dry-run -- go test -timeout 20m
    python .github/scripts/ci_shard.py --verify --count 5

`--count` must exceed the number of `EXCLUSIVE` packages, since each of them
needs a whole shard of its own. `--verify` prints, for every shard, the packages
and test names it would run and fails unless each package and each enumerated
test lands in exactly one place and no exclusive package shares a shard.
"""
import argparse
import json
import re
import subprocess
import sys
import zlib

# Whole-package wall-clock seconds measured on a GitHub windows-latest runner
# (4 vCPU, `-p 2`), median of the `go stable` and `go 1.26` legs of runs
# 39709277413 (v0.183.0) and 39711920511 (v0.183.1). For the three big packages
# the per-group `ok` lines of the split invocations were summed back up, and the
# slowest leg of a run (a loaded runner: commands 549s, job 161s per group) was
# left out. The absolute numbers only need to be right relative to each other -
# they are what the packing balances; a shard's wall clock also includes ~60s of
# checkout/setup and, with `-p 2`, ends up well below the sum of its packages.
COST = {
    "internal/commands": 370.0,  # runs alone in its own shard (EXCLUSIVE)
    "internal/job": 340.0,  # 3 x ~113s groups
    "internal/httpapi": 190.0,  # 2 x ~95s groups
    "internal/core": 74.6,
    "internal/jobstore": 66.1,
    "internal/worker": 46.2,
    "internal/delivery": 37.3,
    "internal/mcpserver": 35.5,
    "internal/sessionrelay": 24.5,
    "internal/work": 20.1,
    "internal/job/workflow": 18.6,
    "internal/tracker": 14.8,
    "internal/serve": 11.8,
    "internal/workbench": 10.0,
    "internal/wshub": 9.1,
    "internal/client": 9.1,
    "internal/hookrelay": 8.8,
    "internal/steward": 8.8,
    "internal/brief": 8.3,
    "internal/runner/peerhttp": 6.8,
    "internal/mail": 6.7,
    "internal/runner/local": 6.2,
    "internal/today": 5.7,
    "internal/workerenroll": 5.7,
    "internal/changelog": 5.0,
    "internal/servicemgr": 4.5,
    "internal/agent": 4.3,
    "internal/project": 3.4,
    "internal/acp": 3.1,
    "internal/runner/pty": 2.5,
    "internal/messenger": 2.4,
    "internal/streaming": 2.2,
    "internal/workflowstep": 1.8,
    "internal/webpush": 1.8,
    "internal/settings": 1.7,
    "internal/runner/acp": 1.4,
    "internal/pushhub": 1.3,
    "internal/tunnel": 1.3,
    "internal/attachment": 1.0,
    "internal/runner/worker": 0.8,
    "internal/config": 0.8,
    "internal/overview": 0.6,
    "internal/daemon": 0.4,
    "internal/procattr": 0.4,
    "internal/notify": 0.4,
    "scripts/tracker-transfer": 0.3,
    "internal/xfer": 0.3,
    "internal/configlint": 0.2,
    "internal/logx": 0.2,
    "internal/certutil": 0.2,
    "internal/skill": 0.2,
    "internal/util": 0.1,
    "internal/ptyrelay": 0.1,
    "internal/mcpprobe": 0.1,
    "internal/testutil/wait": 0.1,
    "internal/updatecheck": 0.1,
    "internal/store": 0.1,
    "internal/webui": 0.1,
    "internal/netproxy": 0.1,
    "internal/workerupgrade": 0.0,
    "internal/bdmigrate": 0.0,
    "internal/castrec": 0.0,
    "internal/work/transcript": 0.0,
    "internal/runner/ndjsonfilter": 0.0,
    "internal/template": 0.0,
    "internal/authz": 0.0,
    "internal/focusremote": 0.0,
    "internal/secret": 0.0,
    "internal/buildinfo": 0.0,
    "internal/wsproto": 0.0,
    "internal/proctree": 0.0,
    "internal/runner": 0.0,
    "internal/metrics": 0.0,
}

# Packages that are cut by test name: the value is how many groups the
# package's tests are split into.
SPLIT = {
    "internal/job": 3,
    "internal/httpapi": 2,
}

# Packages that must not share a shard with anything: each one gets a whole
# shard for itself, the other packages are packed into the remaining shards.
EXCLUSIVE = {
    # Package-level flag globals plus many timing-sensitive tests: alongside
    # other packages (or cut into two halves, which doubled the wall clock on a
    # loaded runner) it runs several times slower and its timing tests flake.
    "internal/commands",
}

# A package not in COST at all: new packages are small until they aren't.
DEFAULT_COST = 12.0

# Top-level test functions. `go test -run` also gates Examples and the seed
# corpus of Fuzz targets, so those must be enumerated as well, otherwise a
# split package would silently stop running them.
TEST_NAME_RE = re.compile(r"^(Test|Example|Fuzz)[A-Za-z0-9_]*$")


def sh(cmd):
    out = subprocess.run(cmd, capture_output=True, text=True)
    if out.returncode != 0:
        sys.stderr.write("$ %s\n%s%s" % (" ".join(cmd), out.stdout, out.stderr))
        raise SystemExit("command failed: %s" % " ".join(cmd))
    return out.stdout


def module_path():
    return sh(["go", "list", "-m"]).strip()


def packages(mod):
    prefix = mod + "/"
    pkgs = []
    for line in sh(["go", "list", "./..."]).split():
        pkgs.append(line[len(prefix):] if line.startswith(prefix) else line)
    return pkgs


def test_names(pkg):
    """Top-level test/example/fuzz names of one package, deduplicated."""
    out = sh(["go", "test", "-list", "^(Test|Example|Fuzz)", "./" + pkg])
    names = []
    seen = set()
    for line in out.splitlines():
        name = line.strip()
        if not TEST_NAME_RE.match(name) or name in seen:
            continue
        seen.add(name)
        names.append(name)
    return names


def units(count):
    """The sharding units: (label, weight, package, run-regex or "")."""
    mod = module_path()
    out = []
    for pkg in packages(mod):
        cost = COST.get(pkg, DEFAULT_COST)
        groups = 1 if pkg in EXCLUSIVE else SPLIT.get(pkg, 1)
        if groups > 1:
            names = test_names(pkg)
            # Spread by hash so each group gets a mix of durations; an
            # alphabetical cut would follow the file layout instead.
            names.sort(key=lambda n: (zlib.crc32(n.encode()), n))
            groups = min(groups, len(names))
            if groups > 1:
                for i in range(groups):
                    part = names[i::groups]
                    out.append(
                        (pkg, cost / groups, pkg, "^(%s)$" % "|".join(part))
                    )
                continue
        out.append((pkg, cost, pkg, ""))
    return out


def plan(units_list, shard, count):
    """Greedy longest-processing-time packing; deterministic for a given input.

    An `EXCLUSIVE` package takes a whole shard before the packing starts, so
    nothing is ever put next to it; the other units are spread over the shards
    that are left.
    """
    shards = [[] for _ in range(count)]
    loads = [0.0] * count
    alone = sorted((u for u in units_list if u[2] in EXCLUSIVE),
                   key=lambda u: (-u[1], u[0]))
    for i, unit in enumerate(alone):
        shards[i].append(unit)
        loads[i] += unit[1]
    free = list(range(len(alone), count))
    rest = sorted((u for u in units_list if u[2] not in EXCLUSIVE),
                  key=lambda u: (-u[1], u[0], u[3]))
    for unit in rest:
        i = min(free, key=lambda j: (loads[j], j))
        shards[i].append(unit)
        loads[i] += unit[1]
    return shards[shard - 1], loads


def commands_for(units_list, base):
    """Split a shard's units into go test invocations."""
    plain = sorted({u[2] for u in units_list if not u[3]})
    cmds = []
    if plain:
        cmds.append(base + ["./" + p for p in plain])
    for unit in sorted((u for u in units_list if u[3]), key=lambda u: -u[1]):
        cmds.append(base + ["./" + unit[2], "-run", unit[3]])
    return cmds


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--shard", type=int, default=1, help="1-based shard number")
    ap.add_argument("--count", type=int, default=1, help="number of shards")
    ap.add_argument("--dry-run", action="store_true", help="print the commands only")
    ap.add_argument("--verify", action="store_true", help="check the partition covers everything once")
    ap.add_argument("command", nargs=argparse.REMAINDER, help="-- go test ... (base command)")
    args = ap.parse_args()

    if args.count < 1 or not 1 <= args.shard <= args.count:
        raise SystemExit("shard must be within 1..count")
    if args.count <= len(EXCLUSIVE):
        raise SystemExit("count must exceed %d: %s each need a shard of their own"
                         % (len(EXCLUSIVE), ", ".join(sorted(EXCLUSIVE))))
    base = args.command[1:] if args.command[:1] == ["--"] else args.command

    units_list = units(args.count)
    shard_units, loads = plan(units_list, args.shard, args.count)
    cmds = commands_for(shard_units, base)

    if args.verify:
        covered, tests, alone = set(), set(), set()
        for i in range(args.count):
            part, _ = plan(units_list, i + 1, args.count)
            mine = {u[2] for u in part if u[2] in EXCLUSIVE}
            if len(mine) > 1:
                raise SystemExit("exclusive packages share shard %d: %s"
                                 % (i + 1, ", ".join(sorted(mine))))
            if mine and len(part) > 1:
                raise SystemExit("exclusive package %s shares shard %d with %s"
                                 % (mine.pop(), i + 1,
                                    ", ".join(sorted({u[2] for u in part} - mine))))
            for unit in part:
                if unit[2] in EXCLUSIVE:
                    alone.add(unit[2])
                if unit[2] in covered and not unit[3]:
                    raise SystemExit("package %s in two shards" % unit[2])
                if not unit[3]:
                    covered.add(unit[2])
                else:
                    for name in re.sub(r"^\^\(|\)\$$", "", unit[3]).split("|"):
                        key = (unit[2], name)
                        if key in tests:
                            raise SystemExit("test %s/%s in two shards" % key)
                        tests.add(key)
        missing = sorted({u[2] for u in units_list} - covered - {t[0] for t in tests})
        if missing:
            raise SystemExit("packages in no shard: %s" % ", ".join(missing))
        if alone != EXCLUSIVE:
            raise SystemExit("exclusive packages without a shard: %s"
                             % ", ".join(sorted(EXCLUSIVE - alone)))
        print("ok: %d units, %d packages, %d split tests, %d exclusive, loads %s"
              % (len(units_list), len(covered), len(tests), len(alone),
                 [round(l) for l in loads]))
        return

    print("shard %d/%d: %d units, ~%ds of tests, %d invocation(s)"
          % (args.shard, args.count, len(shard_units), round(loads[args.shard - 1]), len(cmds)),
          file=sys.stderr)
    for unit in sorted(shard_units, key=lambda u: -u[1]):
        if unit[3]:
            print("  %-28s ~%3ds  %d tests" % (unit[2], round(unit[1]),
                                               unit[3].count("|") + 1), file=sys.stderr)
    if args.dry_run:
        print(json.dumps([c[1:] if c[:1] == ["--"] else c for c in cmds], indent=2))
        return
    for cmd in cmds:
        print("$ %s" % " ".join(cmd), flush=True)
        rc = subprocess.run(cmd).returncode
        if rc != 0:
            raise SystemExit(rc)


if __name__ == "__main__":
    main()
