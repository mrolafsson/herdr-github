# Changelog

## 0.5.0 — 2026-10-06

- **One look, shared with herdr-linear and herdr-recap.** A title, a branch,
  a pull request, an agent and a key are drawn the same in all three (see
  *Colours* in the README). Here: a PR's number and mark wear its state's
  colour (draft peach, open green, merged mauve, closed red), its screen
  shows the state as a pill, branches are mauve, and an agent shows its
  state's mark (`◉ ◔ ● ✓`) in place of `▸`.
- **Keys are pills.** The keys along the bottom are solid chips, coloured by
  what they do and grouped by colour; the one under the pointer underlines.
  Too many for the popup's width, and labels shorten, then the least missed
  keys are left out.
- **The `terminal` theme keeps its hierarchy.** Branches and dim text no
  longer share the body's grey, and a title is the brightest text.
- **ctrl+a** goes to a PR's agent from the list, matching **a** on the PR
  screen. ctrl+g still works.
- Fixed: a click on the agents key, or on `← lists`, typed the key's name
  into the filter instead of pressing it.

## 0.4.0 — 2026-10-06

- **Straight to an agent's PR.** A new action, **GitHub: this agent's pull
  request** (`herdr-github.pr`; bind it to `prefix+alt+p`, say), opens the
  popup on the PR screen of the agent you're in: its branch's PR, or one it
  opened. With several it asks which. With none it refreshes and looks
  again, then lands on this repo's list, saying so.
- Opened that way, the popup never shows the lists on the way to the PR,
  the PR's keys work while they load behind it, and **esc** closes the
  popup (**←** goes to the lists).
- The agent marker is `▸`: `◆` is a merged PR's.
- A long title no longer wraps a menu's line.

## 0.3.0 — 2026-10-06

- **Whose PR is this?** Every open PR knows its agents: the Claude Code
  session that opened it, others that worked on it, and any agent in a
  space on its branch. In the popup a PR's row names the first and counts
  the rest, the PR screen lists them with their state, and **ctrl+g**
  (**a** on the PR screen) or a click goes to one. A session that isn't in
  a pane any more is listed too, and the key copies the command that
  resumes it. An agent's name filters to its PRs. Nothing to set up;
  `"agent_prs": false` turns it off.
- **`$prs`**, a new sidebar token: every PR that's an agent's, in one list.
  Its branch's PR with its status, then the others it opened, whatever
  branch its space is on (`#1112 draft ✓ #1103 ● #1101 ✓`). It's the one
  token an agent's row needs.
- The README says where sidebar rows go when you're attached to another
  machine with `herdr --remote`: in the config of the machine you're
  sitting at.

## 0.2.0 — 2026-09-26

- Linux. The browser opens with `xdg-open`; copying uses `wl-copy`, `xclip`
  or `xsel`. Releases include Linux binaries, and `scripts/build.sh`
  downloads them on machines without Go.
- Over SSH, or on Linux with no display, `o` copies the link instead of
  opening a browser you couldn't see, and every copy asks your terminal to
  set its clipboard (OSC 52) instead of the remote machine's.
- Opening the browser and copying have time limits, so neither can hang the
  picker.

## 0.1.1 — 2026-09-25

- Opening the picker while an earlier one is still up (left open on another
  herdr client, where you can't see it) closes that one and opens it where
  you are, instead of failing with *a popup pane is already open*. Another
  plugin's popup is left alone, with a toast saying so.
- The popup's size is in the manifest, so it's the same however it's
  opened.

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
