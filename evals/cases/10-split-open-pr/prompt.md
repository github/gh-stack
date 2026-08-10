Pull request #{source_pr} (branch `{branch_prefix}/big`, base `{default_branch}`) is too large to
review. Under `eval/{run_id}/` it adds `model.js`, `api.js` which imports the model, `page.js`
which imports the API, and `NOTES.md`, and deletes `legacy.js`. I want the data model, the API, and
the page reviewed as separate units. Split it into a stack of dependent branches, preserving every
change including the deletion, and make sure the original pull request is either reused as the top
layer or closed. Work autonomously.
