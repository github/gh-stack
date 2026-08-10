"""Load declarative eval case contracts."""

from __future__ import annotations

import json
from pathlib import Path


EVAL_ROOT = Path(__file__).resolve().parent
CASES_ROOT = EVAL_ROOT / "cases"
REQUIRED_FIELDS = {
    "name",
    "title",
    "summary",
    "tier",
    "fixture",
    "required",
    "network",
    "timeout_seconds",
    "assertions",
}


def load_cases(root: Path = CASES_ROOT) -> dict[str, dict]:
    cases = {}
    for path in sorted(root.glob("*/case.json")):
        case = json.loads(path.read_text())
        missing = REQUIRED_FIELDS - set(case)
        if missing:
            raise ValueError(
                f"{path} is missing fields: {', '.join(sorted(missing))}"
            )
        prompt_path = path.parent / "prompt.md"
        if not prompt_path.is_file():
            raise ValueError(f"missing prompt: {prompt_path}")
        case["prompt"] = prompt_path.read_text().strip()
        if not case["prompt"] or not case["assertions"]:
            raise ValueError(f"{path} requires a prompt and assertions")
        if int(case["timeout_seconds"]) <= 0:
            raise ValueError(f"{path} timeout_seconds must be positive")
        case["contract_path"] = str(path)
        name = case["name"]
        if name in cases:
            raise ValueError(f"duplicate eval case: {name}")
        cases[name] = case
    if not cases:
        raise ValueError(f"no eval cases found under {root}")
    return cases
