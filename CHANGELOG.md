# Changelog

## 0.1.0 — 2026-09-24

First version.

- **Pull requests in a popup**: Mine, Review requested, and a repo tab that
  starts on the repo of the space you opened it from. Last time's lists show
  at once while fresh ones load, page by page.
- **Any repo**: the repo tab's first row (`⇄ owner/repo  change repo ›`, or
  **ctrl+t**) opens the repos you can reach, grouped by organisation, or
  takes a typed `owner/repo`.
- **The PR screen**: merge readiness and why not, checks needing attention,
  reviewers, the description, unresolved review threads and the
  conversation.
- **Draft ↔ ready**, and **merge** with any method the repo allows (pinned to
  the head commit you saw, confirmed before it happens), or auto-merge; then
  delete the branch and remove the worktree.
- **Worktrees** for any PR, forks through `refs/pull/N/head`, in the repo's
  checkout found by its remotes, or cloned; **start** also prompts the new
  worktree's agent.
- **Sidebar labels**: `$pr`, `$pr_badge`, `$pr_state`, `$pr_checks` and
  `$pr_review` on each space and its agents, kept current on herdr's events.
- A fictional **demo** org. Signs in through gh.
