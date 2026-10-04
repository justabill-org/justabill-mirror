# Contributing to Just a Bill

Thanks for helping. Just a Bill is a nonpartisan civic project, and fixes, tests, docs and
reports of wrong bill or vote data are all welcome. By taking part you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through [SECURITY.md](SECURITY.md),
never a public issue.

## Before you start

- **Small fixes** (a bug, a typo, a missing test): open a pull request directly, or an issue
  first if you'd like to check the approach.
- **Larger changes** (a new feature, a schema change, a new dependency or service): open an issue
  first and agree on the approach with the maintainer before you write the code.
- Wrong bill, member or vote data is an issue too: name the bill, member or vote and the source it
  disagrees with (Congress.gov, GovInfo or the House or Senate clerk).

## How a contribution ships

This repository is published from a private working repository with each release, one commit per
release, so pull requests here aren't merged with the button:

1. You open a pull request here. CI builds, tests and lints every module, checks the title against
   Conventional Commits, checks every commit for a `Signed-off-by` line and scans for secrets.
2. The maintainer reviews it and may ask for changes; push them to the same branch.
3. If it's accepted, the maintainer imports it into the working repository as one commit with you
   as its author and your `Signed-off-by` lines (other commit authors become `Co-authored-by`).
   The import comments on your pull request either way. It refuses, listing every reason, a pull
   request that:
   - has a commit without a `Signed-off-by` line for its author;
   - changes anything under `.github/`, `CHANGELOG.md`, `.gitattributes`, any `CLAUDE.md` or
     `.claude/` path, or a file outside what this repository publishes, or adds a symlink;
   - is over 5,000 changed lines;
   - has a title that isn't a Conventional Commit header, or that references an issue (`#12`: put
     references in the description, since issue numbers differ between the two repositories);
   - has a secret in any of its commits, even one removed again later (gitleaks).

   The change is applied to the release your branch starts from, and the working repository's
   latest code is merged in. If that merge conflicts, nothing is imported and the comment names the
   files: merge the latest `main` here into your branch once the next release is out, or ask the
   maintainer.
4. If changes are asked for, push them to the same branch; the maintainer imports it again, which
   replaces the earlier import. Only the commits that were imported (and reviewed) ship.
5. It ships in the next release, and the release commit credits you as a co-author. Your pull
   request gets a comment linking the release and is marked merged: your commits, as imported,
   become part of this repository's history. If your branch moved after the last import, the
   pull request is closed instead, and the later commits aren't part of the release; open a new
   pull request for them.

## Set up

