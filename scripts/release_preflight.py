#!/usr/bin/env python3
"""Exact-commit release checks and resumable publication. Only `publish` writes."""
import argparse
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import urllib.error

import package_installers
import package_registry
import package_release as release

REPO = 'BrokkAi/bug-bot'
WORKFLOW = '.github/workflows/publish-packages.yml'
NATIVE = Path('dist/native')
PACKAGES = Path('dist/packages')


def gh(*args):
    return subprocess.check_output(['gh', *args], text=True)


def api(path, missing=False):
    result = subprocess.run(['gh', 'api', '--hostname', 'github.com', f'repos/{REPO}/{path}'], capture_output=True, text=True)
    if result.returncode:
        if missing and '(HTTP 404)' in result.stderr:
            return None
        raise ValueError(f'GitHub API failed: {path}: {result.stderr}')
    return json.loads(result.stdout)


def context():
    tag, sha = os.environ['RELEASE_TAG'], os.environ['RELEASE_COMMIT']
    release.validate_tag(tag)
    if release.commit() != sha or release.run('git', 'status', '--porcelain').strip():
        raise ValueError('release checks require the exact clean prepared commit')
    target = os.environ.get('RELEASE_TARGET')
    if target:
        release.run('git', 'merge-base', '--is-ancestor', target, sha)
    return tag, sha


def snapshot(data):
    result = {}
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
        for member in archive.getmembers():
            if member.isdir():
                continue
            if not member.isfile() or member.name in result or '..' in Path(member.name).parts or member.name.startswith('/'):
                raise ValueError('unsafe or duplicate archive member')
            result[member.name] = (member.mode & 0o777, archive.extractfile(member).read())
    return result


def compare_native(directory, tag, sha):
    release.verify_local(tag, directory, sha)
    release.verify_local(tag, NATIVE, sha)
    for target in release.TARGETS:
        filename = release.archive_name(tag, target)
        if snapshot((directory / filename).read_bytes()) != snapshot((NATIVE / filename).read_bytes()):
            raise ValueError(f'published native payload differs: {filename}')


