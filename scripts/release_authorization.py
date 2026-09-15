#!/usr/bin/env python3
"""Probe the actual Actions publisher without uploading artifacts or creating tags."""
import base64
from datetime import datetime, timezone
import json
import os
import subprocess
import urllib.error
import urllib.parse
import urllib.request

import package_installers
import package_release as release

REPO = 'BrokkAi/bug-bot'


def exchange_expiry(value):
    if isinstance(value, int) and not isinstance(value, bool):
        return datetime.fromtimestamp(value, timezone.utc)
    if isinstance(value, str):
        return datetime.fromisoformat(value.replace('Z', '+00:00'))
    raise ValueError('npm exchange returned an unsupported expiry')


def check_subject(claims, repository):
    owner_id, repo_id = str(repository['owner']['id']), str(repository['id'])
    subjects = {
        f'repo:{REPO}:environment:packages-publish',
        f'repo:BrokkAi@{owner_id}/bug-bot@{repo_id}:environment:packages-publish',
    }
    if (claims.get('repository') != REPO or
            str(claims.get('repository_owner_id')) != owner_id or
            str(claims.get('repository_id')) != repo_id or
            claims.get('sub') not in subjects):
        raise ValueError('OIDC token does not identify this repository and packages-publish environment')


def request(url, token, method='GET'):
    req = urllib.request.Request(url, data=b'{}' if method == 'POST' else None,
                                 headers={'Authorization': f'Bearer {token}', 'Content-Type': 'application/json'}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        # Never include response bodies or credentials in errors.
        raise ValueError(f'{method} {url.split("?")[0]} returned HTTP {error.code}') from None


def gh(*args):
    return json.loads(subprocess.check_output(['gh', 'api', '--hostname', 'github.com', *args]))


def github():
    if os.environ.get('GITHUB_REPOSITORY') != REPO or not os.environ.get('GITHUB_ACTIONS'):
        raise ValueError('authorization must run in the publishing Actions job')
    # An unpublished, empty, disposable draft tests release write access. A draft
    # does not create its tag. Its deletion must not invalidate run evidence.
    tag = f"preflight-probe-{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}"
    record = gh(f'repos/{REPO}/releases', '-X', 'POST', '-f', f'tag_name={tag}',
                '-f', f'target_commitish={release.commit()}', '-F', 'draft=true', '-f', 'name=Disposable authorization probe')
    try:
        if not record.get('draft') or record.get('assets'):
            raise ValueError('authorization probe is not an empty private draft')
        gh(f"repos/{REPO}/releases/{record['id']}", '-X', 'PATCH', '-f', 'body=Release write authorization verified; no uploads.')
    finally:
        subprocess.run(['gh', 'api', '--hostname', 'github.com', f"repos/{REPO}/releases/{record['id']}", '-X', 'DELETE'], check=True)
    print('GitHub publishing job created, updated and deleted an empty private draft with its GITHUB_TOKEN; no tag or assets created.')


def npm():
    expected = f'{REPO}/.github/workflows/publish-packages.yml@'
    if (not os.environ.get('GITHUB_WORKFLOW_REF', '').startswith(expected)
            or os.environ.get('GITHUB_JOB') != 'packages'):
        raise ValueError('npm authorization must run in publish-packages.yml / packages')
    url = os.environ['ACTIONS_ID_TOKEN_REQUEST_URL'] + '&audience=npm:registry.npmjs.org'
    token = request(url, os.environ['ACTIONS_ID_TOKEN_REQUEST_TOKEN'])['value']
    claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '==='))
    check_subject(claims, gh(f'repos/{REPO}'))
    if claims.get('sha') != release.commit():
        raise ValueError('OIDC token does not identify the exact commit and packages-publish environment')
    names = [f'{package_installers.NPM_ROOT}-{system}-{arch}' for system in ('linux', 'darwin') for arch in ('x64', 'arm64')]
    names.append(package_installers.NPM_ROOT)
    failures = []
    for name in names:
        try:
            escaped = urllib.parse.quote(name, safe='')
            exchanged = request(f'https://registry.npmjs.org/-/npm/v1/oidc/token/exchange/package/{escaped}', token, 'POST')
            expiry = exchange_expiry(exchanged['expires'])
            if exchanged.get('token_type') != 'oidc' or not exchanged.get('token') or (expiry - datetime.now(timezone.utc)).total_seconds() < 60:
                raise ValueError('OIDC exchange returned invalid or expiring credentials')
            print(f'{name}: exact workflow/environment OIDC exchange accepted; credential expiry validated')
        except (ValueError, KeyError) as error:
            failures.append(f'{name}: {error}')
    if failures:
        raise ValueError('Publishing authorization blocked: package-scoped OIDC exchange failed for BrokkAi/bug-bot / publish-packages.yml / packages-publish. ' + '; '.join(failures))


if __name__ == '__main__':
    github()
    npm()
