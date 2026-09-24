# Changelog

## 0.1.0 — unreleased

- The repo tab can show any repo: **ctrl+t** (or a click on the tab) opens
  a picker grouped by organisation, with this space's repo, recent picks,
  every herdr space's repo, the repos your PRs are in, and every repo you
  can reach on GitHub; or a typed `owner/repo`. It starts on the space's
  repo each time.
- First version: Mine, Review requested and this repo's pull requests; the PR
  screen with merge readiness, checks, reviewers, description, unresolved
  threads and conversation; draft ↔ ready; merge with confirmation, or
  auto-merge; branch and worktree clean-up after a merge; worktrees for any
  PR (forks through `refs/pull/N/head`), found by remotes or cloned; start
  with a prompt for the new agent; sidebar labels (`$pr`, `$pr_badge`, …) on
  spaces and agents; the demo org. Signs in through gh.
