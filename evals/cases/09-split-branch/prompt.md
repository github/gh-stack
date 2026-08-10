Branch `{branch_prefix}/big` holds one large commit under `eval/{run_id}/`: `model.js`, `api.js`
which imports the model, `page.js` which imports the API, a new `NOTES.md`, and a deleted
`legacy.js`. It is too big to review in one pull request. I want the data model, the API, and the
page reviewed as separate units. Split that branch into a stack using `{default_branch}` as the
trunk, preserving every change including the deletion. Do not push or open pull requests. Work
autonomously.
