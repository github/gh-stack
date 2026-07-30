---
title: REST API
description: Reference for the Stacks REST API and the stack object on pull request resources.
---

GitHub exposes stacks through the REST API in two ways:

1. **A `stack` object on pull request resources** — every pull request returned by the REST API carries a `stack` object describing its stack membership when it belongs to one.
2. **A dedicated Stacks API** — endpoints to list, read, create, extend, and dissolve stacks directly.

## The `stack` object on Pull Requests

When a pull request belongs to a stack, GitHub includes a `stack` object on the pull request resource. This lets you read a PR's stack membership — the stack it belongs to, its size, and this PR's position within it — directly from the pull request, without a separate lookup.

The `stack` object is present on every REST endpoint that returns a pull request, including:

| Endpoint | Description |
|----------|-------------|
| `GET /repos/{owner}/{repo}/pulls` | List pull requests |
| `GET /repos/{owner}/{repo}/pulls/{pull_number}` | Get a pull request |

```sh
gh api /repos/OWNER/REPO/pulls/42 --jq '.stack'
```

```json
{
  "id": 123456,
  "number": 50,
  "size": 5,
  "position": 2,
  "base": {
    "ref": "main",
    "sha": "def456..."
  }
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `stack.id` | `integer` | Global identifier for the stack. |
| `stack.number` | `integer` | The stack's number, scoped to the repository (shown in the GitHub UI). |
| `stack.size` | `integer` | Total number of pull requests in the stack. |
| `stack.position` | `integer` | 1-based position of this PR within the stack, where `1` is the bottom (the PR closest to the stack's base). |
| `stack.base.ref` | `string` | The branch the entire stack ultimately targets (e.g., `main`). |
| `stack.base.sha` | `string` | The HEAD SHA of the stack's base branch. |

The pull request's own `base.ref` is the branch it directly targets (the PR below it in the stack), while `stack.base.ref` is the ultimate target of the entire stack. These differ for every PR in the stack except the bottom one.

The `stack` object is **only present** when the pull request belongs to a stack. For standalone PRs, the field is `null`.

The same object is delivered on `pull_request` webhook events. See the [Webhooks reference](/gh-stack/reference/webhooks/) for details.

## The Stacks API

The Stacks API provides endpoints to read and manage stacks directly. A stack is addressed by its **stack number** — the repository-scoped number shown in the GitHub UI (the same value as `stack.number` on a pull request).

If stacked PRs are not enabled for the repository, these endpoints return `404 Not Found`.

### List stacks

```
GET /repos/{owner}/{repo}/stacks
```

Lists the stacks in a repository, ordered by stack number (newest first).

| Parameter | In | Type | Description |
|-----------|-----|------|-------------|
| `pull_request` | query | `integer` | Filter to the stack containing this pull request number. |
| `per_page` | query | `integer` | Results per page (max 100). |
| `page` | query | `integer` | Page number of the results. |

```sh
# All stacks in the repository
gh api repos/OWNER/REPO/stacks

# The stack containing PR #102
gh api "repos/OWNER/REPO/stacks?pull_request=102"
```

```json
[
  {
    "id": 9876543,
    "number": 42,
    "node_id": "S_kwDOABCDEF4AAAAA",
    "url": "https://api.github.com/repos/octocat/hello-world/stacks/42",
    "base": { "ref": "main" },
    "open": true,
    "created_at": "2026-04-15T10:00:00Z",
    "pull_requests": [
      {
        "number": 101,
        "state": "open",
        "draft": false,
        "merged_at": null,
        "head": { "ref": "user-model", "sha": "aaa1111..." }
      },
      {
        "number": 102,
        "state": "open",
        "draft": false,
        "merged_at": null,
        "head": { "ref": "user-api", "sha": "bbb2222..." }
      }
    ]
  }
]
```

### Get a stack

```
GET /repos/{owner}/{repo}/stacks/{stack_number}
```

Returns a single stack by its stack number.

```sh
gh api repos/OWNER/REPO/stacks/42
```

### Create a stack

```
POST /repos/{owner}/{repo}/stacks
```

Creates a stack from an ordered list of pull request numbers, from the **bottom of the stack to the top**. Each pull request's base ref must match the previous pull request's head ref, forming a valid chain. Returns `201 Created` with the new stack.

| Body field | Type | Description |
|------------|------|-------------|
| `pull_requests` | `array[integer]` | Ordered pull request numbers, bottom to top. Minimum 2, maximum 100. |

```sh
echo '{"pull_requests": [101, 102, 103]}' | \
  gh api --method POST repos/OWNER/REPO/stacks --input -
```

### Add pull requests to a stack

```
POST /repos/{owner}/{repo}/stacks/{stack_number}/add
```

Appends pull requests onto the **top** of an existing stack. Provide only the pull requests you want to add (the delta), from the current top of the stack upward. The first new pull request's base ref must match the current top pull request's head ref. Returns `200 OK` with the updated stack.

| Body field | Type | Description |
|------------|------|-------------|
| `pull_requests` | `array[integer]` | Ordered pull request numbers to append, from the current top upward. Minimum 1, maximum 100. |

```sh
echo '{"pull_requests": [104]}' | \
  gh api --method POST repos/OWNER/REPO/stacks/42/add --input -
```

### Unstack

```
POST /repos/{owner}/{repo}/stacks/{stack_number}/unstack
```

Removes the unmerged pull requests from a stack. This endpoint takes no request body. Pull requests that cannot be unstacked (those merged, merging, or queued for merge) are left in place.

- When pull requests remain in the stack, the updated stack is returned with `200 OK`.
- When no pull requests remain, the stack is dissolved and `204 No Content` is returned.

```sh
gh api --method POST repos/OWNER/REPO/stacks/42/unstack
```

### The stack resource

Each stack is represented by the following resource. The get, create, and add endpoints return a single stack; the list endpoint returns an array of them; and unstack returns the remaining merged stack, or `204 No Content` when the stack is dissolved.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `integer` | Global identifier for the stack. |
| `number` | `integer` | The stack's number, scoped to the repository. Used to address the stack in these endpoints. |
| `node_id` | `string` | Global node ID for the stack. |
| `url` | `string` | The API URL of the stack. |
| `base.ref` | `string` | The branch the stack targets (e.g., `main`). |
| `open` | `boolean` | Whether the stack has any open pull request. `false` when all pull requests are merged or closed. |
| `created_at` | `string` | Timestamp when the stack was created (ISO 8601). |
| `pull_requests` | `array` | The pull requests in the stack, ordered from bottom to top. |

Each entry in `pull_requests` is a minimal pull request representation:

| Field | Type | Description |
|-------|------|-------------|
| `number` | `integer` | The pull request number. |
| `state` | `string` | `open` or `closed`. |
| `draft` | `boolean` | Whether the pull request is a draft. |
| `merged_at` | `string \| null` | Timestamp when the pull request was merged, or `null` if not merged. |
| `head.ref` | `string` | The head branch of the pull request. |
| `head.sha` | `string` | The HEAD SHA of that branch. |
