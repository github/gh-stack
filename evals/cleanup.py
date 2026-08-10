#!/usr/bin/env python3
"""Best-effort cleanup for interrupted gh-stack eval suites."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
from pathlib import Path


def run(
    args: list[str],
    *,
    cwd: Path | None = None,
    check: bool = False,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        cwd=cwd,
        text=True,
        capture_output=True,
        check=check,
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", required=True, help="owner/repo test repository")
    parser.add_argument("--prefix", required=True, help="suite iteration prefix")
    parser.add_argument(
        "--source-repo",
        type=Path,
        default=Path(os.environ.get("GH_STACK_EVAL_SOURCE_REPO", ".")),
        help="local checkout whose origin points at the disposable repository",
    )
    parser.add_argument("--remote", default="origin")
    parser.add_argument("--audit-path", type=Path)
    args = parser.parse_args()
    args.source_repo = args.source_repo.expanduser().resolve()
    if not args.source_repo.is_dir():
        parser.error(f"source repository not found: {args.source_repo}")

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
            "all",
            "--limit",
            "500",
            "--json",
            "number,headRefName,state",
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
        if pr["state"] == "OPEN":
            run(["gh", "pr", "close", str(pr["number"]), "--repo", args.repo])

    refs = run(
        ["git", "ls-remote", "--heads", args.remote],
        cwd=args.source_repo,
        check=True,
    )
    for line in refs.stdout.splitlines():
        ref = line.split("refs/heads/")[-1]
        if ref.startswith(head_prefix) or ref.startswith(base_prefix):
            run(
                ["git", "push", args.remote, "--delete", ref],
                cwd=args.source_repo,
            )

    remaining_prs = [
        item
        for item in json.loads(
            run(
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
            ).stdout
        )
        if item["headRefName"].startswith(head_prefix)
    ]
    remaining_refs = []
    for line in run(
        ["git", "ls-remote", "--heads", args.remote],
        cwd=args.source_repo,
        check=True,
    ).stdout.splitlines():
        ref = line.split("refs/heads/")[-1]
        if ref.startswith(head_prefix) or ref.startswith(base_prefix):
            remaining_refs.append(ref)
    audit = {
        "repo": args.repo,
        "prefix": args.prefix,
        "remaining_pull_requests": remaining_prs,
        "remaining_refs": remaining_refs,
        "passed": not remaining_prs and not remaining_refs,
    }
    text = json.dumps(audit, indent=2) + "\n"
    if args.audit_path:
        args.audit_path.parent.mkdir(parents=True, exist_ok=True)
        args.audit_path.write_text(text)
    print(text, end="")
    return 0 if audit["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
