The working tree has one large uncommitted change under `eval/{run_id}/`: `model.js`, `api.js`
which imports the model, `page.js` which imports the API, a new `NOTES.md`, and a deleted
`legacy.js`. This is too big to review in one pull request. I want the data model, the API, and the
page reviewed as separate units. Break it into a stack using `{default_branch}` as the trunk and
branches prefixed with `{branch_prefix}/`, preserving every change including the deletion. Do not
push or open pull requests. Work autonomously.
