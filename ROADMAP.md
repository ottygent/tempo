# Tempo Roadmap

Candidate features for future releases, grouped by rough size. Grounded in what
Tempo has today: workspaces → projects → tasks/kanban, docs, timeline, calendar,
time tracking, single-admin auth, and a PWA shell.

## Quick wins (days, mostly frontend)

- **Global search / command palette (Ctrl+K)** — jump to tasks, docs, and
  projects; expose actions ("new task", "start timer"). High daily-use value.
- **Board filtering and sorting** — filter by assignee, tag, priority, or due
  date; the data is already on every card.
- **Drag-to-reorder within a column** — only status changes are possible today;
  a `sortOrder` field would let users rank work inside a column.
- **Manual time-entry editing** — add or correct entries after the fact. The
  timer is currently the only way in, so a forgotten stop pollutes totals.
- **Task quick-add** — inline "add card" that creates directly in a column with
  just a title, no modal.
- **Keyboard shortcuts** — `n` new task, `1–6` to switch views, arrow keys to
  move cards (the drag handle already supports arrow keys).
- **Per-workspace accent color (Material You)** — workspaces already store a
  color; derive the Material 3 primary/container palette from it so each
  workspace gets its own themed UI.

## Medium features (a week or two each)

- **Subtasks / checklists** — progress indicator on the card ("3/7"); makes the
  board useful for bigger work items.
- **Task comments + activity log** — needs a schema addition, but unlocks the
  audit trail the task drawer already hints at.
- **Task dependencies** — "blocked by" links; makes the Gantt view genuinely
  useful (dependency arrows, impossible-schedule warnings).
- **Recurring tasks** — a `recurrence` rule plus server-side materialization
  (weekly reviews, invoicing reminders).
- **Time reports** — weekly/monthly summaries per project and task with CSV
  export; entries exist already, only aggregation and a view are missing.
- **Doc improvements** — wiki-style `[[links]]` between docs, image
  attachments, doc templates, and lightweight version history.
- **Saved board views** — persisted filter/sort combinations ("My
  high-priority", "Due this week").
- **Import/export** — JSON export for backup; Trello/CSV import for
  onboarding. Cheap insurance and a real adoption lever.
- **Notifications** — due-date reminders via the existing service worker (web
  push) plus an in-app inbox.

## Big bets (structural)

- **Real multi-user** — the biggest one. There is one admin account and
  assignees are free-text strings. Real users need invitations, per-workspace
  roles/permissions, and assignee-as-user-reference. Almost every collaboration
  feature depends on this.
- **Real-time sync** — WebSocket or SSE so two tabs/users see board moves live;
  the optimistic-update queue in `moveTask` is already halfway to the client
  model needed.
- **Offline-first PWA** — the service worker exists; add IndexedDB state
  caching plus a mutation queue so the timer and quick edits work offline.
- **Integrations** — GitHub/GitLab (link commits/PRs to tasks), Slack
  notifications, and a read-only iCal feed for the calendar view.
- **Public API + tokens** — the Go backend already has clean REST routes;
  scoped API tokens enable automation and CLI tooling.
- **Analytics dashboard** — burndown/burnup, cumulative flow (requires
  recording status transitions), workload per assignee, and estimate-vs-actual
  using `estimateMinutes` against tracked time — a distinctive feature since
  both numbers already exist.

## Knowledge graph

Today Tempo's data is a set of flat lists joined by IDs (workspace → project →
task/doc). The bigger idea: treat the workspace as a **graph knowledge base**,
where everything is a node and relationships are first-class.

### Data model

- **Nodes** — workspaces, projects, tasks, docs, tags, people, time entries.
  Each already has a stable ID; they just need a shared node identity.
- **Typed edges** — stored in a new `edges` collection:
  `{ from, to, type, createdAt, createdBy }` with types like `references`,
  `blocked-by`, `part-of`, `mentions`, `assigned-to`, `tracked-on`.
  MongoDB handles this fine at Tempo's scale; traversal is a few indexed
  queries, no graph database required until proven otherwise.
- **Automatic edge extraction** — parse `[[Doc Title]]` wiki-links, `#tag`, and
  `@person` out of doc content and task descriptions on save, and materialize
  them as edges. Existing fields (task→project, tags[], assignee) become edges
  too, so old and new data live in one model.

### What it unlocks in the UI

- **Backlinks panel** — every doc and the task drawer show "what links here":
  the doc a task was planned in, the tasks a spec produced, etc.
- **Graph view** — a force-directed map of the workspace, filterable by node
  type and time; orphan detection ("docs nothing references anymore").
- **Contextual sidebars** — opening a task surfaces related docs, sibling tasks
  that share tags, and recent time entries without manual linking.
- **Cross-project queries** — "everything tagged #a11y across all projects",
  which the current per-project views cannot express.

## Agents

With a graph in place, agents get a substrate they can read and act on. The
plan is layered — each layer is useful without the ones above it.

### Layer 1: retrieval

- **Embeddings + semantic search** — index docs, task titles/descriptions, and
  comments; "find everything about the CMS migration" beats keyword search.
  Store vectors in MongoDB (Atlas vector search) or a local index.
- **RAG-powered workspace Q&A** — a chat panel that answers from the graph:
  "what's blocking the launch?", "summarize last week's tracked time". Answers
  cite their source nodes, and every citation is a click-through.

### Layer 2: assistant with tools

Expose Tempo's existing REST mutations as **agent tools** (create/update task,
link entities, start/stop timer, write doc). A chat assistant can then do work,
not just answer:

- "Turn these meeting notes into tasks" → extracts action items, creates
  tasks, links them back to the source doc.
- "Plan the a11y audit" → drafts a doc, creates subtasks with estimates,
  sets dependencies.
- Auto-triage: suggest tags, priority, and estimates for new tasks based on
  similar historical tasks (estimate-vs-actual data already exists).
- **MCP server** — ship Tempo as an MCP server so external agents (Claude
  Code, Claude Desktop) can operate the workspace with scoped API tokens.
  This is cheap once the public API + tokens exist and makes Tempo
  automatable by tools users already run.

### Layer 3: autonomous routines

Background agents on a schedule, writing results back into the graph:

- **Daily digest / standup doc** — what moved, what's stuck, what's due.
- **Stale-work nudges** — tasks untouched for N days in `progress`, docs that
  contradict newer decisions.
- **Dependency inference** — suggest `blocked-by` edges from co-occurrence and
  language in descriptions; human confirms.
- **Schedule advisor** — compare estimates + dependencies against the calendar
  and flag impossible plans before the timeline view makes them obvious.

### Guardrails

- Every agent mutation goes through the same audited REST layer as a human,
  attributed to an agent identity, and is reversible (soft-delete/undo).
- Suggestions default to human-in-the-loop approval; only explicitly enabled
  routines run autonomously.
- Agent tokens are scoped per workspace and per capability (read-only,
  suggest, write).

### Sequencing

Wiki-links + backlinks first (pure app feature, no AI, immediately useful) →
edges collection + graph view → embeddings search → chat with tools → MCP
server → autonomous routines. Multi-user auth should land before Layer 3, so
agent actions have a real identity model to attach to.

## Housekeeping

- E2E tests (Playwright is already a devDependency).
- Accessibility pass: focus trapping in the drawer/modals; contrast audit on
  the amber/mint tones.
- i18n scaffolding — cheaper to add before the string count doubles.

## Suggested sequence

Command palette → board filters → manual time entries → time reports →
subtasks. Then decide whether Tempo stays a great single-player tool (double
down on offline + integrations) or goes multi-user (do that before comments
and notifications, since they all depend on it).
