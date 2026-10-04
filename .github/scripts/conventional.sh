#!/usr/bin/env bash
# Checks branch names, commit messages and PR titles against Conventional Commits
# (https://www.conventionalcommits.org/en/v1.0.0/). PRs are squash-merged with the PR title as the
# commit title, so the title is what lands on main.
#
#   conventional.sh title "<text>"     a PR title or commit header
#   conventional.sh branch "<name>"    a branch name: <type>/<issue>-<slug>, e.g. feat/135-token-verification
#   conventional.sh commit-msg <file>  the git commit-msg hook (.githooks/commit-msg)
#   conventional.sh pr                 CI: the PR's title, branch and every commit (env below)
#
# CI env: GH_TOKEN, GITHUB_REPOSITORY, PR, TITLE, BRANCH and CREATED (the PR's created_at). Branch
# names and commits are checked for PRs opened from RULES_FROM on; older PRs only need a
# conventional title, which is all a squash merge keeps.
#
# The public export publishes this script (the commit-msg and pre-push hooks run it), so this
# repo's own PR rules live elsewhere (guide-rule.sh).
set -euo pipefail

types='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'
header_re="^(${types})(\([a-z0-9][a-z0-9._/,-]*\))?!?: [^ ]"
branch_re="^(${types})/[a-z0-9][a-z0-9._-]*(/[a-z0-9._-]+)*$"
bot_branch_re='^(dependabot|renovate|gh-readonly-queue|release-please--)'
max_header=100
rules_from=${RULES_FROM:-2026-09-27T00:00:00Z}

# check_header <text>: prints why it isn't a Conventional Commit header, or nothing.
check_header() {
  local h=$1
  [[ $h =~ ^Revert\ \".+\"$ ]] && return 0 # GitHub's revert button
  if [[ ! $h =~ $header_re ]]; then
    echo "not '<type>(<scope>): <description>' with a type of ${types//|/, }"
  elif (( ${#h} > max_header )); then
    echo "longer than ${max_header} characters (${#h})"
  elif [[ $h == *. ]]; then
    echo "ends with a period"
  fi
}

check_branch() {
  local b=$1
  [[ $b =~ $bot_branch_re ]] && return 0
  [[ $b =~ $branch_re ]] || echo "not '<type>/<issue>-<slug>' (lowercase) with a type of ${types//|/, }"
}

fail=0
report() { # report <what> <subject> <problem>
  [[ -n $3 ]] || return 0
  fail=1
  if [[ -n ${GITHUB_ACTIONS:-} ]]; then
    echo "::error title=${1} isn't a Conventional Commit::${2}: ${3}"
    printf -- '- **%s** `%s`: %s\n' "$1" "$2" "$3" >>"$GITHUB_STEP_SUMMARY"
  else
    printf '%s "%s": %s\n' "$1" "$2" "$3" >&2
  fi
}

pr_commits() { # prints "<parent count> <header>" per commit (the API returns at most 250)
  local page batch
  for page in 1 2 3; do
    batch=$(curl -fsS -H "Authorization: Bearer ${GH_TOKEN}" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/${GITHUB_REPOSITORY}/pulls/${PR}/commits?per_page=100&page=${page}" |
      jq -r '.[] | "\(.parents | length) \(.commit.message | split("\n")[0])"')
    [[ -n $batch ]] || break
    echo "$batch"
  done
}

case ${1:-} in
  title) report "Title" "$2" "$(check_header "$2")" ;;
  branch) report "Branch" "$2" "$(check_branch "$2")" ;;
  commit-msg)
    header=$(grep -v '^#' "$2" | sed '/^[[:space:]]*$/d' | head -1)
    # git's own merge messages are fine locally; merge commits are skipped in CI too.
    [[ $header =~ ^Merge\ (branch|remote-tracking\ branch|pull\ request|tag)\  ]] ||
      report "Commit message" "$header" "$(check_header "$header")"
    ;;
  pr)
    report "PR title" "$TITLE" "$(check_header "$TITLE")"
    if [[ ! $CREATED < $rules_from ]]; then
      report "Branch" "$BRANCH" "$(check_branch "$BRANCH")"
      while read -r parents header; do
        (( parents > 1 )) && continue # merges from main (Update branch)
        [[ $header =~ ^(fixup|squash|amend)!\  ]] && continue # squashed away on merge
        report "Commit" "$header" "$(check_header "$header")"
      done < <(pr_commits)
    fi
    if (( fail )) && [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
      cat >>"$GITHUB_STEP_SUMMARY" <<'EOF'

**How to fix:**
- Title: `gh pr edit <n> --title "feat(api): …"`.
- Commit messages: reword them with `git rebase -i origin/main` (`reword`), then `git push --force-with-lease`.
- Branch: rename it on GitHub, which moves the open PR with it:
  `gh api -X POST repos/<owner>/<repo>/branches/<old>/rename -f new_name=feat/<issue>-<slug>`.

Types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert. Add `!` before the
colon for a breaking change. Keep the header to 100 characters, without a trailing period.
EOF
    fi
    ;;
  *)
    echo "usage: $0 title <text> | branch <name> | commit-msg <file> | pr" >&2
    exit 2
    ;;
esac
exit "$fail"
