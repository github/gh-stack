from __future__ import annotations

import importlib.util
import json
import unittest
from pathlib import Path


EVAL_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = EVAL_ROOT.parent


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader
    spec.loader.exec_module(module)
    return module


runner = load_module("eval_runner", EVAL_ROOT / "runner.py")
aggregate = load_module("eval_aggregate", EVAL_ROOT / "aggregate.py")


class EvalConfigurationTest(unittest.TestCase):
    def test_corpus_has_at_least_twelve_unique_cases(self):
        cases = json.loads((EVAL_ROOT / "cases.json").read_text())
        names = [case["name"] for case in cases]
        self.assertGreaterEqual(len(cases), 12)
        self.assertEqual(len(names), len(set(names)))

    def test_every_case_has_required_fields_and_renderable_prompt(self):
        for case in runner.CASES.values():
            with self.subTest(case=case["name"]):
                self.assertTrue(case["tier"])
                self.assertTrue(case["fixture"])
                self.assertTrue(case["assertions"])
                prompt = runner.render_prompt(
                    case["name"],
                    "test-run",
                    "eval/test-run",
                    {
                        "top_pr": 123,
                        "source_pr": 456,
                        "base_branch": "eval-base/test-run",
                    },
                )
                self.assertNotIn("{", prompt)
                self.assertNotIn("}", prompt)

    def test_public_cases_cover_core_workflows(self):
        expected = {
            "read-state",
            "lower-layer-edit",
            "multi-remote-push",
            "submit-prs",
            "reorder-stack",
            "merge-stack",
            "split-worktree",
            "split-branch",
            "split-open-pr",
            "continue-stack",
            "link-stack",
        }
        self.assertTrue(expected.issubset(runner.CASES))

    def test_only_preemptive_trigger_is_informational(self):
        informational = {
            name
            for name, case in runner.CASES.items()
            if not case.get("required", True)
        }
        self.assertEqual(informational, {"preemptive-feature"})

    def test_current_skill_exists(self):
        self.assertTrue((REPO_ROOT / "skills/gh-stack/SKILL.md").is_file())

    def test_current_skill_reference_links_resolve(self):
        skill_dir = REPO_ROOT / "skills/gh-stack"
        text = (skill_dir / "SKILL.md").read_text()
        for name in ("commands.md", "stack-design.md", "troubleshooting.md"):
            self.assertIn(f"references/{name}", text)
            self.assertTrue((skill_dir / "references" / name).is_file())

    def test_aggregate_handles_empty_groups(self):
        summary = aggregate.summarize(
            [
                {
                    "case": "example",
                    "tier": "happy",
                    "model": "mini",
                    "arm": "current",
                    "grade": {"passed": True},
                    "execution": {"timed_out": False},
                    "telemetry": {
                        "tool_calls": 2,
                        "input_tokens": 10,
                        "skill_invoked": True,
                    },
                }
            ]
        )
        self.assertEqual(summary["by_arm"]["current"]["passed"], 1)
        self.assertEqual(summary["by_case_arm"]["example"]["current"]["total"], 1)


if __name__ == "__main__":
    unittest.main()
