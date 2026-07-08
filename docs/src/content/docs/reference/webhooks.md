---
title: Webhooks
description: Reference for the stack object in pull_request webhook event payloads.
---

When a pull request belongs to a stack, GitHub adds a `stack` property to the `pull_request` object in webhook event payloads. This lets apps and integrations inspect the stack's ultimate target branch — not just the direct parent branch of the PR.

The `stack` object is included in the `pull_request` webhook payload for all [pull request lifecycle events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request) whenever the pull request is part of a stack.



## The `stack` Object

The `stack` object is nested inside the `pull_request` object. It identifies the stack, describes this PR's place within it, and reports the stack's base branch and ultimate merge target:

```json
{
  "action": "synchronize",
  "pull_request": {
    "number": 42,
    "title": "Add API routes",
    "base": {
      "ref": "feat/auth-layer",
      "sha": "abc123..."
    },
    "stack": {
      "id": 123456,
      "number": 50,
      "size": 5,
      "position": 2,
      "base": {
        "ref": "main",
        "sha": "def456..."
      }
    }
  }
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `pull_request.stack.id` | `integer` | Global identifier for the stack. |
| `pull_request.stack.number` | `integer` | The stack's number, scoped to the repository. |
| `pull_request.stack.size` | `integer` | Total number of pull requests in the stack. |
| `pull_request.stack.position` | `integer` | 1-based position of this PR within the stack, where `1` is the bottom (the PR closest to the stack's base). |
| `pull_request.stack.base.ref` | `string` | The branch the entire stack ultimately targets (e.g., `main`). |
| `pull_request.stack.base.sha` | `string` | The HEAD SHA of the stack's base branch. |

`pull_request.base.ref` is the direct parent branch of an individual PR (the branch below it in the stack), while `pull_request.stack.base.ref` is the ultimate target of the entire stack. These differ for all PRs in the stack except the bottom one.

The `stack` object is **only present** when the pull request belongs to a stack. For standalone PRs, the field is null.

## GitHub Actions

GitHub Actions automatically evaluates workflow triggers using the stack's base branch. If a PR is part of a stack targeting `main`, any workflow configured to run on pull requests targeting `main` will run for every PR in the stack — no workflow changes are required.

The `stack` object is also available in GitHub Actions workflow expressions via `github.event.pull_request.stack`. See [How do I access stack metadata in my GitHub Actions workflow?](/gh-stack/faq/#how-do-i-access-stack-metadata-in-my-github-actions-workflow) in the FAQ for examples.
