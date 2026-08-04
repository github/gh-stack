#!/usr/bin/env python3
"""Run a matrix of gh-stack skill evaluations."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import subprocess
import sys
import time
from pathlib import Path


EVAL_ROOT = Path(__file__).resolve().parent
RUNNER = EVAL_ROOT / "runner.py"
CASES = {
    item["name"]: item
    for item in json.loads((EVAL_ROOT / "cases.json").read_text())
}


def split_values(value: str, allowed: set[str], label: str) -> list[str]:
    values = list(allowed) if value == "all" else value.split(",")
    invalid = set(values) - allowed
    if invalid:
        raise SystemExit(f"unknown {label}: {', '.join(sorted(invalid))}")
    return sorted(values)


def run_one(
    case: str,
    arm: str,
    model: str,
    iteration: str,
    skill_path: Path | None,
) -> tuple[str, int, bool]:
    command = [
        sys.executable,
        str(RUNNER),
        "--case",
        case,
        "--arm",
        arm,
        "--model",
        model,
        "--iteration",
        iteration,
    ]
    if skill_path and arm == "current":
        command.extend(["--skill-path", str(skill_path)])
    result = subprocess.run(command, text=True, capture_output=True)
    label = f"{case}/{model}/{arm}"
    blocking = arm == "current" and CASES[case].get("required", True)
    if result.returncode == 0:
        return label, 0, blocking
    # runner prints the complete result JSON even for assertion failures.
    try:
        payload = json.loads(result.stdout)
        failed = [
            name
            for name, passed in payload.get("grade", {}).get(
                "assertions", {}
            ).items()
            if not passed
        ]
        detail = ", ".join(failed) or payload.get("fatal_error", "failed")
    except json.JSONDecodeError:
        detail = result.stderr.strip() or "runner failed"
    return f"{label}: {detail}", result.returncode, blocking


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cases", default="all")
    parser.add_argument("--arms", default="current")
    parser.add_argument("--models", default="mini,sonnet")
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--jobs", type=int, default=2)
    parser.add_argument("--prefix", default="")
    parser.add_argument("--skill-path", type=Path)
    parser.add_argument(
        "--fail-on-failure",
        action="store_true",
        help="Return nonzero when any eval fails",
    )
    args = parser.parse_args()
    Path(
        os.environ.get("GH_STACK_EVAL_RESULTS_DIR", EVAL_ROOT / "results")
    ).mkdir(parents=True, exist_ok=True)

    cases = split_values(args.cases, set(CASES), "cases")
    arms = split_values(args.arms, {"current", "none"}, "arms")
    models = split_values(args.models, {"mini", "sonnet"}, "models")
    prefix = args.prefix or time.strftime("suite-%Y%m%d-%H%M%S")
    jobs = []
    for repetition in range(1, args.repetitions + 1):
        iteration = f"{prefix}-r{repetition}"
        for case in cases:
            for arm in arms:
                for model in models:
                    jobs.append((case, arm, model, iteration, args.skill_path))

    failures = []
    print(f"Running {len(jobs)} evals with {args.jobs} workers (prefix: {prefix})")
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as executor:
        futures = [executor.submit(run_one, *job) for job in jobs]
        for future in concurrent.futures.as_completed(futures):
            label, returncode, blocking = future.result()
            status = (
                "PASS"
                if returncode == 0
                else "FAIL"
                if blocking
                else "INFO"
            )
            print(f"[{status}] {label}", flush=True)
            if returncode and blocking:
                failures.append(label)

    print(f"\nCompleted: {len(jobs) - len(failures)}/{len(jobs)} passed")
    print(
        f"Aggregate with: python evals/aggregate.py --prefix {prefix}",
    )
    return 1 if failures and args.fail_on_failure else 0


if __name__ == "__main__":
    sys.exit(main())
