#!/bin/sh
# Vercel "Ignored Build Step" for the web project (Root Directory web/).
# Exit 0 skips the build; exit 1 builds. Any doubt builds, so a web change is
# never silently left undeployed. See docs/design/51-vercel.md.
#
# Vercel runs this from web/ in a shallow clone (depth 10) and sets
# VERCEL_GIT_PREVIOUS_SHA to the last successful deployment of this branch
# (empty on the branch's first deployment).

# Production deployments come only from the release deploy, one per
# release tag (#365, design 28). A skipped build ends "Canceled", which fails
# that apply, so production always builds (#531).
if [ "${VERCEL_ENV:-}" = "production" ]; then
  echo "vercel-ignore: production deployment; building"
  exit 1
fi

prev="${VERCEL_GIT_PREVIOUS_SHA:-}"

if [ -z "$prev" ]; then
  echo "vercel-ignore: no previous deployment on this branch; building"
  exit 1
fi

if [ "$prev" = "$(git rev-parse HEAD 2>/dev/null)" ]; then
  # A new push of an already-deployed SHA never reaches this step, so this is
  # a manual redeploy (e.g. after an env var change).
  echo "vercel-ignore: redeploy of ${prev}; building"
  exit 1
fi

if ! git cat-file -e "${prev}^{commit}" 2>/dev/null; then
  echo "vercel-ignore: ${prev} is not in the clone; building"
  exit 1
fi

git diff --quiet "$prev" HEAD -- .
case $? in
  0)
    echo "vercel-ignore: web/ unchanged since ${prev}; skipping"
    exit 0
    ;;
  1)
    echo "vercel-ignore: web/ changed since ${prev}; building"
    exit 1
    ;;
  *)
    echo "vercel-ignore: git diff failed; building"
    exit 1
    ;;
esac
