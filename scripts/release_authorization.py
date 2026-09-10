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
    if claims.get('sub') != f'repo:{REPO}:environment:packages-publish' or claims.get('sha') != release.commit():
        raise ValueError('OIDC token does not identify the exact commit and packages-publish environment')
    names = [f'{package_installers.NPM_ROOT}-{system}-{arch}' for system in ('linux', 'darwin') for arch in ('x64', 'arm64')]
    names.append(package_installers.NPM_ROOT)
    failures = []
    for name in names:
        try:
            escaped = urllib.parse.quote(name, safe='')
            exchanged = request(f'https://registry.npmjs.org/-/npm/v1/oidc/token/exchange/package/{escaped}', token, 'POST')
            expiry = datetime.fromisoformat(exchanged['expires'].replace('Z', '+00:00'))
            if exchanged.get('token_type') != 'oidc' or not exchanged.get('token') or (expiry - datetime.now(timezone.utc)).total_seconds() < 60:
                raise ValueError('OIDC exchange returned invalid or expiring credentials')
            print(f'{name}: exact workflow/environment OIDC exchange accepted; credential expiry validated')
            # Exchange alone also accepts stage-only trust. Require the direct
            # publishing grant, never infer it from a public metadata read.
            configs = request(f'https://registry.npmjs.org/-/package/{escaped}/trust', exchanged['token'])
            matching = [c for c in configs if c.get('type') == 'github'
                        and c.get('claims', {}).get('repository') == REPO
                        and c['claims'].get('workflow_ref') == {'file': 'publish-packages.yml'}
                        and c['claims'].get('environment') == 'packages-publish'
                        and 'createPackage' in c.get('permissions', [])]
            if not matching:
                raise ValueError('no inspectable matching direct-publish createPackage grant')
            print(f'{name}: direct publishing trust verified')
        except (ValueError, KeyError) as error:
            failures.append(f'{name}: {error}')
    if failures:
        raise ValueError('Publishing authorization blocked. npm owners must enable and provide inspectable direct-publish trust for BrokkAi/bug-bot / publish-packages.yml / packages-publish. ' + '; '.join(failures))


if __name__ == '__main__':
    github()
    npm()
