#!/usr/bin/env bash
# Manual, destructive GitHub Pages test. Run only with an account that can
# create and delete public repositories. Never run this from automated checks.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
stage=preflight
repo_created=0
work_dir=
repo=
site_url=
failed_command=
failed_line=
run_command=

log() { printf '\n[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
run() {
    printf -v run_command '%q ' "$@"
    printf '  + %s\n' "$run_command"
    "$@"
    run_command=
}
on_error() {
    failed_line=$1
    failed_command=${run_command:-$2}
}
on_exit() {
    local status=$1 cleanup_status=0
    trap - ERR
    if ((status != 0)); then
        printf '\nFAIL: stage=%s exit=%s line=%s command=%s\n' \
            "$stage" "$status" "${failed_line:-unknown}" "${failed_command:-unknown}" >&2
        printf 'Repository: %s\nSite: %s\n' "${repo:-none}" "${site_url:-none}" >&2
        if ((repo_created)); then
            log 'Recent Pages workflow runs:'
            gh run list --repo "$repo" --limit 5 \
                --json databaseId,workflowName,status,conclusion,headSha 2>&1 || true
            if [[ -n ${run_id:-} ]]; then
                log "Failed workflow log for run $run_id, if any:"
                gh run view "$run_id" --repo "$repo" --log-failed 2>&1 || true
            fi
            log 'Pages configuration:'
            gh api "repos/$repo/pages" --jq '{status, build_type, html_url}' 2>&1 || true
            if [[ $stage == site ]]; then
                log 'Published file HTTP responses:'
                for path in setup.sh deb/Packages.gz rpm/repodata/repomd.xml KEY.gpg; do
                    curl -sSIL --max-time 10 "$site_url/$path" 2>&1 || true
                done
            fi
        fi
    fi
    if ((repo_created)); then
        stage=cleanup
        log "Delete disposable GitHub repository $repo"
        if ! gh repo delete "$repo" --yes; then
            printf 'CLEANUP FAILED: delete %s manually with: gh repo delete %q --yes\n' \
                "$repo" "$repo" >&2
            cleanup_status=1
        fi
    fi
    if [[ -n $work_dir ]]; then
        log "Remove temporary files in $work_dir"
        rm -rf -- "$work_dir" || cleanup_status=1
    fi
    if ((status == 0 && cleanup_status == 0)); then
        log 'PASS: both package types installed by both setup methods.'
    elif ((status == 0)); then
        printf 'FAIL: test passed but cleanup failed.\n' >&2
    fi
    if ((status == 0 && cleanup_status != 0)); then status=1; fi
    exit "$status"
}
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap 'on_exit "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for tool in gh git docker go curl od gpg gpgv python3 sha256sum; do
    command -v "$tool" >/dev/null || { printf 'Missing tool: %s\n' "$tool" >&2; exit 1; }
done
run gh auth status
if ! gh auth status 2>&1 | grep -q 'delete_repo'; then
    printf 'gh token needs delete_repo scope before this test can create a repository.\n' >&2
    printf 'Run: gh auth refresh -h github.com -s delete_repo\n' >&2
    exit 1
fi
run docker info >/dev/null
owner=$(gh api user --jq .login)
[[ -n $owner ]] || { printf 'GitHub login is empty\n' >&2; exit 1; }
# ghrepo uses GH_TOKEN for release uploads and Pages API calls.
export GH_TOKEN
GH_TOKEN=$(gh auth token)
[[ -n $GH_TOKEN ]] || { printf 'gh did not return a token\n' >&2; exit 1; }
[[ -f $root/contract/testdata/packages/hello_1.2.0-1_amd64.deb ]]
[[ -f $root/contract/testdata/packages/empty-0.1-1.x86_64.rpm ]]
log "Authenticated as $owner; Docker is available."

stage=build
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ghrepo-github-XXXXXX")
log 'Build the local ghrepo binary'
run go build -C "$root" -o "$work_dir/ghrepo" .
log 'Build the Linux binary for the Pages workflow'
run env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C "$root" \
    -o "$work_dir/ghrepo-linux-amd64" .
read -r builder_hash _ < <(sha256sum "$work_dir/ghrepo-linux-amd64")

stage=create
name="ghrepo-manual-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
repo="$owner/$name"
site_url="https://${owner,,}.github.io/$name"
log "Create public disposable repository $repo"
run gh repo create "$repo" --public
repo_created=1

stage=clone
log 'Clone the empty repository and create its initial commit'
run gh repo clone "$repo" "$work_dir/repo"
cd "$work_dir/repo"
run git checkout -B main
run git config user.name 'ghrepo manual test'
run git config user.email 'ghrepo-manual-test@users.noreply.github.com'
run git config credential.https://github.com.helper '!gh auth git-credential'
run git commit --allow-empty -m 'chore: create initial commit'
run git push -u origin HEAD:main

stage=release
log 'Upload the checked-in packages and the temporary Pages builder'
run gh release create v1 --repo "$repo" --target main --title 'Test packages' \
    --notes 'Disposable packages for the ghrepo manual test.' \
    "$root/contract/testdata/packages/hello_1.2.0-1_amd64.deb" \
    "$root/contract/testdata/packages/empty-0.1-1.x86_64.rpm" \
    "$work_dir/ghrepo-linux-amd64"

stage=init
log 'Initialize ghrepo with the selected signing key'
key_args=()
if [[ -n ${GHREPO_TEST_GPG_KEY_ID:-} ]]; then
    key_args=(--key-id "$GHREPO_TEST_GPG_KEY_ID")
fi
run "$work_dir/ghrepo" init --repo "$repo" "${key_args[@]}"

stage=workflow-config
log 'Set the generated Pages workflow to run the tested binary'
run python3 - ".github/workflows/pages.yml" "$repo" "$builder_hash" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
repo = sys.argv[2]
digest = sys.argv[3]
workflow = path.read_text()
start_marker = "      - name: Build site with signed ghrepo release\n"
end_marker = "      - uses: actions/upload-pages-artifact@v3\n"
if workflow.count(start_marker) != 1 or workflow.count(end_marker) != 1:
    raise SystemExit("generated Pages workflow has an unexpected build step")
start = workflow.index(start_marker)
end = workflow.index(end_marker, start)
replacement = (
    "      - name: Build site with local test binary\n"
    "        run: |\n"
    f"          curl -fsSL --retry 5 -o /tmp/ghrepo-linux-amd64 https://github.com/{repo}/releases/download/v1/ghrepo-linux-amd64\n"
    f"          echo '{digest}  /tmp/ghrepo-linux-amd64' | sha256sum -c -\n"
    "          chmod +x /tmp/ghrepo-linux-amd64\n"
    "          /tmp/ghrepo-linux-amd64 site-build\n"
)
path.write_text(workflow[:start] + replacement + workflow[end:])
PY

stage=pages
log 'Set GitHub Pages source to GitHub Actions'
run gh api --method POST "repos/$repo/pages" -f build_type=workflow >/dev/null

stage=add
log 'Add the public release assets to the manifest'
run "$work_dir/ghrepo" add \
    "https://github.com/$repo/releases/download/v1/hello_1.2.0-1_amd64.deb" \
    "https://github.com/$repo/releases/download/v1/empty-0.1-1.x86_64.rpm"

stage=publish
log 'Sign metadata, commit, and push the repository'
run "$work_dir/ghrepo" publish
head_sha=$(git rev-parse HEAD)

stage=workflow
log "Wait up to 15 minutes for Pages workflow at $head_sha"
deadline=$((SECONDS + 900))
run_id=
while ((SECONDS < deadline)); do
    if ! run_id=$(gh run list --repo "$repo" --commit "$head_sha" --limit 20 \
        --json databaseId,workflowName --jq '[.[] | select(.workflowName == "Pages") | .databaseId][0] // empty' 2>&1); then
        if [[ $run_id == *'HTTP 404:'* ]]; then
            log 'GitHub Actions is still indexing the workflow'
            run_id=
        else
            failed_command='List Pages workflow runs'
            printf '%s\n' "$run_id" >&2
            exit 1
        fi
    fi
    if [[ -n $run_id ]]; then break; fi
    sleep 10
done
[[ -n $run_id ]] || { failed_command='Pages workflow did not start'; printf 'Timed out waiting for Pages workflow to start\n' >&2; exit 1; }
log "Pages workflow run: $run_id"
while ((SECONDS < deadline)); do
    state=$(gh run view "$run_id" --repo "$repo" --json status,conclusion \
        --jq '.status + " " + (.conclusion // "")')
    log "Workflow: $state"
    if [[ $state == 'completed success' ]]; then break; fi
    if [[ $state == completed\ * ]]; then
        failed_command="Pages workflow $run_id: $state"
        printf 'Pages workflow failed: %s\n' "$state" >&2
        exit 1
    fi
    sleep 10
done
[[ $state == 'completed success' ]] || { failed_command="Pages workflow $run_id: $state"; printf 'Timed out waiting for Pages workflow\n' >&2; exit 1; }

stage=site
log "Wait up to 5 minutes for published files at $site_url"
deadline=$((SECONDS + 300))
site_ready=0
while ((SECONDS < deadline)); do
    if curl -fsSL --max-time 15 "$site_url/setup.sh" -o "$work_dir/setup.sh" 2>/dev/null &&
       curl -fsSL --max-time 15 "$site_url/deb/Packages.gz" -o /dev/null 2>/dev/null &&
       curl -fsSL --max-time 15 "$site_url/rpm/repodata/repomd.xml" -o /dev/null 2>/dev/null; then
        site_ready=1
        break
    fi
    sleep 10
done
((site_ready)) || { printf 'Pages files did not become available\n' >&2; exit 1; }
curl -fsSL --max-time 15 "$site_url/KEY.gpg" -o "$work_dir/KEY.gpg"

stage=deb-manual
log 'Install DEB manually in a clean Debian container'
run docker run --rm -e "SITE_URL=$site_url" debian:bookworm-slim bash -euc '
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates curl
    curl -fsSLo /usr/share/keyrings/ghrepo-archive-keyring.gpg "$SITE_URL/KEY.gpg"
    printf "deb [signed-by=/usr/share/keyrings/ghrepo-archive-keyring.gpg] %s/deb/ ./\n" "$SITE_URL" > /etc/apt/sources.list.d/ghrepo.list
    apt-get update
    apt-get install -y --no-install-recommends hello=1.2.0-1
    dpkg-query -W -f "\${Status}\n" hello | grep -qx "install ok installed"
'
stage=deb-setup
log 'Install DEB using setup.sh in a new Debian container'
run docker run --rm -e "SITE_URL=$site_url" debian:bookworm-slim bash -euc '
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates curl
    mkdir /tmp/ghrepo-setup && cd /tmp/ghrepo-setup
    curl -fsSLo setup.sh "$SITE_URL/setup.sh"
    sh setup.sh
    install -m 644 KEY.gpg /usr/share/keyrings/ghrepo-archive-keyring.gpg
    install -m 644 deb.list /etc/apt/sources.list.d/ghrepo.list
    apt-get update
    apt-get install -y --no-install-recommends hello=1.2.0-1
    dpkg-query -W -f "\${Status}\n" hello | grep -qx "install ok installed"
'
stage=rpm-manual
log 'Install RPM manually in a clean AlmaLinux container'
run docker run --rm -e "SITE_URL=$site_url" almalinux:9 bash -euc '
    dnf -y install ca-certificates curl-minimal
    curl -fsSLo /etc/pki/rpm-gpg/GHREPO-KEY "$SITE_URL/KEY.gpg"
    printf "[ghrepo]\nname=ghrepo test\nbaseurl=%s/rpm/\nenabled=1\nrepo_gpgcheck=1\ngpgcheck=0\ngpgkey=file:///etc/pki/rpm-gpg/GHREPO-KEY\n" "$SITE_URL" > /etc/yum.repos.d/ghrepo.repo
    dnf -y --disablerepo="*" --enablerepo=ghrepo makecache
    dnf -y --disablerepo="*" --enablerepo=ghrepo install empty
    rpm -q empty
'
stage=rpm-setup
log 'Install RPM using setup.sh in a new AlmaLinux container'
run docker run --rm -e "SITE_URL=$site_url" almalinux:9 bash -euc '
    dnf -y install ca-certificates curl-minimal
    mkdir /tmp/ghrepo-setup && cd /tmp/ghrepo-setup
    curl -fsSLo setup.sh "$SITE_URL/setup.sh"
    sh setup.sh
    install -m 644 KEY.gpg /etc/pki/rpm-gpg/GHREPO-KEY
    install -m 644 rpm.repo /etc/yum.repos.d/ghrepo.repo
    dnf -y --disablerepo="*" --enablerepo=ghrepo makecache
    dnf -y --disablerepo="*" --enablerepo=ghrepo install empty
    rpm -q empty
'

stage=review
log "All checks passed. Inspect the site at $site_url"
printf 'Press Enter to delete %s and its temporary files... ' "$repo"
IFS= read -r _ </dev/tty
