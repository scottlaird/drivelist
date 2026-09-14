#!/bin/sh
# Publish an exported demo directory as the gh-pages branch: one fresh
# commit holding exactly the directory's contents, force-pushed, so the
# branch never accumulates old snapshots.
#
#   drivelist demo export site --names demo/names.json
#   demo/publish.sh site [REMOTE]
#
# GitHub Pages must be set to serve the gh-pages branch, root directory
# (Settings > Pages > Deploy from a branch), once.
set -eu
dir=${1:?usage: publish.sh DIR [REMOTE]}
remote=${2:-git@github.com:scottlaird/drivelist.git}
[ -f "$dir/demo.json" ] || { echo "publish.sh: $dir is not an exported demo (no demo.json)" >&2; exit 1; }
cd "$dir"
rm -rf .git
git init -q -b gh-pages
git add -A
git -c user.name=drivelist -c user.email=drivelist@localhost commit -qm "demo snapshot $(date -u +%Y-%m-%dT%H:%MZ)"
git push -f "$remote" gh-pages:gh-pages
rm -rf .git
