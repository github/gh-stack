---
title: Merge API
description: Reference for the asynchronous merge API — the required method for merging stacked pull requests.
---

Stacked pull requests are merged through a new **asynchronous merge API**. Because a stack merge can involve several pull requests that may take up to a few minutes to merge, the merge runs in the background: you submit a merge request and then poll for its result.

This is the **required method for merging stacked PRs**. A stack cannot be merged with the legacy synchronous [merge endpoints](https://docs.github.com/rest/pulls/pulls#merge-a-pull-request) or [mutations](https://docs.github.com/en/graphql/reference/pulls#mutation-mergepullrequest). When you merge a stacked pull request, every pull request in the stack up to and including the one you request is merged into the base branch.

:::caution[Private Preview]
Stacked PRs is currently in private preview. These endpoints are only available for repositories where the feature is enabled. [Sign up for the waitlist →](https://gh.io/stacksbeta)
:::

## How it works

Merging is a two-step flow:

1. **Submit** a merge request with `PUT .../merge-async`. The response contains a `uuid` identifying the request.
2. **Poll** for the result with `GET .../merge-async/{uuid}` until the `status` is no longer `pending`.

Only basic pull request state is checked when you submit (the PR must be open and not a draft). Branch protection and repository rules are evaluated later, when the merge actually runs, and a rule failure is reported as a `failed` result while polling. A stack merge request is **atomic**: either the whole group of pull requests lands (or is added to the merge queue), or none of it does.

## Submit a merge request

```
PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge-async
```

Merges the pull request (and, for a stacked PR, everything below it in the stack) into the base branch in the background. Returns a `uuid` used to fetch the result.

All body fields are optional.

| Body field | Type | Description |
|------------|------|-------------|
| `merge_method` | `string` | The merge method: `merge`, `squash`, or `rebase`. Defaults to a merge commit. |
| `merge_action` | `string` | How to merge: `default` (recommended), `direct_merge`, or `merge_queue`. `default` picks the most appropriate option — it merges directly, or adds the stack to the base branch's merge queue when the branch requires one. `direct_merge` forces a direct merge; `merge_queue` forces the merge queue, if available. |
| `commit_title` | `string` | Title for the automatic commit message. Not supported on `merge_queue` merge actions. |
| `commit_message` | `string` | Extra detail to append to the automatic commit message. Not supported on `merge_queue` merge actions. |
| `sha` | `string` | SHA that the pull request head must match to allow the merge. If the PR head does not match the provided SHA, the merge is cancelled. |

```sh
echo '{"merge_method": "squash", "merge_action": "default"}' | \
  gh api --method PUT repos/OWNER/REPO/pulls/102/merge-async --input -
```

### Responses

| Status | When | Body `status` |
|--------|------|---------------|
| `202 Accepted` | The merge request was accepted and will run in the background. | `pending` |
| `200 OK` | The pull request was already merged. | `merged` |
| `409 Conflict` | A merge request already exists for this pull request. The existing request's `uuid` is returned — its options may differ from those you requested. | `pending` |
| `400 Bad Request` | The pull request is not ready to be merged (for example, it is closed or a draft). | `failed` |
| `404 Not Found` | Async merge is not available for this repository, or the pull request was not found. | — |

```json
// 202 Accepted
{
  "status": "pending",
  "details": {
    "message": "Merge request enqueued.",
    "uuid": "630b9d5e-3f2a-4f7e-8b0c-2d5f9a8c1e42",
    "merge_method": "squash",
    "merge_action": "default",
    "expected_head_sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e"
  }
}
```

## Get the result of a merge

```
GET /repos/{owner}/{repo}/pulls/{pull_number}/merge-async/{uuid}
```

Fetches the current result of a merge request, identified by the `uuid` returned when the merge was submitted. A valid lookup always returns `200 OK`. Read the `status` field to see where the merge stands. Poll this endpoint (e.g., once a second) until the `status` is no longer `pending`.

The result is retained for **24 hours** after its most recent update. After that window the request expires and this endpoint returns `404 Not Found` for the UUID.

```sh
gh api repos/OWNER/REPO/pulls/102/merge-async/630b9d5e-3f2a-4f7e-8b0c-2d5f9a8c1e42
```

```json
// still running
{
  "status": "pending",
  "details": {
    "message": "Merge request is in progress.",
    "uuid": "630b9d5e-3f2a-4f7e-8b0c-2d5f9a8c1e42",
    "merge_method": "squash",
    "merge_action": "default",
    "expected_head_sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e"
  }
}

// merged directly
{
  "status": "merged",
  "details": {
    "message": "Pull request was merged.",
    "sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e"
  }
}

// added to the merge queue
{
  "status": "enqueued",
  "details": {
    "message": "Pull request was added to the merge queue."
  }
}

// could not be merged
{
  "status": "failed",
  "details": {
    "message": "Merge conflict: the pull request could not be merged."
  }
}
```

## The result object

Both endpoints return the same object: a `status` enum and a `details` object.

| Field | Type | Description |
|-------|------|-------------|
| `status` | `string` | The state of the merge: `pending`, `merged`, `enqueued`, or `failed`. |
| `details` | `object` | Details for the current state (see below). |

### Status values

| Status | Meaning |
|--------|---------|
| `pending` | The merge is running in the background. Keep polling. |
| `merged` | The stack was merged directly. `details.sha` is the resulting merge commit. |
| `enqueued` | The stack was added to the base branch's merge queue. It will merge once the queue processes it. This is a terminal state for the merge request — track the merge queue for the final outcome. |
| `failed` | The merge was attempted but could not complete (for example, a merge conflict or an unmet branch rule). `details.message` explains why. Because the merge is atomic, nothing was merged. |

### Details fields

The fields present in `details` depend on the state:

| Field | Type | Present when | Description |
|-------|------|--------------|-------------|
| `message` | `string` | always | A human-readable description of the current state. |
| `uuid` | `string` | `pending` | The identifier of the merge request, used to poll for the result. |
| `merge_method` | `string` | `pending` | The merge method being used (`merge`, `squash`, or `rebase`). |
| `merge_action` | `string` | `pending` | The resolved action (`default`, `direct_merge`, or `merge_queue`). |
| `expected_head_sha` | `string` | `pending` | The SHA the pull request head must match for the merge to proceed. |
| `sha` | `string` | `merged` | The resulting merge commit SHA. |

## Limitations

- **Bypassing merge requirements is not supported.** You cannot use admin privileges to bypass a stack's branch protection rules or rulesets; every pull request in the stack must satisfy its requirements before the stack can land.
- **Auto-merge is not supported.** A stacked pull request cannot be set to merge automatically once its requirements are met.
