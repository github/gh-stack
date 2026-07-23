---
title: GraphQL API
description: Reference for the read-only stack fields and objects on the GraphQL PullRequest type.
---

The GraphQL API exposes a pull request's stack membership through two read-only fields on the `PullRequest` type, backed by a small set of stack objects. These fields are **read-only** — there are no stack mutations via GraphQL. To create or modify stacks use the [REST API](/gh-stack/reference/rest-api/).

## Fields on `PullRequest`

| Field | Type | Description |
|-------|------|-------------|
| `stack` | `PullRequestStack` | The stack this pull request belongs to, or `null` if it is not part of a stack. |
| `stackEntry` | `PullRequestStackEntry` | This pull request's entry within its stack (including its position), or `null` if it is not part of a stack. |

## Objects

### `PullRequestStack`

A stack of pull requests.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `ID!` | The Node ID of the `PullRequestStack` object. |
| `number` | `Int!` | A number uniquely identifying the stack within its repository. |
| `size` | `Int!` | The total number of pull requests in the stack. |
| `baseRefName` | `String!` | The branch that the stack's pull requests target. |
| `entries` | `PullRequestStackEntryConnection!` | The entries in the stack. |

### `PullRequestStackEntry`

A member of a `PullRequestStack`.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `ID!` | The Node ID of the `PullRequestStackEntry` object. |
| `position` | `Int!` | This entry's position in the stack, where `1` is the closest to the base branch, `2` is stacked on top of `1`, and so on. |
| `pullRequest` | `PullRequest` | The pull request that occupies this position in the stack. |
| `stack` | `PullRequestStack` | The stack that this entry is a part of. |

### `PullRequestStackEntryConnection`

A paginated connection of stack entries. Follows the standard [GraphQL connection pattern](https://docs.github.com/en/graphql/guides/introduction-to-graphql#connection).

| Field | Type | Description |
|-------|------|-------------|
| `edges` | `[PullRequestStackEntryEdge]` | A list of edges. |
| `nodes` | `[PullRequestStackEntry]` | A list of the entries. |
| `pageInfo` | `PageInfo!` | Information to aid in pagination. |
| `totalCount` | `Int!` | The total number of entries in the connection. |

### `PullRequestStackEntryEdge`

An edge in a `PullRequestStackEntryConnection`.

| Field | Type | Description |
|-------|------|-------------|
| `cursor` | `String!` | A cursor for use in pagination. |
| `node` | `PullRequestStackEntry` | The item at the end of the edge. |

## Example

Read a pull request's stack, its position, and every pull request in the stack:

```graphql
{
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: 42) {
      number
      baseRefName
      stackEntry {
        position
      }
      stack {
        number
        size
        baseRefName
        entries(first: 20) {
          totalCount
          nodes {
            position
            pullRequest {
              number
              title
              state
            }
          }
        }
      }
    }
  }
}
```

The pull request's own `baseRefName` is the branch it directly targets (the PR below it in the stack), while `stack.baseRefName` is the branch the entire stack ultimately targets. These differ for every PR in the stack except the bottom one.
