#!/usr/bin/env python3
"""Best-effort cleanup for interrupted gh-stack eval suites."""

from __future__ import annotations

import argparse
import json
import subprocess


def run(args: list[str], check: bool = False) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, capture_output=True, check=check)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", required=True, help="owner/repo test repository")
    parser.add_argument("--prefix", required=True, help="suite iteration prefix")
    parser.add_argument("--remote", default="origin")
    args = parser.parse_args()

    head_prefix = f"eval/{args.prefix}"
    base_prefix = f"eval-base/{args.prefix}"
    listed = run(
        [
            "gh",
            "pr",
            "list",
            "--repo",
            args.repo,
            "--state",
            "open",
            "--limit",
            "500",
            "--json",
            "number,headRefName",
        ],
        check=True,
    )
    prs = [
        item
        for item in json.loads(listed.stdout)
        if item["headRefName"].startswith(head_prefix)
    ]
    numbers = {int(item["number"]) for item in prs}

    stacks_result = run(
        ["gh", "api", f"repos/{args.repo}/stacks", "--paginate"]
    )
    if stacks_result.returncode == 0:
        for stack in json.loads(stacks_result.stdout or "[]"):
            members = {
                int(pr["number"])
                for pr in stack.get("pull_requests", [])
                if "number" in pr
            }
            if members & numbers:
                run(
                    [
                        "gh",
                        "api",
                        "--method",
                        "POST",
                        f"repos/{args.repo}/stacks/{stack['number']}/unstack",
                    ]
                )

    for pr in prs:
        run(["gh", "pr", "close", str(pr["number"]), "--repo", args.repo])

    refs = run(["git", "ls-remote", "--heads", args.remote], check=True)
    for line in refs.stdout.splitlines():
        ref = line.split("refs/heads/")[-1]
        if ref.startswith(head_prefix) or ref.startswith(base_prefix):
            run(["git", "push", args.remote, "--delete", ref])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