def remote_run(tag, sha, authorization=False):
    runs = api(f'actions/workflows/publish-packages.yml/runs?head_sha={sha}&event=workflow_dispatch&per_page=100')['workflow_runs']
    runs = [r for r in runs if r['head_sha'] == sha and r['path'] == WORKFLOW
            and r['display_title'] == f'Release {tag} (publish=false)']
    if not runs:
        raise ValueError('missing exact-commit non-publishing workflow run')
    run = max(runs, key=lambda r: r['id'])
    if run['status'] != 'completed' or run['conclusion'] != 'success':
        raise ValueError(f"preflight run {run['id']} is {run['status']}/{run['conclusion']}")
    jobs = api(f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")['jobs']
    expected = {'native / checks / test (ubuntu-latest)', 'native / checks / test (macos-latest)', 'native / build', 'packages'}
    if {j['name'] for j in jobs} != expected or any(j['conclusion'] != 'success' or j['head_sha'] != sha for j in jobs):
        raise ValueError('missing, failed or mismatched publishing-context jobs')
    package_job = next(j for j in jobs if j['name'] == 'packages')
    for name in ('Validate every destination before any final upload', 'Check actual publishing credentials without uploads'):
        if not any(s['name'] == name and s['conclusion'] == 'success' for s in package_job['steps']):
            raise ValueError(f'missing successful step: {name}')
    if authorization:
        # Authorization is mutable; stale historical runs cannot establish readiness.
        from datetime import datetime, timezone
        age = datetime.now(timezone.utc) - datetime.fromisoformat(run['updated_at'].replace('Z', '+00:00'))
        if age.total_seconds() > 3600:
            raise ValueError('authorization evidence expired; dispatch publish=false again at this commit')
    artifacts = api(f"actions/runs/{run['id']}/artifacts?per_page=100")['artifacts']
    for name in (f'native-{tag}', f'packages-{tag}'):
        matching = [a for a in artifacts if a['name'] == name and not a['expired'] and a['size_in_bytes'] > 0
                    and a.get('workflow_run', {}).get('head_sha') == sha]
        if len(matching) != 1:
            raise ValueError(f'missing exact-commit artifact: {name}')
    return run


def stage(tag, sha):
    if not NATIVE.exists() or not PACKAGES.exists():
        run = remote_run(tag, sha)
        for name, path in ((f'native-{tag}', NATIVE), (f'packages-{tag}', PACKAGES)):
            if not path.exists():
                gh('run', 'download', str(run['id']), '--repo', f'github.com/{REPO}', '--name', name, '--dir', str(path))
    release.verify_local(tag, NATIVE, sha)
    manifest = json.loads((PACKAGES / 'npm/manifest.json').read_text())
    if manifest['tag'] != tag or manifest['commit'] != sha:
        raise ValueError('npm artifacts mismatch the exact release commit/version')
    return manifest


def tag_version(tag, sha, required=False):
    ref = api(f'git/ref/tags/{tag}', missing=True)
    if not ref:
        if required:
            raise ValueError('release tag missing')
        return
    obj = ref['object']
    while obj['type'] == 'tag':
        obj = api(f"git/tags/{obj['sha']}")['object']
    if obj['type'] != 'commit' or obj['sha'] != sha:
        raise ValueError('release tag points at a different commit')


def release_record(tag):
    record = api(f'releases/tags/{tag}', missing=True)
    if record:
        return record
    # GitHub's release-by-tag endpoint hides drafts, even from their creator.
    # The authorized releases list includes them so a failed upload can resume.
    for page in range(1, 100):
        records = api(f'releases?per_page=100&page={page}')
        for candidate in records:
            if candidate['tag_name'] == tag:
                return candidate
        if len(records) < 100:
            break
    return None


def github_version(tag, sha, required=False, strict=False):
    tag_version(tag, sha, required)
    record = release_record(tag)
    if not record:
        if required:
            raise ValueError('GitHub release missing')
        return None
    if required and record['draft']:
        raise ValueError('GitHub release is still a draft')
    expected = {p.name for p in NATIVE.iterdir()}
    names = [a['name'] for a in record['assets']]
    if len(names) != len(set(names)) or set(names) - expected:
        raise ValueError('unexpected or duplicate GitHub assets')
    with tempfile.TemporaryDirectory() as temporary:
        directory = Path(temporary)
        if names:
            gh('release', 'download', tag, '--repo', f'github.com/{REPO}', '--dir', str(directory))
        if record['draft']:
            # Recovery of partial staging requires the original exact bytes.
            for path in directory.iterdir():
                if path.read_bytes() != (NATIVE / path.name).read_bytes():
                    raise ValueError(f'conflicting staged GitHub asset: {path.name}; preserve draft for inspection')
        else:
            compare_native(directory, tag, sha)
        if strict:
            if set(names) != expected or any((directory / name).read_bytes() != (NATIVE / name).read_bytes() for name in names):
                raise ValueError('uploaded bytes differ from exact staged bytes')
    return record


def version(tag, sha):
    stage(tag, sha)
    github_version(tag, sha)
    package_registry.run('check', PACKAGES)


def published(tag, sha):
    stage(tag, sha)
    destination = os.environ.get('RELEASE_DESTINATION', '')
    if destination.startswith('@brokkai/'):
        package_registry.run('verify', PACKAGES)  # stronger: all dependent packages
    else:
        github_version(tag, sha, required=True)
        if destination.startswith('github.com/'):
            # Validate Go module contents at the immutable tag without modifying go.mod.
            with tempfile.TemporaryDirectory() as temp:
                env = dict(os.environ, GOPROXY='direct', GOWORK='off')
                result = subprocess.check_output(['go', 'mod', 'download', '-json', f'github.com/BrokkAi/bug-bot@{tag}'], cwd=temp, env=env)
                info = json.loads(result)
                if info.get('Error') or info.get('Version') != tag or info.get('Origin', {}).get('Hash') != sha:
                    raise ValueError('Go module does not resolve to exact version and commit')


def publish(tag, sha):
    # No writes before all versions and the actual credentials have been checked.
    import release_authorization
    if os.environ.get('GITHUB_REF') != f'refs/tags/{tag}':
        raise ValueError('publication requires an existing exact release tag')
    version(tag, sha)
    tag_version(tag, sha, required=True)
    record = github_version(tag, sha)
    if record and not record['draft']:
        package_registry.run('verify', PACKAGES)
        return
    release_authorization.github()
    release_authorization.npm()
    prerelease = '-' in tag
    if not record:
        gh('release', 'create', tag, '--repo', f'github.com/{REPO}', '--verify-tag', '--draft', '--title', f'Brokk Bug Bot {tag}', '--generate-notes',
           *(['--prerelease'] if prerelease else []))
        record = release_record(tag)
    present = {a['name'] for a in record['assets']}
    for path in sorted(NATIVE.iterdir()):
        if path.name not in present:
            gh('release', 'upload', tag, str(path), '--repo', f'github.com/{REPO}')
    github_version(tag, sha, strict=True)
    package_registry.run('publish', PACKAGES)
    package_registry.run('verify', PACKAGES)
    # Set both flags on finalization too, including drafts staged by older code.
    gh('release', 'edit', tag, '--repo', f'github.com/{REPO}', '--draft=false',
       f'--prerelease={str(prerelease).lower()}', '--latest=false' if prerelease else '--latest')
    github_version(tag, sha, required=True, strict=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['build', 'authorization', 'version', 'published', 'publish'])
    args = parser.parse_args()
    tag, sha = context()
    if args.command in ('build', 'authorization'):
        run = remote_run(tag, sha, args.command == 'authorization')
        if args.command == 'build':
            stage(tag, sha)
        print(f"Verified exact-commit {args.command} evidence: {run['html_url']}")
    else:
        globals()[args.command](tag, sha)


if __name__ == '__main__':
    main()
