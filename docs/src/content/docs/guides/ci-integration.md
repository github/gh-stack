---
title: Using Stacks with CI and Custom Integrations
description: How to use the stack object in pull_request webhook events to trigger CI workflows on any PR in a stack that targets a specific branch.
---

When a pull request is part of a stack, GitHub includes a `stack` object in `pull_request` webhook events. This lets CI systems and custom apps inspect the stack's ultimate target branch — not just the direct base branch of the PR — so you can trigger the right workflows for every PR in a stack.

## The `stack` Object in Webhook Payloads

For any PR that belongs to a stack, the `pull_request` webhook payload includes a top-level `stack` property:

```json
{
  "action": "opened",
  "pull_request": {
    "number": 42,
    "title": "Add API routes",
    "base": {
      "ref": "feat/auth-layer",
      "sha": "abc123..."
    },
    "stack": {
      "base": {
        "ref": "main",
        "sha": "def456..."
      }
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `pull_request.stack.base.ref` | The branch that the entire stack is ultimately targeting (e.g., `main`). |
| `pull_request.stack.base.sha` | The current HEAD SHA of that target branch at the time of the event. |

The `stack` property is only present if the pull request belongs to a stack. For standalone PRs, the field is absent.

Note that `pull_request.base.ref` is the direct base of this individual PR (e.g., the branch below it in the stack), while `pull_request.stack.base.ref` is the ultimate target of the entire stack (e.g., `main`). These differ for all PRs except the bottom PR of the stack.

## Triggering CI for Any PR in a Stack Targeting `main`

A common need is to run CI checks on every PR in a stack that ultimately targets `main` — not just the bottom PR that directly targets `main`. Without the `stack` object, you would have to inspect the full chain of PRs to determine the stack's target branch; with it, you can check directly.

### GitHub Actions

Use an `if` condition on your workflow or job to check `pull_request.stack.base.ref`:

```yaml
name: CI

on:
  pull_request:
    types: [opened, synchronize, reopened]

jobs:
  test:
    # Run on PRs that directly target main OR are in a stack targeting main
    if: |
      github.event.pull_request.base.ref == 'main' ||
      github.event.pull_request.stack.base.ref == 'main'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run tests
        run: make test
```

This ensures that every layer of a stack targeting `main` is tested — not just the bottom PR.

### Filtering by `stack.base.ref` in a Custom App

If you're building a custom integration (a GitHub App, a webhook receiver, or a CI bridge), filter on `stack.base.ref` in your event handler:

```javascript
// Express.js webhook handler example
app.post('/webhook', (req, res) => {
  const { action, pull_request } = req.body;

  if (action !== 'opened' && action !== 'synchronize') {
    return res.sendStatus(200);
  }

  const directBase = pull_request.base.ref;
  const stackBase = pull_request.stack?.base?.ref;

  const targetsMain = directBase === 'main' || stackBase === 'main';

  if (targetsMain) {
    triggerCIPipeline(pull_request);
  }

  res.sendStatus(200);
});
```

The optional chaining (`?.`) safely handles PRs that are not part of a stack — `pull_request.stack` will be `undefined` for standalone PRs.

### Python Example

```python
import hmac
import hashlib
from flask import Flask, request, abort

app = Flask(__name__)

@app.route('/webhook', methods=['POST'])
def handle_webhook():
    data = request.get_json()
    action = data.get('action')
    pull_request = data.get('pull_request', {})

    if action not in ('opened', 'synchronize', 'reopened'):
        return '', 200

    direct_base = pull_request.get('base', {}).get('ref')
    stack_base = (pull_request.get('stack') or {}).get('base', {}).get('ref')

    targets_main = direct_base == 'main' or stack_base == 'main'

    if targets_main:
        trigger_ci_pipeline(pull_request)

    return '', 200
```

## Checking the Stack Base SHA

`pull_request.stack.base.sha` provides the SHA of the ultimate target branch at the time of the event. You can use this to:

- Determine how far the stack has drifted from the latest trunk commit
- Fetch only the commits introduced by the entire stack (from `stack.base.sha` to `pull_request.head.sha`)
- Cache CI results keyed on the trunk SHA so that re-runs after a trunk update aren't necessary

```yaml
- name: Fetch stack commits
  run: |
    TRUNK_SHA="${{ github.event.pull_request.stack.base.sha }}"
    HEAD_SHA="${{ github.event.pull_request.head.sha }}"
    git fetch origin "$TRUNK_SHA" "$HEAD_SHA"
    git log --oneline "$TRUNK_SHA..$HEAD_SHA"
```

## Handling Both Stacked and Standalone PRs

Because `stack` is absent for standalone PRs, any code that reads `pull_request.stack` should treat it as optional. The patterns above already demonstrate this, but here's a concise summary:

| Scenario | `pull_request.base.ref` | `pull_request.stack` |
|----------|------------------------|----------------------|
| Standalone PR targeting `main` | `main` | absent |
| Bottom PR of a stack targeting `main` | `main` | `{ base: { ref: "main", sha: "..." } }` |
| Mid-stack PR in a stack targeting `main` | `feat/auth-layer` | `{ base: { ref: "main", sha: "..." } }` |
| Top PR of a stack targeting `main` | `feat/api-routes` | `{ base: { ref: "main", sha: "..." } }` |

A robust check covers all cases:

```javascript
function targetsMainBranch(pullRequest, trunk = 'main') {
  return (
    pullRequest.base.ref === trunk ||
    pullRequest.stack?.base?.ref === trunk
  );
}
```
