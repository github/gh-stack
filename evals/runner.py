#!/usr/bin/env python3
"""Run one isolated gh-stack skill evaluation."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import signal
import sqlite3
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any

from case_loader import load_cases


EVAL_ROOT = Path(__file__).resolve().parent
REPO_ROOT = EVAL_ROOT.parent
USER_HOME = Path.home()
SOURCE_REPO = Path(
    os.environ.get("GH_STACK_EVAL_SOURCE_REPO", str(USER_HOME / "test"))
).expanduser().resolve()
REMOTE_URL = os.environ.get("GH_STACK_EVAL_REMOTE_URL", "")
REPO_SLUG = os.environ.get("GH_STACK_EVAL_REPO", "")
DEFAULT_BRANCH = os.environ.get("GH_STACK_EVAL_DEFAULT_BRANCH", "main")
SEED_REF = os.environ.get("GH_STACK_EVAL_SEED_REF", DEFAULT_BRANCH)
EXPECTED_SEED_SHA = os.environ.get("GH_STACK_EVAL_SEED_SHA", "")
RESULTS_ROOT = Path(
    os.environ.get("GH_STACK_EVAL_RESULTS_DIR", str(EVAL_ROOT / "results"))
).expanduser().resolve()
CASES = load_cases()
MODELS = {
    "sonnet": ("claude-sonnet-4.5", []),
    "mini": ("gpt-5.4-mini", ["--effort", "low"]),
}
ARMS = {"current", "none"}


def shell(
    args: list[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    check: bool = True,
    timeout: int = 180,
) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        args,
        cwd=cwd,
        env=env,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    if check and result.returncode:
        raise RuntimeError(
            f"command failed ({result.returncode}): {' '.join(args)}\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )
    return result


def git(repo: Path, *args: str, check: bool = True) -> str:
    return shell(["git", *args], cwd=repo, check=check).stdout.strip()


def gh(repo: Path, *args: str, check: bool = True, timeout: int = 180) -> str:
    return shell(["gh", *args], cwd=repo, check=check, timeout=timeout).stdout.strip()


def configure() -> None:
    global REPO_SLUG
    if not (REPO_ROOT / "gh-stack").is_file():
        raise RuntimeError("build the extension first: go build -o gh-stack .")
    eval_token = os.environ.get("GH_STACK_EVAL_GITHUB_TOKEN")
    if eval_token and not os.environ.get("GH_TOKEN"):
        os.environ["GH_TOKEN"] = eval_token
    if not REPO_SLUG:
        REPO_SLUG = gh(
            SOURCE_REPO,
            "repo",
            "view",
            "--json",
            "nameWithOwner",
            "--jq",
            ".nameWithOwner",
        )


def write(repo: Path, relative: str, content: str) -> None:
    path = repo / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


def commit(repo: Path, message: str, *paths: str) -> str:
    git(repo, "add", "--", *paths)
    git(repo, "commit", "-m", message)
    return git(repo, "rev-parse", "HEAD")


def export_skill(ref: str, destination: Path) -> Path:
    paths = git(
        REPO_ROOT,
        "ls-tree",
        "-r",
        "--name-only",
        ref,
        "--",
        "skills/gh-stack",
    ).splitlines()
    if not paths:
        raise RuntimeError(
            f"{ref!r} does not contain skills/gh-stack; fetch the commit first"
        )
    skill = destination / "gh-stack"
    for path in paths:
        relative = Path(path).relative_to("skills/gh-stack")
        target = skill / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        result = subprocess.run(
            ["git", "show", f"{ref}:{path}"],
            cwd=REPO_ROOT,
            capture_output=True,
            check=True,
        )
        target.write_bytes(result.stdout)
    return skill


def directory_sha256(root: Path) -> str:
    digest = hashlib.sha256()
    for path in sorted(item for item in root.rglob("*") if item.is_file()):
        digest.update(str(path.relative_to(root)).encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def file_sha256(path: Path) -> str | None:
    if not path.is_file():
        return None
    return hashlib.sha256(path.read_bytes()).hexdigest()


def command_version(command: list[str]) -> str:
    result = shell(command, check=False)
    output = (result.stdout or result.stderr).strip()
    return output.splitlines()[0] if output else "unknown"


def provenance(
    case_name: str,
    skill: Path | None,
    skill_ref: str | None,
    skill_source: str,
    model_key: str,
) -> dict[str, Any]:
    binary = REPO_ROOT / "gh-stack"
    return {
        "case_contract_sha256": hashlib.sha256(
            Path(CASES[case_name]["contract_path"]).read_bytes()
            + CASES[case_name]["prompt"].encode()
        ).hexdigest(),
        "skill_source": skill_source,
        "skill_commit": (
            git(REPO_ROOT, "rev-parse", skill_ref) if skill_ref else None
        ),
        "skill_tree": (
            git(REPO_ROOT, "rev-parse", f"{skill_ref}:skills/gh-stack")
            if skill_ref
            else None
        ),
        "skill_sha256": directory_sha256(skill) if skill else None,
        "repository_commit": git(REPO_ROOT, "rev-parse", "HEAD"),
        "repository_dirty": bool(git(REPO_ROOT, "status", "--porcelain")),
        "fixture_seed_ref": SEED_REF,
        "fixture_seed_sha": EXPECTED_SEED_SHA or None,
        "gh_stack_binary_sha256": file_sha256(binary),
        "gh_stack_cli": command_version([str(binary), "--version"]),
        "model_alias": model_key,
        "model": MODELS[model_key][0],
        "copilot_cli": command_version(["copilot", "--version"]),
        "gh_cli": command_version(["gh", "--version"]),
        "git": command_version(["git", "--version"]),
        "python": platform.python_version(),
        "platform": platform.platform(),
    }


def prepare_repo(
    run_dir: Path,
    arm: str,
    skill_path: Path | None = None,
    skill_ref: str | None = None,
) -> Path:
    if not SOURCE_REPO.is_dir():
        raise RuntimeError(
            f"test repository not found: {SOURCE_REPO}; "
            "set GH_STACK_EVAL_SOURCE_REPO"
        )
    repo = run_dir / "repo"
    shell(["git", "clone", "--quiet", "--no-hardlinks", str(SOURCE_REPO), str(repo)])
    remote_url = REMOTE_URL or git(SOURCE_REPO, "remote", "get-url", "origin")
    git(repo, "remote", "set-url", "origin", remote_url)
    git(
        repo,
        "fetch",
        "--quiet",
        "origin",
        f"+refs/heads/{DEFAULT_BRANCH}:refs/remotes/origin/{DEFAULT_BRANCH}",
    )
    git(repo, "remote", "set-head", "origin", DEFAULT_BRANCH)
    if SEED_REF == DEFAULT_BRANCH:
        seed_sha = git(repo, "rev-parse", f"origin/{DEFAULT_BRANCH}")
    else:
        git(repo, "fetch", "--quiet", "origin", SEED_REF)
        seed_sha = git(repo, "rev-parse", "FETCH_HEAD")
    if EXPECTED_SEED_SHA and seed_sha != EXPECTED_SEED_SHA:
        raise RuntimeError(
            f"fixture seed mismatch: expected {EXPECTED_SEED_SHA}, got {seed_sha}"
        )
    git(
        repo,
        "checkout",
        "-q",
        "-B",
        DEFAULT_BRANCH,
        seed_sha,
    )
    git(repo, "config", "user.name", "gh-stack skill eval")
    git(repo, "config", "user.email", "gh-stack-eval@example.com")
    git(repo, "config", "rerere.enabled", "true")
    git(
        repo,
        "config",
        "credential.https://github.com.helper",
        "!gh auth git-credential",
    )
    git(repo, "config", "--local", "--unset-all", "remote.pushDefault", check=False)

    tracked_skill = ".agents/skills/gh-stack/SKILL.md"
    if (repo / tracked_skill).exists():
        git(repo, "update-index", "--skip-worktree", tracked_skill)
    shutil.rmtree(repo / ".agents/skills/gh-stack", ignore_errors=True)
    info_exclude = repo / ".git/info/exclude"
    with info_exclude.open("a") as handle:
        handle.write("\n.agents/skills/gh-stack/\n")

    if arm != "none":
        if skill_ref:
            source = export_skill(skill_ref, run_dir / "skill-source")
        elif skill_path:
            source = skill_path.resolve()
        else:
            source = REPO_ROOT / "skills/gh-stack"
        if not (source / "SKILL.md").is_file():
            raise RuntimeError(f"skill snapshot missing SKILL.md: {source}")
        target = repo / ".agents/skills/gh-stack"
        shutil.copytree(source, target)
    return repo


def create_eval_base(repo: Path, run_id: str) -> str:
    branch = f"eval-base/{run_id}"
    git(repo, "checkout", "-q", "-b", branch, "HEAD")
    git(repo, "push", "-q", "origin", branch)
    return branch


def prepare_home(run_dir: Path) -> Path:
    home = run_dir / "home"
    home.mkdir()
    ssh = USER_HOME / ".ssh"
    if ssh.exists():
        (home / ".ssh").symlink_to(ssh)
    (home / ".gitconfig").write_text(
        '[credential "https://github.com"]\n'
        "\thelper = !gh auth git-credential\n"
    )
    extensions = home / ".local/share/gh/extensions"
    extensions.mkdir(parents=True)
    (extensions / "gh-stack").symlink_to(REPO_ROOT)
    return home


def prepare_three_layer(
    repo: Path, run_id: str, prefix: str, base_branch: str
) -> dict[str, Any]:
    root = f"eval/{run_id}"
    branches = [f"{prefix}/model", f"{prefix}/api", f"{prefix}/ui"]

    gh(repo, "stack", "init", "--base", base_branch, branches[0])
    write(
        repo,
        f"{root}/user.js",
        "module.exports = { id: 1, name: 'Ada', createdAt: '2026-08-03T00:00:00Z' };\n",
    )
    model_sha = commit(repo, f"{run_id}: add user model", f"{root}/user.js")

    gh(repo, "stack", "add", branches[1])
    write(
        repo,
        f"{root}/serializer.js",
        "const user = require('./user');\n"
        "module.exports = () => ({ id: user.id, name: user.name });\n",
    )
    api_sha = commit(repo, f"{run_id}: add user serializer", f"{root}/serializer.js")

    gh(repo, "stack", "add", branches[2])
    write(
        repo,
        f"{root}/page.js",
        "const serialize = require('./serializer');\n"
        "module.exports = () => `<p>${serialize().name}</p>`;\n",
    )
    ui_sha = commit(repo, f"{run_id}: add user page", f"{root}/page.js")

    return {
        "root": root,
        "branches": branches,
        "fixture_shas": [model_sha, api_sha, ui_sha],
        "fixture_head": ui_sha,
    }


def prepare_reorder(
    repo: Path, run_id: str, prefix: str, base_branch: str
) -> dict[str, Any]:
    root = f"eval/{run_id}"
    branches = [f"{prefix}/models", f"{prefix}/migration", f"{prefix}/ui"]

    gh(repo, "stack", "init", "--base", base_branch, branches[0])
    write(repo, f"{root}/models.js", "module.exports = { table: 'users' };\n")
    model_sha = commit(repo, f"{run_id}: add models", f"{root}/models.js")

    gh(repo, "stack", "add", branches[1])
    write(repo, f"{root}/migration.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
    migration_sha = commit(repo, f"{run_id}: add migration", f"{root}/migration.sql")

    gh(repo, "stack", "add", branches[2])
    write(
        repo,
        f"{root}/ui.js",
        "const model = require('./models');\nmodule.exports = () => model.table;\n",
    )
    ui_sha = commit(repo, f"{run_id}: add ui", f"{root}/ui.js")
    return {
        "root": root,
        "branches": branches,
        "target_branches": [branches[1], branches[0], branches[2]],
        "fixture_shas": [model_sha, migration_sha, ui_sha],
        "fixture_head": ui_sha,
    }


def prepare_merge(
    repo: Path, run_id: str, prefix: str, base_branch: str
) -> dict[str, Any]:
    root = f"eval/{run_id}"
    branches = [f"{prefix}/base", f"{prefix}/top"]
    git(repo, "config", "remote.pushDefault", "origin")

    gh(repo, "stack", "init", "--base", base_branch, branches[0])
    write(repo, f"{root}/base.txt", f"{run_id} base\n")
    commit(repo, f"{run_id}: add base layer", f"{root}/base.txt")

    gh(repo, "stack", "add", branches[1])
    write(repo, f"{root}/top.txt", f"{run_id} top\n")
    commit(repo, f"{run_id}: add top layer", f"{root}/top.txt")
    gh(repo, "stack", "submit", "--auto", "--open", "--remote", "origin", timeout=180)

    prs = [list_pr(repo, branch) for branch in branches]
    if not all(prs):
        raise RuntimeError(f"failed to create merge fixture PRs: {prs}")
    return {
        "root": root,
        "branches": branches,
        "prs": prs,
        "bottom_pr": int(prs[0]["number"]),
        "top_pr": int(prs[1]["number"]),
        "fixture_head": git(repo, "rev-parse", "HEAD"),
    }


def prepare_link(
    repo: Path, run_id: str, prefix: str, base_branch: str
) -> dict[str, Any]:
    root = f"eval/{run_id}"
    branches = [f"{prefix}/base", f"{prefix}/top"]

    git(repo, "checkout", "-q", "-b", branches[0])
    write(repo, f"{root}/base.js", "module.exports = { value: 42 };\n")
    base_sha = commit(repo, f"{run_id}: add link base", f"{root}/base.js")

    git(repo, "checkout", "-q", "-b", branches[1])
    write(
        repo,
        f"{root}/top.js",
        "const base = require('./base');\nmodule.exports = () => base.value;\n",
    )
    top_sha = commit(repo, f"{run_id}: add link top", f"{root}/top.js")
    return {
        "root": root,
        "branches": branches,
        "fixture_shas": [base_sha, top_sha],
        "fixture_head": top_sha,
    }


def prepare_split(
    repo: Path,
    run_id: str,
    prefix: str,
    source: str,
    base_branch: str,
) -> dict[str, Any]:
    """Build one large multi-concern diff to be split into a stack.

    Layers must be model -> api -> ui by import dependency so ordering is gradable.
    """
    root = f"eval/{run_id}"
    # Base commit contains only the legacy helper the work will later delete.
    write(repo, f"{root}/legacy.js", "module.exports = { old: true };\n")
    git(repo, "add", "--", root)
    git(repo, "commit", "-m", f"{run_id}: add legacy helper")
    base_sha = git(repo, "rev-parse", "HEAD")

    # The large change: three dependent modules, a new note, and the legacy deletion.
    write(
        repo,
        f"{root}/model.js",
        "module.exports = { normalizeUser: u => ({ ...u, name: String(u.name).trim() }) };\n",
    )
    write(
        repo,
        f"{root}/api.js",
        "const m = require('./model');\n"
        "module.exports = { serializeUser: u => JSON.stringify(m.normalizeUser(u)) };\n",
    )
    write(
        repo,
        f"{root}/page.js",
        "const a = require('./api');\n"
        "module.exports = { renderUser: u => `<div>${a.serializeUser(u)}</div>` };\n",
    )
    write(repo, f"{root}/NOTES.md", f"{run_id} billing notes\n")
    (repo / root / "legacy.js").unlink()

    metadata: dict[str, Any] = {
        "root": root,
        "base_sha": base_sha,
        "base_branch": base_branch,
        "source": source,
        "expected_files": [
            f"{root}/model.js",
            f"{root}/api.js",
            f"{root}/page.js",
            f"{root}/NOTES.md",
            f"{root}/legacy.js",
        ],
    }

    if source == "worktree":
        # Leave everything uncommitted on trunk.
        metadata["source_sha"] = None
        metadata["fixture_head"] = base_sha
        metadata["branches"] = []
        return metadata

    branch = f"{prefix}/big"
    git(repo, "checkout", "-q", "-b", branch)
    git(repo, "add", "-A", "--", root)
    git(repo, "commit", "-m", f"{run_id}: add user model, api, page; drop legacy")
    source_sha = git(repo, "rev-parse", "HEAD")
    metadata["source_sha"] = source_sha
    metadata["source_branch"] = branch
    metadata["fixture_head"] = source_sha
    metadata["branches"] = [branch]

    if source == "pr":
        git(repo, "config", "remote.pushDefault", "origin")
        git(repo, "push", "-q", "origin", branch)
        shell(
            [
                "gh", "pr", "create", "--repo", REPO_SLUG,
                "--base", base_branch, "--head", branch,
                "--title", f"{run_id}: large billing change",
                "--body", "Large single PR that should be split into a stack.",
            ],
            cwd=repo,
            timeout=120,
        )
        pr = list_pr(repo, branch)
        if not pr:
            raise RuntimeError("failed to create split fixture PR")
        metadata["pr"] = pr
        metadata["source_pr"] = int(pr["number"])
    return metadata


def prepare_fixture(
    repo: Path,
    case_name: str,
    run_id: str,
    prefix: str,
    base_branch: str,
) -> dict[str, Any]:
    fixture = CASES[case_name]["fixture"]
    if fixture == "clean":
        return {
            "root": f"eval/{run_id}",
            "base_branch": base_branch,
            "branches": [],
            "fixture_head": git(repo, "rev-parse", "HEAD"),
        }

    if fixture == "reorder":
        return prepare_reorder(repo, run_id, prefix, base_branch)
    if fixture == "merge":
        return prepare_merge(repo, run_id, prefix, base_branch)
    if fixture == "link":
        return prepare_link(repo, run_id, prefix, base_branch)
    if fixture.startswith("split-"):
        return prepare_split(
            repo,
            run_id,
            prefix,
            fixture.split("-", 1)[1],
            base_branch,
        )

    metadata = prepare_three_layer(repo, run_id, prefix, base_branch)
    if fixture == "multi-remote":
        git(repo, "remote", "add", "backup", git(repo, "remote", "get-url", "origin"))
        git(repo, "config", "--local", "--unset-all", "remote.pushDefault", check=False)
        metadata["fixture_head"] = git(repo, "rev-parse", "HEAD")
    return metadata


def render_prompt(
    case_name: str,
    run_id: str,
    prefix: str,
    metadata: dict[str, Any] | None = None,
) -> str:
    values = metadata or {}
    return CASES[case_name]["prompt"].format(
        run_id=run_id,
        branch_prefix=prefix,
        top_pr=values.get("top_pr", ""),
        source_pr=values.get("source_pr", ""),
        default_branch=values.get("base_branch", DEFAULT_BRANCH),
    )


def run_agent(
    run_dir: Path,
    repo: Path,
    home: Path,
    model_key: str,
    prompt: str,
    timeout_seconds: int,
) -> dict[str, Any]:
    model, model_flags = MODELS[model_key]
    events = run_dir / "events.jsonl"
    stderr = run_dir / "copilot.stderr"
    gh_token = (
        os.environ.get("GH_STACK_EVAL_GITHUB_TOKEN")
        or os.environ.get("GH_TOKEN")
        or shell(["gh", "auth", "token"]).stdout.strip()
    )
    copilot_token = os.environ.get("COPILOT_GITHUB_TOKEN") or gh_token
    env = {
        **os.environ,
        "HOME": str(home),
        "COPILOT_GITHUB_TOKEN": copilot_token,
        "GH_TOKEN": gh_token,
        "GITHUB_TOKEN": gh_token,
        "GIT_TERMINAL_PROMPT": "0",
        "GH_PROMPT_DISABLED": "1",
        "CI": "1",
        "NO_COLOR": "1",
    }
    command = [
        "copilot",
        "-C",
        str(repo),
        "--no-custom-instructions",
        "--disable-builtin-mcps",
        "--allow-all-tools",
        "--allow-all-paths",
        "--allow-all-urls",
        "--no-ask-user",
        "--autopilot",
        "--max-autopilot-continues",
        "10",
        "--model",
        model,
        *model_flags,
        "--output-format",
        "json",
        "-p",
        prompt,
    ]

    started = time.time()
    timed_out = False
    with events.open("w") as out, stderr.open("w") as err:
        process = subprocess.Popen(
            command,
            cwd=repo,
            env=env,
            text=True,
            stdout=out,
            stderr=err,
            start_new_session=True,
        )
        try:
            return_code = process.wait(timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            return_code = 124

    return {
        "return_code": return_code,
        "timed_out": timed_out,
        "duration_seconds": round(time.time() - started, 3),
    }


def load_events(path: Path) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    if not path.exists():
        return events
    for line in path.read_text(errors="replace").splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return events


def telemetry(run_dir: Path, home: Path) -> dict[str, Any]:
    events = load_events(run_dir / "events.jsonl")
    requests: list[dict[str, Any]] = []
    final_messages: list[str] = []
    completed_tools: list[dict[str, Any]] = []
    for event in events:
        event_type = event.get("type")
        data = event.get("data", {})
        if event_type == "assistant.message":
            requests.extend(data.get("toolRequests") or [])
            if data.get("content"):
                final_messages.append(data["content"])
        elif event_type == "tool.execution_complete":
            completed_tools.append(data)

    bash_commands = [
        request.get("arguments", {}).get("command", "")
        for request in requests
        if request.get("name") == "bash"
    ]
    vc_shell_calls = sum(
        bool(re.search(r"(^|[;&|]\s*)(git|gh\s+(stack|pr))\b", command))
        for command in bash_commands
    )
    skill_invoked = any(
        request.get("name") == "skill"
        and request.get("arguments", {}).get("skill") == "gh-stack"
        for request in requests
    )
    references: set[str] = set()
    for request in requests:
        raw = json.dumps(request.get("arguments", {}))
        for name in ("commands.md", "stack-design.md", "troubleshooting.md"):
            if name in raw:
                references.add(name)

    input_tokens = 0
    output_tokens = 0
    model_calls = 0
    db = home / ".copilot/session-store.db"
    if db.exists():
        connection = sqlite3.connect(db)
        try:
            row = connection.execute(
                "SELECT COUNT(*), COALESCE(SUM(input_tokens),0), "
                "COALESCE(SUM(output_tokens),0) "
                "FROM assistant_usage_events"
            ).fetchone()
            model_calls = int(row[0])
            input_tokens, output_tokens = int(row[1]), int(row[2])
        finally:
            connection.close()

    tool_output_bytes = sum(
        len(
            str(data.get("result", {}).get("content", "")).encode(
                errors="replace"
            )
        )
        for data in completed_tools
    )
    return {
        "tool_calls": len(requests),
        "bash_calls": len(bash_commands),
        "vc_shell_calls": vc_shell_calls,
        "bash_commands": bash_commands,
        "failed_tool_calls": sum(
            data.get("success") is False for data in completed_tools
        ),
        "tool_output_bytes": tool_output_bytes,
        "model_calls": model_calls,
        "skill_invoked": skill_invoked,
        "references_opened": sorted(references),
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "final_response": "\n".join(final_messages[-2:]),
    }


def safe_stack_view(repo: Path) -> dict[str, Any] | None:
    result = shell(
        ["gh", "stack", "view", "--json"],
        cwd=repo,
        check=False,
        timeout=90,
    )
    if result.returncode:
        return None
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return None


def file_intro_commit(repo: Path, relative: str) -> str | None:
    result = shell(
        ["git", "log", "--diff-filter=A", "--format=%H", "--", relative],
        cwd=repo,
        check=False,
    )
    lines = result.stdout.splitlines()
    return lines[-1] if lines else None


def branch_index_for_commit(repo: Path, branches: list[str], sha: str | None) -> int | None:
    if not sha:
        return None
    for index, branch in enumerate(branches):
        if shell(
            ["git", "merge-base", "--is-ancestor", sha, branch],
            cwd=repo,
            check=False,
        ).returncode == 0:
            return index
    return None


def command_contains(commands: list[str], *needles: str) -> bool:
    return any(all(needle in command for needle in needles) for command in commands)


def classify_failure(failed: list[str]) -> str | None:
    if not failed:
        return None
    joined = " ".join(failed)
    if "original_pr" in joined:
        return "PR_IDENTITY_LOST"
    if any(
        token in joined
        for token in ("pr_", "github_stack", "remote_", "branches_pushed")
    ):
        return "REMOTE_STATE_WRONG"
    if any(
        token in joined
        for token in (
            "parity",
            "files_covered",
            "deletion",
            "created_at",
            "serializer",
            "layer_contains",
        )
    ):
        return "CONTENT_WRONG"
    if any(
        token in joined
        for token in (
            "layer",
            "stack_order",
            "dependency_order",
            "rebase",
            "ancestor",
        )
    ):
        return "GRAPH_WRONG"
    if any(
        token in joined
        for token in ("clean_worktree", "repository_unchanged", "unchanged")
    ):
        return "DIRTY_STATE_WRONG"
    return "WORKFLOW_WRONG"


def list_pr(repo: Path, branch: str) -> dict[str, Any] | None:
    result = shell(
        [
            "gh",
            "pr",
            "list",
            "--repo",
            REPO_SLUG,
            "--state",
            "open",
            "--head",
            branch,
            "--json",
            "number,headRefName,baseRefName,isDraft,url",
            "--limit",
            "10",
        ],
        cwd=repo,
        check=False,
    )
    try:
        values = json.loads(result.stdout)
    except json.JSONDecodeError:
        return None
    return values[0] if values else None


def stack_contains(repo: Path, pr_numbers: set[int]) -> bool:
    result = shell(
        ["gh", "api", f"repos/{REPO_SLUG}/stacks", "--paginate"],
        cwd=repo,
        check=False,
        timeout=120,
    )
    try:
        stacks = json.loads(result.stdout)
    except json.JSONDecodeError:
        return False
    for stack in stacks:
        numbers = {
            int(item["number"])
            for item in stack.get("pull_requests", [])
            if "number" in item
        }
        if pr_numbers.issubset(numbers):
            return True
    return False


def grade(
    repo: Path,
    case_name: str,
    metadata: dict[str, Any],
    telemetry_data: dict[str, Any],
) -> dict[str, Any]:
    assertions: dict[str, bool] = {}
    details: dict[str, Any] = {}
    status = git(repo, "status", "--porcelain")
    current = git(repo, "branch", "--show-current")
    commands = telemetry_data["bash_commands"]
    view = safe_stack_view(repo)

    if case_name == "preemptive-feature":
        branches = [item["name"] for item in (view or {}).get("branches", [])]
        root = metadata["root"]
        introductions = [
            file_intro_commit(repo, f"{root}/model.js"),
            file_intro_commit(repo, f"{root}/api.js"),
            file_intro_commit(repo, f"{root}/page.js"),
        ]
        indexes = [branch_index_for_commit(repo, branches, sha) for sha in introductions]
        assertions["skill_triggered"] = telemetry_data["skill_invoked"]
        assertions["stack_has_three_layers"] = len(branches) >= 3
        assertions["files_in_dependency_order"] = (
            all(index is not None for index in indexes)
            and len(set(indexes)) == 3
            and indexes == sorted(indexes)
        )
        assertions["clean_worktree"] = not status
        remote_result = shell(
            ["git", "ls-remote", "--heads", "origin", f"{metadata['prefix']}/*"],
            cwd=repo,
            check=False,
            timeout=90,
        )
        assertions["nothing_pushed"] = not remote_result.stdout.strip()
        details.update({"branches": branches, "intro_commits": introductions, "indexes": indexes})

    elif case_name == "read-state":
        assertions["repository_unchanged"] = (
            not status and git(repo, "rev-parse", "HEAD") == metadata["fixture_head"]
        )
        assertions["stayed_on_top"] = current == metadata["branches"][-1]
        assertions["used_view_json"] = command_contains(commands, "gh stack view", "--json")

    elif case_name == "lower-layer-edit":
        root = metadata["root"]
        api_branch = metadata["branches"][1]
        top_branch = metadata["branches"][2]
        api_file = shell(
            ["git", "show", f"{api_branch}:{root}/serializer.js"],
            cwd=repo,
            check=False,
        ).stdout
        top_only_diff = shell(
            ["git", "diff", f"{api_branch}..{top_branch}", "--", f"{root}/serializer.js"],
            cwd=repo,
            check=False,
        ).stdout
        assertions["api_layer_contains_created_at"] = bool(
            re.search(r"\bcreatedAt\s*:", api_file)
        )
        assertions["serializer_not_only_in_top"] = not top_only_diff
        assertions["top_rebased_on_api"] = (
            shell(
                ["git", "merge-base", "--is-ancestor", api_branch, top_branch],
                cwd=repo,
                check=False,
            ).returncode
            == 0
        )
        assertions["finished_on_top"] = current == top_branch
        assertions["clean_worktree"] = not status
        details.update({"api_file": api_file, "top_only_diff": top_only_diff})

    elif case_name == "multi-remote-push":
        remote_heads: dict[str, bool] = {}
        for branch in metadata["branches"]:
            result = shell(
                ["git", "ls-remote", "--exit-code", "--heads", "origin", branch],
                cwd=repo,
                check=False,
                timeout=90,
            )
            remote_heads[branch] = result.returncode == 0
        assertions["all_branches_pushed"] = all(remote_heads.values())
        assertions["remote_disambiguated"] = (
            command_contains(commands, "gh stack push", "--remote", "origin")
            or git(repo, "config", "--get", "remote.pushDefault", check=False) == "origin"
        )
        assertions["clean_worktree"] = not status
        details["remote_heads"] = remote_heads

    elif case_name == "submit-prs":
        branches = [f"{metadata['prefix']}/data", f"{metadata['prefix']}/consumer"]
        prs = [list_pr(repo, branch) for branch in branches]
        assertions["requested_branches_exist"] = all(
            shell(["git", "show-ref", "--verify", f"refs/heads/{branch}"], cwd=repo, check=False).returncode
            == 0
            for branch in branches
        )
        assertions["both_prs_open_ready"] = all(pr and not pr["isDraft"] for pr in prs)
        assertions["dependent_pr_bases"] = bool(
            prs[0]
            and prs[1]
            and prs[0]["baseRefName"] == metadata["base_branch"]
            and prs[1]["baseRefName"] == branches[0]
        )
        numbers = {int(pr["number"]) for pr in prs if pr}
        assertions["one_github_stack"] = len(numbers) == 2 and stack_contains(repo, numbers)
        assertions["clean_worktree"] = not status
        details.update({"branches": branches, "prs": prs})

    elif case_name == "continue-stack":
        original = metadata["branches"]
        added = f"{metadata['prefix']}/audit"
        actual = [item["name"] for item in (view or {}).get("branches", [])]
        audit_path = f"{metadata['root']}/audit.js"
        assertions["new_top_layer"] = actual == [*original, added]
        assertions["audit_file_in_new_layer"] = (
            shell(
                ["git", "cat-file", "-e", f"{added}:{audit_path}"],
                cwd=repo,
                check=False,
            ).returncode
            == 0
            and shell(
                ["git", "cat-file", "-e", f"{original[-1]}:{audit_path}"],
                cwd=repo,
                check=False,
            ).returncode
            != 0
        )
        assertions["new_layer_depends_on_previous_top"] = (
            shell(
                ["git", "merge-base", "--is-ancestor", original[-1], added],
                cwd=repo,
                check=False,
            ).returncode
            == 0
        )
        assertions["existing_layers_unchanged"] = all(
            git(repo, "rev-parse", branch) == sha
            for branch, sha in zip(original, metadata["fixture_shas"])
        )
        assertions["finished_on_new_top"] = current == added
        assertions["clean_worktree"] = not status
        details.update({"expected_order": [*original, added], "actual_order": actual})

    elif case_name == "link-stack":
        branches = metadata["branches"]
        prs = [list_pr(repo, branch) for branch in branches]
        assertions["both_prs_open_ready"] = all(pr and not pr["isDraft"] for pr in prs)
        assertions["dependent_pr_bases"] = bool(
            prs[0]
            and prs[1]
            and prs[0]["baseRefName"] == metadata["base_branch"]
            and prs[1]["baseRefName"] == branches[0]
        )
        numbers = {int(pr["number"]) for pr in prs if pr}
        assertions["one_github_stack"] = len(numbers) == 2 and stack_contains(repo, numbers)
        assertions["no_local_tracking"] = safe_stack_view(repo) is None
        assertions["clean_worktree"] = not status
        details.update({"branches": branches, "prs": prs})

    elif case_name == "reorder-stack":
        root = metadata["root"]
        target = metadata["target_branches"]
        actual = [item["name"] for item in (view or {}).get("branches", [])]

        def exists(branch: str, relative: str) -> bool:
            return (
                shell(
                    ["git", "cat-file", "-e", f"{branch}:{root}/{relative}"],
                    cwd=repo,
                    check=False,
                ).returncode
                == 0
            )

        migration, models, ui = target
        assertions["stack_order"] = actual == target
        assertions["migration_layer_isolated"] = (
            exists(migration, "migration.sql")
            and not exists(migration, "models.js")
            and not exists(migration, "ui.js")
        )
        assertions["models_layer_correct"] = (
            exists(models, "migration.sql")
            and exists(models, "models.js")
            and not exists(models, "ui.js")
        )
        assertions["ui_layer_complete"] = (
            exists(ui, "migration.sql")
            and exists(ui, "models.js")
            and exists(ui, "ui.js")
        )
        assertions["finished_on_top"] = current == ui
        assertions["clean_worktree"] = not status
        details.update({"target_order": target, "actual_order": actual})

    elif case_name == "merge-stack":
        def pr_state(number: int) -> str:
            result = shell(
                [
                    "gh",
                    "pr",
                    "view",
                    str(number),
                    "--repo",
                    REPO_SLUG,
                    "--json",
                    "state",
                    "--jq",
                    ".state",
                ],
                cwd=repo,
                check=False,
                timeout=90,
            )
            return result.stdout.strip()

        states = {
            "bottom": pr_state(metadata["bottom_pr"]),
            "top": pr_state(metadata["top_pr"]),
        }
        assertions["bottom_pr_merged"] = states["bottom"] == "MERGED"
        assertions["top_pr_merged"] = states["top"] == "MERGED"
        assertions["used_stack_merge"] = command_contains(commands, "gh stack merge")
        assertions["used_yes"] = command_contains(commands, "gh stack merge", "--yes")
        details["pr_states"] = states

    elif case_name.startswith("split-"):
        root = metadata["root"]
        base_sha = metadata["base_sha"]
        src = metadata.get("source_sha")
        branches = [item["name"] for item in (view or {}).get("branches", [])]
        top = branches[-1] if branches else None

        def has_file(ref: str, path: str) -> bool:
            return shell(
                ["git", "cat-file", "-e", f"{ref}:{path}"], cwd=repo, check=False
            ).returncode == 0

        def changed_files(a: str, b: str) -> set[str]:
            out = shell(
                ["git", "diff", "--name-only", a, b], cwd=repo, check=False
            ).stdout
            return {line for line in out.splitlines() if line.strip()}

        def introducing_layer(path: str) -> int | None:
            """Index of the lowest layer whose tree contains path."""
            for index, branch in enumerate(branches):
                if has_file(branch, path):
                    return index
            return None

        assertions["stack_has_layers"] = len(branches) >= 3
        details["branches"] = branches

        # 1. Parity: the top of the stack reproduces the original work exactly.
        if not top:
            assertions["parity_with_original"] = False
        elif src:
            parity = shell(
                ["git", "diff", "--name-status", src, top], cwd=repo, check=False
            ).stdout.strip()
            assertions["parity_with_original"] = parity == ""
            details["parity_diff"] = parity
        else:
            # Worktree source has no commit to compare against; assert final content.
            mismatches = [
                path
                for path in metadata["expected_files"]
                if has_file(top, path) != (not path.endswith("legacy.js"))
            ]
            assertions["parity_with_original"] = not mismatches
            details["parity_mismatches"] = mismatches

        # 2. Coverage: every added/modified file is present, and the deletion happened.
        if top:
            missing = [
                path
                for path in metadata["expected_files"]
                if not path.endswith("legacy.js") and not has_file(top, path)
            ]
            assertions["all_files_covered"] = not missing
            assertions["deletion_applied"] = not has_file(top, f"{root}/legacy.js")
            details["missing_files"] = missing
        else:
            assertions["all_files_covered"] = False
            assertions["deletion_applied"] = False

        # 3. Ordering: model at or below api, api at or below page, and truly split.
        order = [
            introducing_layer(f"{root}/model.js"),
            introducing_layer(f"{root}/api.js"),
            introducing_layer(f"{root}/page.js"),
        ]
        assertions["dependency_order"] = (
            all(v is not None for v in order)
            and order[0] <= order[1] <= order[2]
            and len(set(order)) >= 2
        )
        details["layer_indexes"] = order

        # 4. Real layering: every layer adds something over its parent.
        nonempty = [
            bool(changed_files(base_sha if i == 0 else branches[i - 1], branch))
            for i, branch in enumerate(branches)
        ]
        assertions["no_empty_layers"] = bool(nonempty) and all(nonempty)
        details["layer_nonempty"] = nonempty

        assertions["clean_worktree"] = not status

        if case_name == "split-open-pr":
            state = shell(
                [
                    "gh", "pr", "view", str(metadata["source_pr"]), "--repo", REPO_SLUG,
                    "--json", "state", "--jq", ".state",
                ],
                cwd=repo,
                check=False,
                timeout=90,
            ).stdout.strip()
            reused = metadata.get("source_branch") in branches
            assertions["original_pr_resolved"] = state == "CLOSED" or reused
            details["source_pr_state"] = state
            details["source_branch_reused"] = reused

    core_assertions = dict(assertions)
    if metadata["arm"] == "none":
        core_assertions.pop("skill_triggered", None)
    passed = bool(core_assertions) and all(core_assertions.values())
    failed = [name for name, value in core_assertions.items() if not value]
    return {
        "passed": passed,
        "failure_class": classify_failure(failed),
        "failed_assertions": failed,
        "assertions": assertions,
        "details": details,
        "current_branch": current,
        "git_status": status,
        "stack_view": view,
    }


def cleanup(repo: Path, case_name: str, metadata: dict[str, Any]) -> dict[str, Any]:
    errors: list[str] = []
    branches = metadata.get("branches", [])

    def delete_remote(branch: str) -> None:
        outcome = shell(
            ["git", "push", "origin", "--delete", branch],
            cwd=repo,
            check=False,
            timeout=90,
        )
        if (
            outcome.returncode
            and "remote ref does not exist" not in outcome.stderr
            and "unable to delete" not in outcome.stderr
        ):
            errors.append(outcome.stderr.strip())

    if case_name.startswith("split-"):
        # Close any PR the agent opened, plus the fixture PR, then drop remote branches.
        numbers: set[int] = set()
        if metadata.get("source_pr"):
            numbers.add(int(metadata["source_pr"]))
        listed = shell(
            [
                "gh", "pr", "list", "--repo", REPO_SLUG, "--state", "open",
                "--search", metadata["prefix"], "--json", "number,headRefName",
                "--limit", "50",
            ],
            cwd=repo,
            check=False,
            timeout=120,
        ).stdout
        try:
            for item in json.loads(listed or "[]"):
                if str(item.get("headRefName", "")).startswith(metadata["prefix"]):
                    numbers.add(int(item["number"]))
        except json.JSONDecodeError:
            pass
        for number in sorted(numbers):
            outcome = shell(
                ["gh", "pr", "close", str(number), "--repo", REPO_SLUG],
                cwd=repo,
                check=False,
                timeout=90,
            )
            if outcome.returncode and "already closed" not in outcome.stderr.lower():
                errors.append(outcome.stderr.strip())
        remote_refs = shell(
            ["git", "ls-remote", "--heads", "origin", f"{metadata['prefix']}/*"],
            cwd=repo,
            check=False,
            timeout=90,
        ).stdout
        for line in remote_refs.splitlines():
            ref = line.split("refs/heads/")[-1].strip()
            if ref:
                delete_remote(ref)
        delete_remote(metadata["base_branch"])
        return {"cleanup_errors": [e for e in errors if e]}

    if case_name in {"submit-prs", "merge-stack", "link-stack"}:
        branches = [f"{metadata['prefix']}/data", f"{metadata['prefix']}/consumer"]
        if case_name == "merge-stack":
            branches = metadata["branches"]
        if case_name == "link-stack":
            branches = metadata["branches"]
        prs = [list_pr(repo, branch) for branch in branches]
        if case_name == "merge-stack":
            prs = metadata["prs"]
        numbers = {int(pr["number"]) for pr in prs if pr}
        if numbers and case_name != "merge-stack":
            result = shell(
                ["gh", "api", f"repos/{REPO_SLUG}/stacks", "--paginate"],
                cwd=repo,
                check=False,
            )
            try:
                stacks = json.loads(result.stdout)
            except json.JSONDecodeError:
                stacks = []
            for stack in stacks:
                stack_prs = {
                    int(item["number"])
                    for item in stack.get("pull_requests", [])
                    if "number" in item
                }
                if numbers.issubset(stack_prs):
                    outcome = shell(
                        ["gh", "stack", "unstack", str(stack["number"])],
                        cwd=repo,
                        check=False,
                        timeout=120,
                    )
                    if outcome.returncode:
                        errors.append(outcome.stderr.strip())
                    break
            for number in sorted(numbers):
                state = shell(
                    [
                        "gh",
                        "pr",
                        "view",
                        str(number),
                        "--repo",
                        REPO_SLUG,
                        "--json",
                        "state",
                        "--jq",
                        ".state",
                    ],
                    cwd=repo,
                    check=False,
                ).stdout.strip()
                if state == "MERGED":
                    continue
                outcome = shell(
                    ["gh", "pr", "close", str(number), "--repo", REPO_SLUG],
                    cwd=repo,
                    check=False,
                    timeout=90,
                )
                if outcome.returncode:
                    errors.append(outcome.stderr.strip())

    if case_name in {"multi-remote-push", "submit-prs", "merge-stack", "link-stack"}:
        for branch in reversed(branches):
            delete_remote(branch)
    delete_remote(metadata["base_branch"])
    return {"cleanup_errors": [error for error in errors if error]}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=sorted(CASES))
    parser.add_argument("--arm", choices=sorted(ARMS))
    parser.add_argument("--model", choices=sorted(MODELS))
    parser.add_argument("--iteration", default="1", help="Run ID prefix")
    parser.add_argument(
        "--skill-path",
        type=Path,
        help="Override the skill directory for the current configuration",
    )
    parser.add_argument(
        "--skill-ref",
        help="Load skills/gh-stack from a git commit, tag, or branch",
    )
    parser.add_argument("--list", action="store_true", help="List eval cases and exit")
    parser.add_argument(
        "--keep-remote",
        action="store_true",
        help="Skip cleanup for debugging",
    )
    args = parser.parse_args()

    if args.list:
        for case in CASES.values():
            print(f"{case['name']}\t{case['tier']}\t{case['fixture']}")
        return 0
    if not args.case:
        parser.error("--case is required unless --list is used")
    if not args.arm or not args.model:
        parser.error("--arm and --model are required")
    if args.skill_path and args.skill_ref:
        parser.error("--skill-path and --skill-ref are mutually exclusive")

    configure()
    suffix = uuid.uuid4().hex[:6]
    run_id = f"{args.iteration}-{args.case[:6]}-{args.arm[:3]}-{args.model}-{suffix}"
    branch_prefix = f"eval/{run_id}"
    run_dir = RESULTS_ROOT / run_id
    run_dir.mkdir(parents=True)

    metadata: dict[str, Any] = {
        "run_id": run_id,
        "case": args.case,
        "tier": CASES[args.case]["tier"],
        "network": CASES[args.case]["network"],
        "arm": args.arm,
        "model": args.model,
        "skill_path": str(args.skill_path) if args.skill_path else None,
        "skill_ref": args.skill_ref,
        "prefix": branch_prefix,
    }
    (run_dir / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")

    repo: Path | None = None
    try:
        repo = prepare_repo(
            run_dir,
            args.arm,
            args.skill_path,
            args.skill_ref,
        )
        metadata["fixture_seed_commit"] = git(repo, "rev-parse", DEFAULT_BRANCH)
        installed_skill = (
            repo / ".agents/skills/gh-stack"
            if args.arm != "none"
            else None
        )
        skill_source = (
            f"git:{args.skill_ref}"
            if args.skill_ref
            else f"path:{args.skill_path.resolve()}"
            if args.skill_path
            else "working-tree"
            if args.arm == "current"
            else "none"
        )
        metadata["provenance"] = provenance(
            args.case,
            installed_skill,
            args.skill_ref,
            skill_source,
            args.model,
        )
        home = prepare_home(run_dir)
        base_branch = create_eval_base(repo, run_id)
        metadata["base_branch"] = base_branch
        fixture = prepare_fixture(
            repo,
            args.case,
            run_id,
            branch_prefix,
            base_branch,
        )
        metadata.update(fixture)
        metadata["prompt"] = render_prompt(
            args.case,
            run_id,
            branch_prefix,
            metadata,
        )
        (run_dir / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")

        execution = run_agent(
            run_dir,
            repo,
            home,
            args.model,
            metadata["prompt"],
            int(CASES[args.case].get("timeout_seconds", 360)),
        )
        telemetry_data = telemetry(run_dir, home)
        grade_data = grade(repo, args.case, metadata, telemetry_data)
        cleanup_data = (
            {"cleanup_errors": []}
            if args.keep_remote
            else cleanup(repo, args.case, metadata)
        )
        result = {
            **metadata,
            "status": "PASS" if grade_data["passed"] else "FAIL",
            "execution": execution,
            "telemetry": telemetry_data,
            "grade": grade_data,
            "cleanup": cleanup_data,
        }
    except Exception as error:
        cleanup_data = {"cleanup_errors": []}
        if repo and metadata.get("base_branch") and not args.keep_remote:
            try:
                cleanup_data = cleanup(repo, args.case, metadata)
            except Exception as cleanup_error:
                cleanup_data = {
                    "cleanup_errors": [
                        f"{type(cleanup_error).__name__}: {cleanup_error}"
                    ]
                }
        result = {
            **metadata,
            "status": "INFRA_FAILURE",
            "fatal_error": f"{type(error).__name__}: {error}",
            "cleanup": cleanup_data,
        }

    (run_dir / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))
    return 0 if result.get("grade", {}).get("passed") else 1


if __name__ == "__main__":
    sys.exit(main())
