#!/usr/bin/env python3
"""Aggregate gh-stack skill eval result.json files."""

from __future__ import annotations

import argparse
import csv
import json
import statistics
from pathlib import Path


EVAL_ROOT = Path(__file__).resolve().parent


def load_results(results_dir: Path, prefix: str) -> list[dict]:
    rows = []
    for path in results_dir.glob("*/result.json"):
        result = json.loads(path.read_text())
        if not prefix or str(result.get("run_id", "")).startswith(prefix):
            rows.append(result)
    return sorted(rows, key=lambda row: (row["case"], row["model"], row["arm"]))


def rate(rows: list[dict]) -> dict:
    passed = sum(row.get("grade", {}).get("passed", False) for row in rows)
    return {
        "passed": passed,
        "total": len(rows),
        "percent": 100 * passed / len(rows) if rows else 0,
    }


def median(rows: list[dict], key: str) -> float:
    values = [row["telemetry"][key] for row in rows if "telemetry" in row]
    return statistics.median(values) if values else 0


def summarize(rows: list[dict]) -> dict:
    arms = sorted({row["arm"] for row in rows})
    models = sorted({row["model"] for row in rows})
    tiers = sorted({row["tier"] for row in rows})
    cases = sorted({row["case"] for row in rows})
    summary = {
        "run_count": len(rows),
        "by_arm": {},
        "by_model_arm": {},
        "by_tier_arm": {},
        "by_case_arm": {},
    }

    for arm in arms:
        selected = [row for row in rows if row["arm"] == arm]
        summary["by_arm"][arm] = {
            **rate(selected),
            "median_tool_calls": median(selected, "tool_calls"),
            "median_input_tokens": median(selected, "input_tokens"),
            "hangs": sum(
                row.get("execution", {}).get("timed_out", False)
                for row in selected
            ),
            "skill_invocations": sum(
                row.get("telemetry", {}).get("skill_invoked", False)
                for row in selected
            ),
        }

    for model in models:
        summary["by_model_arm"][model] = {}
        for arm in arms:
            selected = [
                row for row in rows
                if row["model"] == model and row["arm"] == arm
            ]
            summary["by_model_arm"][model][arm] = {
                **rate(selected),
                "median_tool_calls": median(selected, "tool_calls"),
                "median_input_tokens": median(selected, "input_tokens"),
            }

    for tier in tiers:
        summary["by_tier_arm"][tier] = {}
        for arm in arms:
            summary["by_tier_arm"][tier][arm] = rate(
                [row for row in rows if row["tier"] == tier and row["arm"] == arm]
            )

    for case in cases:
        summary["by_case_arm"][case] = {}
        for arm in arms:
            summary["by_case_arm"][case][arm] = rate(
                [row for row in rows if row["case"] == case and row["arm"] == arm]
            )
    return summary


def write_csv(rows: list[dict], path: Path) -> None:
    with path.open("w", newline="") as handle:
        writer = csv.DictWriter(
            handle,
            fieldnames=[
                "run_id",
                "case",
                "tier",
                "model",
                "arm",
                "passed",
                "skill_invoked",
                "tool_calls",
                "bash_calls",
                "input_tokens",
                "output_tokens",
                "duration_seconds",
                "timed_out",
                "references_opened",
            ],
        )
        writer.writeheader()
        for row in rows:
            telemetry = row.get("telemetry", {})
            writer.writerow(
                {
                    "run_id": row["run_id"],
                    "case": row["case"],
                    "tier": row["tier"],
                    "model": row["model"],
                    "arm": row["arm"],
                    "passed": row.get("grade", {}).get("passed", False),
                    "skill_invoked": telemetry.get("skill_invoked", False),
                    "tool_calls": telemetry.get("tool_calls", 0),
                    "bash_calls": telemetry.get("bash_calls", 0),
                    "input_tokens": telemetry.get("input_tokens", 0),
                    "output_tokens": telemetry.get("output_tokens", 0),
                    "duration_seconds": row.get("execution", {}).get(
                        "duration_seconds", 0
                    ),
                    "timed_out": row.get("execution", {}).get("timed_out", False),
                    "references_opened": ",".join(
                        telemetry.get("references_opened", [])
                    ),
                }
            )


def markdown(summary: dict) -> str:
    lines = [
        "# gh-stack skill eval results",
        "",
        f"Runs: **{summary['run_count']}**",
        "",
        "| Configuration | Pass rate | Median tools | Median input tokens | Hangs |",
        "|---|---:|---:|---:|---:|",
    ]
    for arm, values in summary["by_arm"].items():
        lines.append(
            f"| {arm} | {values['passed']}/{values['total']} "
            f"({values['percent']:.1f}%) | {values['median_tool_calls']:g} | "
            f"{values['median_input_tokens']:g} | {values['hangs']} |"
        )
    lines.extend(["", "## By case", ""])
    arms = list(summary["by_arm"])
    lines.append("| Case | " + " | ".join(arms) + " |")
    lines.append("|---|" + "|".join("---:" for _ in arms) + "|")
    for case, values in summary["by_case_arm"].items():
        cells = [
            f"{values[arm]['passed']}/{values[arm]['total']}"
            for arm in arms
        ]
        lines.append(f"| {case} | " + " | ".join(cells) + " |")
    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--results-dir",
        type=Path,
        default=EVAL_ROOT / "results",
    )
    parser.add_argument("--prefix", default="")
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=EVAL_ROOT / "results",
    )
    args = parser.parse_args()

    rows = load_results(args.results_dir, args.prefix)
    if not rows:
        raise SystemExit("no result.json files matched")
    args.output_dir.mkdir(parents=True, exist_ok=True)
    summary = summarize(rows)
    (args.output_dir / "summary.json").write_text(
        json.dumps(summary, indent=2) + "\n"
    )
    (args.output_dir / "results.json").write_text(
        json.dumps(rows, indent=2) + "\n"
    )
    write_csv(rows, args.output_dir / "results.csv")
    report = markdown(summary)
    (args.output_dir / "summary.md").write_text(report)
    print(report)


if __name__ == "__main__":
    main()
