---
title: Webhooks
description: Reference for the stacked action and stack object in pull_request webhook event payloads.
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

## The `stacked` Event

GitHub delivers the `pull_request` event with the `stacked` action when a pull request is **added to a stack**. Because a PR is created before it joins a stack, this is the event to listen for when you need to know exactly when a PR becomes part of a stack.

| | |
|---|---|
| **Event** (`X-GitHub-Event` header) | `pull_request` |
| **Action** | `stacked` |
| **Fires when** | A pull request is added to a stack |

The `stacked` payload surfaces the joined stack as a **top-level `stack` object**, in addition to the `stack` nested under `pull_request`. The two objects use the same [fields](#fields) and always match, so you can read either one.

```json
{
  "action": "stacked",
  "number": 42,
  "stack": {
    "id": 123456,
    "number": 50,
    "size": 5,
    "position": 2,
    "base": {
      "ref": "main",
      "sha": "def456..."
    }
  },
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

The top-level `stack` object is unique to the `stacked` event; other `pull_request` actions (such as `opened` or `synchronize`) only carry the `stack` nested inside `pull_request`.

## GitHub Actions

GitHub Actions automatically evaluates workflow triggers using the stack's base branch. If a PR is part of a stack targeting `main`, any workflow configured to run on pull requests targeting `main` will run for every PR in the stack — no workflow changes are required.

The `stack` object is also available in GitHub Actions workflow expressions via `github.event.pull_request.stack`. See [How do I access stack metadata in my GitHub Actions workflow?](/gh-stack/faq/#how-do-i-access-stack-metadata-in-my-github-actions-workflow) in the FAQ for examples.
