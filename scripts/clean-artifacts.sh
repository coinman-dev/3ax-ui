#!/usr/bin/env bash
#
# clean-artifacts.sh — remove build outputs, caches and OS junk from this checkout,
# so a backup archive carries the sources and what cannot be rebuilt.
#
# KEPT (never touched):
#   - all code, go.mod/go.sum, package.json/package-lock.json, scripts/, docs/ sources;
#   - notes kept only here: .git, .ai/ (UPSTREAM_REBASE_PLAN.md and friends),
#     .claude/ (except agent worktrees, see --agents);
#   - local runtime data of a dev panel: x-ui.db*, system_metrics.gob, bin/
#     (Xray and geo files `go run .` downloaded) — not rebuilt by any build.
#
# REMOVED with --force (rebuilt on demand):
#   - frontend/node_modules (npm ci), frontend/storybook-static,
#     frontend/.husky/_, coverage and Playwright reports;
#   - internal/web/dist (npm run build); an empty stub is left so a bare
#     `go build` still compiles, as `make dist-stub` would;
#   - .cache/ (make node-e2e rebuilds its binary there);
#   - docs/node_modules, docs/.next, docs/out;
#   - tmp/, build/, release/, target/ and release archives *.tar.gz in the root;
#   - OS and editor junk (*Zone.Identifier, .DS_Store, *.swp, *.orig, *~).
#
# EXTRA, only with their own flags (together with --force; without it, shown only):
#   --stand  lab VM overlays ~/lab/3ax-ui/vm*/ (disks of gigabytes). A VM whose
#            qemu is still running is left alone. The lab scripts, keys and
#            screenshots stay, and so do the owner's base images in ~/lab/linux.
#   --agents agent worktrees .claude/worktrees/* without uncommitted changes and
#            worktree-agent-* branches whose every change is already in dev
#            (`git cherry`); removed branches are saved first to
#            backup/agent-branches-<time>.bundle. `git worktree prune` drops
#            stale entries of worktrees whose folder is gone.
#   --all    = --stand --agents
#
# Refuses --force on NTFS/FAT/exFAT: deleting node_modules' symlinks there from
# Linux is how the Windows copy of this repo got a corrupted volume (2026-11-22).
#
# Usage:
#   scripts/clean-artifacts.sh                  # DRY RUN: what would go and how much
#   scripts/clean-artifacts.sh --force          # remove the artifacts
#   scripts/clean-artifacts.sh --force --all    # ...and the lab overlays and agents
#   scripts/clean-artifacts.sh --archive [OUT]  # light .tar.gz, nothing removed
#                                               # (default: into the current folder)
#   scripts/clean-artifacts.sh --archive --with-git [OUT]   # ...with the .git history
#   scripts/clean-artifacts.sh --help
#
set -euo pipefail

INVOKE_DIR="$(pwd)"
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE="$(basename "$ROOT")"
LAB="${XUI_LAB_DIR:-$HOME/lab/3ax-ui}"
cd "$ROOT"

ARTIFACT_DIRS=(
    "frontend/node_modules"
    "frontend/storybook-static"
    "frontend/.husky/_"
    "frontend/coverage"
    "frontend/test-results"
    "frontend/playwright-report"
    "internal/web/dist"
    ".cache"
    "docs/node_modules"
    "docs/.next"
    "docs/out"
    "tmp"
    "build"
    "release"
    "target"
)

# Release archives scripts/build-local.sh writes; only in the root, never searched for.
BUILT_GLOBS=("*.tar.gz" "*.zip")

JUNK_GLOBS=("*.DS_Store" "Thumbs.db" "*Zone.Identifier" "*.swp" "*.swo" "*.orig" "*.rej" "*~")

# Not removed, but kept out of the archive: runtime data of a dev panel.
ARCHIVE_EXTRA_EXCLUDES=("bin" "x-ui.db" "x-ui.db-shm" "x-ui.db-wal" "system_metrics.gob" ".claude/worktrees")

human() { du -sh "$1" 2>/dev/null | cut -f1; }

usage() {
    sed -n '2,/^set -euo/p' "$SELF" | grep '^#' | sed 's/^# \{0,1\}//'
    exit 0
}

