#!/usr/bin/env python3
"""Run a matrix of gh-stack skill evaluations."""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
import subprocess
import sys
import time
from pathlib import Path

from case_loader import load_cases


EVAL_ROOT = Path(__file__).resolve().parent
RUNNER = EVAL_ROOT / "runner.py"
CASES = load_cases()


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
    skill_ref: str | None,
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
    if skill_ref and arm == "current":
        command.extend(["--skill-ref", skill_ref])
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
    parser.add_argument(
        "--cases",
        default="all",
        help="Comma-separated scenario names, or all",
    )
    parser.add_argument(
        "--arms",
        default="current",
        help="Comma-separated configurations: current,none",
    )
    parser.add_argument(
        "--models",
        default="mini,sonnet",
        help="Comma-separated model aliases: mini,sonnet",
    )
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--jobs", type=int, default=2, help="Concurrent trials")
    parser.add_argument("--prefix", default="", help="Batch/run ID prefix")
    parser.add_argument(
        "--shuffle-seed",
        help="Deterministically shuffle run order; defaults to the suite prefix",
    )
    parser.add_argument(
        "--skill-path",
        type=Path,
        help="Evaluate an uncommitted skill directory",
    )
    parser.add_argument(
        "--skill-ref",
        help="Evaluate skills/gh-stack from a git commit, tag, or branch",
    )
    parser.add_argument(
        "--fail-on-failure",
        action="store_true",
        help="Return nonzero when any eval fails",
    )
    args = parser.parse_args()
    if args.skill_path and args.skill_ref:
        parser.error("--skill-path and --skill-ref are mutually exclusive")
    results_dir = Path(
        os.environ.get("GH_STACK_EVAL_RESULTS_DIR", EVAL_ROOT / "results")
    )
    results_dir.mkdir(parents=True, exist_ok=True)

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
                    jobs.append(
                        (
                            case,
                            arm,
                            model,
                            iteration,
                            args.skill_path,
                            args.skill_ref,
                        )
                    )
    seed = args.shuffle_seed or prefix
    jobs.sort(
        key=lambda job: hashlib.sha256(
            f"{seed}|{'|'.join(str(value) for value in job[:4])}".encode()
        ).hexdigest()
    )
    (results_dir / f"{prefix}-plan.json").write_text(
        json.dumps(
            {
                "prefix": prefix,
                "shuffle_seed": seed,
                "cases": cases,
                "arms": arms,
                "models": models,
                "repetitions": args.repetitions,
                "skill_ref": args.skill_ref,
                "skill_path": str(args.skill_path) if args.skill_path else None,
                "jobs": [
                    {
                        "case": case,
                        "arm": arm,
                        "model": model,
                        "iteration": iteration,
                    }
                    for case, arm, model, iteration, _, _ in jobs
                ],
            },
            indent=2,
        )
        + "\n"
    )

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
