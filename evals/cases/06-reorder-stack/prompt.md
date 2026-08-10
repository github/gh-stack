The current stack is ordered `{default_branch} <- {branch_prefix}/models <-
{branch_prefix}/migration <- {branch_prefix}/ui`, but the migration must come before the models.
Reorder it to `{default_branch} <- {branch_prefix}/migration <- {branch_prefix}/models <-
{branch_prefix}/ui`, preserve all three changes, and finish on the top branch. Do not push. Work
autonomously.