# Walks the tree past .git, node_modules and the folders removed whole anyway.
prune_find() {
    find . -path ./.git -prune -o -name node_modules -prune -o -path ./.cache -prune \
        -o -path ./tmp -prune -o -path ./.claude/worktrees -prune -o "$@" || true
}

fs_type() {
    findmnt -no FSTYPE -T "$1" 2> /dev/null || stat -f -c %T "$1" 2> /dev/null || echo unknown
}

MODE="dry"
WITH_GIT=0
DO_STAND=0
DO_AGENTS=0
OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --force | -f) MODE="force" ;;
        --archive | -a) MODE="archive" ;;
        --with-git) WITH_GIT=1 ;;
        --stand) DO_STAND=1 ;;
        --agents) DO_AGENTS=1 ;;
        --all)
            DO_STAND=1
            DO_AGENTS=1
            ;;
        -h | --help) usage ;;
        -*)
            echo "unknown flag: $1" >&2
            exit 2
            ;;
        *) OUT="$1" ;;
    esac
    shift
done

if [ "$MODE" = "archive" ]; then
    ts="$(date +%Y%m%d-%H%M%S)"
    if [ -z "$OUT" ]; then
        OUT="$INVOKE_DIR/${BASE}-backup-${ts}.tar.gz"
    elif [ "${OUT#/}" = "$OUT" ]; then
        OUT="$INVOKE_DIR/$OUT"
    fi
    excludes=(--exclude='node_modules')
    for d in "${ARTIFACT_DIRS[@]}"; do excludes+=(--exclude="$BASE/$d"); done
    for g in "${BUILT_GLOBS[@]}"; do excludes+=(--exclude="$BASE/$g"); done
    for g in "${JUNK_GLOBS[@]}"; do excludes+=(--exclude="$g"); done
    for e in "${ARCHIVE_EXTRA_EXCLUDES[@]}"; do excludes+=(--exclude="$BASE/$e"); done
    [ "$WITH_GIT" -eq 0 ] && excludes+=(--exclude="$BASE/.git")

    echo "Building the archive (code plus .ai/ and .claude/: outside git, it is their only copy):"
    echo "  -> $OUT"
    [ "$WITH_GIT" -eq 0 ] && echo "  (no .git history; add --with-git for it)"
    tar -C "$(dirname "$ROOT")" -czf "$OUT" "${excludes[@]}" "$BASE"
    echo "Done. Archive size: $(human "$OUT")"
    exit 0
fi

if [ "$MODE" = "force" ]; then
    case "$(fs_type "$ROOT")" in
        ntfs | ntfs3 | fuseblk | vfat | msdos | exfat | *0x7366746e*)
            echo "This checkout is on a Windows file system ($(fs_type "$ROOT")): not deleting anything here." >&2
            echo "Clean it from Windows, or update it through git instead of copying it over." >&2
            exit 3
            ;;
    esac
fi

total_kb=0
found=0
note() { printf '  %-7s %s\n' "$1" "$2"; }

echo "Artifacts under $ROOT:"
for d in "${ARTIFACT_DIRS[@]}"; do
    [ -e "$d" ] || continue
    [ -L "$d" ] && continue
    if [ "$d" = "internal/web/dist" ] && [ -z "$(find "$d" -mindepth 1 ! -name .gitkeep -print -quit)" ]; then
        continue
    fi
    found=1
    note "$(human "$d")" "$d/"
    total_kb=$((total_kb + $(du -sk "$d" | cut -f1)))
    if [ "$MODE" = "force" ]; then
        rm -rf --one-file-system -- "$d"
        if [ "$d" = "internal/web/dist" ]; then
            mkdir -p "$d" && touch "$d/.gitkeep"
        fi
    fi
done

shopt -s nullglob
for pat in "${BUILT_GLOBS[@]}"; do
    for b in $pat; do
        [ -f "$b" ] && [ ! -L "$b" ] || continue
        found=1
        note "$(human "$b")" "$b (build output)"
        total_kb=$((total_kb + $(du -sk "$b" | cut -f1)))
        [ "$MODE" = "force" ] && rm -f -- "$b"
    done
done
shopt -u nullglob