You need Go 1.27, Node 24, Docker and [Task](https://taskfile.dev/docs/installation).

```bash
cp .env.example .env   # add a free api.data.gov key for CONGRESS_API_KEY and GOVINFO_API_KEY
task setup             # install the git hooks (commit messages, branch names, pre-commit checks)
task infra:up          # Spanner emulator, Auth emulator and Redis in Docker
task migrate           # create the local database and apply the migrations
task seed              # the row of the congress in progress
task backfill:quick    # a small sample of bills, members, votes and texts (LIMIT=100)
task dev               # API on :8080, web on :3000
```

With only Docker and Task (on a Mac: Docker Desktop, OrbStack or Colima, and `brew install
go-task`), run the checks in the dev container instead: `task docker:build` once, then
`task docker:test`, `task docker:lint` and `task docker:cover` run the same targets with the
pinned Go, Node and golangci-lint, against the emulators and Redis. `task docker:shell` opens a
shell there, and `task docker:run -- <command>` runs one command from the directory you're in.
The pre-commit hook (`task setup`) uses the container too: when `go`, `golangci-lint` or `npm`
isn't on your `PATH`, it says so and runs the staged modules' checks through
`docker compose run --rm dev`. `JAB_HOOKS=native` or `JAB_HOOKS=docker` picks one regardless.

`task --list` shows everything else.

## Sign off your commits (DCO)

Every commit must carry a `Signed-off-by` line. It certifies that you wrote the change or have
the right to submit it under the project's license, as described by the
[Developer Certificate of Origin](https://developercertificate.org/). Git adds the line for you:

```bash
git commit -s -m "fix(api): return 404 for an unknown congress"
```

The name and email must be your real identity (or the one on your GitHub account). To sign off
commits you already made on your branch, run `git rebase --signoff <base>` (the `main` you
branched from) and push. There's no CLA: you keep the copyright to your contributions, and
they're licensed under [Apache-2.0](LICENSE) like the rest of the project. New source files
don't need a license header; if you add one, use `Copyright The Just a Bill Authors`.

## Branches, commits and pull requests

Branches, commit messages and pull request titles follow
[Conventional Commits](https://www.conventionalcommits.org). The hooks from `task setup` and CI
check them.

- **Branches:** `<type>/<issue>-<slug>`, lowercase, e.g. `fix/123-member-photo`.
- **Commits and titles:** `<type>(<scope>): <description>`, at most 100 characters, imperative,
  no trailing period, `!` before the colon for a breaking change. Types: feat, fix, docs, style,
  refactor, perf, test, build, ci, chore, revert. Scopes are the area: api, web, pipeline, db, …
- **Pull requests** become one commit whose message is the title, so the title is what ships.
  In the description, write a summary with `Closes #<issue>` if there is one, what changed and
  any decisions you made, how you tested it, and anything the reviewer should look at closely.
- Keep pull requests focused: 500 to 1,000 changed lines is a good size, and bigger work splits
  into several. More than 5,000 changed lines can't be imported.

## Checks

CI runs the tests, linters and a secret scan (gitleaks) on every pull request. Run the ones for
what you touched before you push:

```bash
task test                  # db, api, obs, pipeline and web (one module: test:api, test:web, ...)
task lint                  # golangci-lint (db, api, obs, pipeline) and ESLint (lint:api, ...)
task cover                 # tests with coverage, checked against the floors (cover:api, ...)
```

Tests that use Spanner, Redis or the Auth emulator need `task infra:up` first; `task test:db`
runs `task migrate` itself.

- Go follows the strict `.golangci.yml` (no globals or `init`, 120-column lines, injected
  `*slog.Logger`s). Format with `gofmt`.
- Coverage only goes up: new code comes with tests, and a change shouldn't lower a module's
  coverage. Don't edit the floors in each Go module's `.testcoverage.yml` or in
  `web/vitest.config.ts`; the maintainer raises them now and then.
- Never commit secrets. `.env` is gitignored; tests must not need real API keys.

## Schema migrations

The database schema is defined by the numbered files in `db/migrations/`. `db/schema.sql` is
generated from them: read it, never edit it.

```bash
task db:new NAME=add_member_votes_index  # the next db/migrations/<n>_add_member_votes_index.sql
task db:schema             # regenerate db/schema.sql (on a throwaway emulator database)
task db:schema:check       # fail if db/schema.sql doesn't match the migrations
task db:append-only        # fail if the branch edits, renames or deletes a migration on BASE
task db:upgrade-test       # apply the branch's new migrations to a seeded database at BASE
task db:rebase             # merge BASE, renumber your new migrations after its latest, regenerate
```

`BASE` defaults to `origin/main`; if `origin` is your fork, pass `BASE=upstream/main` (or whatever
remote points here).

- **Append-only.** Never edit, rename or delete a migration that's already on `main`; fix forward
  with a new one.
- **Numbers are sequential.** If `main` gains a migration with your number, `task db:rebase` moves
  yours up and regenerates `db/schema.sql`. Commit migrations with the regenerated schema.
- **One logical change per file.** A file isn't atomic: if its second statement fails, the first
  stays applied. Keep at most 10 statements that validate or backfill data (`NOT NULL`, new
  indexes, foreign keys, checks) in one file.
- **DDL and DML never share a file.** A file starting with `UPDATE` or `DELETE` runs as
  Partitioned DML, which isn't atomic, so make it idempotent. DML only seeds public reference
  data: never personal data or secrets.
- **Expand, then contract.** Migrations run before the new code rolls out, so each must work with
  the code already running. A drop or rename takes two releases: first add the new column or
  table and write to both; once that's deployed, switch reads, then drop the old one.
- **New tables need a grant.** The API connects as the database role `api`. A migration that adds
  a table grants `api` the access it needs and classifies the table in `apiTableClasses`
  (`db/migrations/apirole_test.go`); that test fails until it does. `REVOKE` before `DROP TABLE`.

## Review

The maintainer reviews every contribution before it's imported, and the imported change runs the
working repository's full CI again. Some day-to-day work there is done by AI agents under the
maintainer's review; their changes follow the same rules as yours.