junk_args=()
for g in "${JUNK_GLOBS[@]}"; do junk_args+=(-name "$g" -o); done
unset 'junk_args[${#junk_args[@]}-1]'
junk_count=$(prune_find -type f \( "${junk_args[@]}" \) -print | wc -l | tr -d ' ')
if [ "$junk_count" -gt 0 ]; then
    found=1
    note "" "$junk_count file(s) of OS/editor junk (Zone.Identifier, .DS_Store, swap files ...)"
    [ "$MODE" = "force" ] && prune_find -type f \( "${junk_args[@]}" \) -print0 | xargs -0 -r rm -f
fi

if [ "$found" -eq 0 ]; then
    echo "  (clean, nothing to remove)"
else
    echo
    if [ "$total_kb" -ge 1024 ]; then
        printf 'Frees: ~%s MB\n' "$((total_kb / 1024))"
    else
        printf 'Frees: ~%s KB\n' "$total_kb"
    fi
fi

if [ "$DO_STAND" -eq 1 ]; then
    echo
    echo "Lab VMs ($LAB):"
    vms=0
    for vm in "$LAB"/vm*/; do
        [ -d "$vm" ] || continue
        vm="${vm%/}"
        vms=1
        if [ -f "$vm/qemu.pid" ] && kill -0 "$(cat "$vm/qemu.pid")" 2> /dev/null; then
            note "$(human "$vm")" "${vm/#$HOME/\~} — VM running, stays (stop it first)"
            continue
        fi
        note "$(human "$vm")" "${vm/#$HOME/\~} (VM disk overlay)"
        [ "$MODE" = "force" ] && rm -rf --one-file-system -- "$vm"
    done
    [ "$vms" -eq 0 ] && echo "  no VM overlays"
    echo "  lab scripts, keys and screenshots stay; base images in ~/lab/linux are not touched"
fi

if [ "$DO_AGENTS" -eq 1 ]; then
    echo
    echo "Agents (.claude/worktrees and worktree-agent-* branches):"
    if [ -d .claude/worktrees ]; then
        for wt in .claude/worktrees/*/; do
            [ -d "$wt" ] || continue
            wt="${wt%/}"
            if [ -n "$(git -C "$wt" status --porcelain 2> /dev/null)" ]; then
                note "$(human "$wt")" "$wt — uncommitted changes, stays"
                continue
            fi
            note "$(human "$wt")" "$wt (worktree)"
            [ "$MODE" = "force" ] && git worktree remove --force "$wt"
        done
    fi
    stale=$(git worktree prune --dry-run -v 2>&1 | wc -l | tr -d ' ')
    [ "$stale" -gt 0 ] && note "" "$stale stale worktree entr(y/ies) whose folder is gone"
    [ "$MODE" = "force" ] && git worktree prune
    gone=()
    for b in $(git branch --format='%(refname:short)' | grep '^worktree-agent-' || true); do
        if git worktree list --porcelain | grep -qx "branch refs/heads/$b"; then
            note "" "$b — checked out in a worktree, stays"
        elif git cherry dev "$b" | grep -q '^+'; then
            note "" "$b — has changes not in dev, stays: $(git log -1 --format=%s "$b" | cut -c1-60)"
        else
            note "" "$b (all in dev)"
            gone+=("$b")
        fi
    done
    if [ "$MODE" = "force" ] && [ ${#gone[@]} -gt 0 ]; then
        mkdir -p backup
        bundle="backup/agent-branches-$(date +%Y%m%d-%H%M%S).bundle"
        git bundle create "$bundle" "${gone[@]}" > /dev/null 2>&1 && echo "  branches saved: $bundle"
        git branch -D "${gone[@]}" > /dev/null
    fi
fi

if [ "$MODE" = "force" ]; then
    echo
    echo "Removed. (code, .ai/, .claude/, bin/ and the dev database are in place)"
    echo "  rebuild: cd frontend && npm ci && npm run build"
elif [ "$found" -ne 0 ] || [ "$DO_STAND" -eq 1 ] || [ "$DO_AGENTS" -eq 1 ]; then
    echo
    echo "DRY RUN, nothing removed."
    echo "  remove:            scripts/clean-artifacts.sh --force [--stand] [--agents | --all]"
    echo "  archive instead:   scripts/clean-artifacts.sh --archive"
fi
