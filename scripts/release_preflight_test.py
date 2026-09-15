import io
from datetime import datetime, timezone, timedelta
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_preflight as preflight
import release_authorization as authorization


def archive(payload=b'binary', mode=0o755, mtime=0):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w:gz') as tar:
        member = tarfile.TarInfo('bbb')
        member.size, member.mode, member.mtime = len(payload), mode, mtime
        tar.addfile(member, io.BytesIO(payload))
    return output.getvalue()


class ReleasePreflight(unittest.TestCase):
    def test_publication_sets_release_channel_for_new_and_resumed_drafts(self):
        for tag in ('v0.4.0-rc.1', 'v0.4.0-beta', 'v0.4.0'):
            for resumed in (False, True):
                with self.subTest(tag=tag, resumed=resumed), tempfile.TemporaryDirectory() as temporary:
                    prerelease = '-' in tag
                    draft = {'draft': True, 'prerelease': False, 'assets': []}
                    preflight.release.validate_tag(tag)
                    with patch.dict(os.environ, GITHUB_REF=f'refs/tags/{tag}'), \
                            patch.object(preflight, 'NATIVE', Path(temporary)), \
                            patch.object(preflight, 'version'), patch.object(preflight, 'tag_version'), \
                            patch.object(preflight, 'github_version', return_value=draft if resumed else None), \
                            patch.object(preflight, 'release_record', return_value=draft), \
                            patch.object(authorization, 'github'), patch.object(authorization, 'npm'), \
                            patch.object(preflight.package_registry, 'run') as registry, \
                            patch.object(preflight, 'gh') as gh:
                        preflight.publish(tag, 'a'*40)
                    commands = [call.args for call in gh.call_args_list]
                    creates = [args for args in commands if args[:2] == ('release', 'create')]
                    self.assertEqual(len(creates), 0 if resumed else 1)
                    if creates:
                        self.assertEqual('--prerelease' in creates[0], prerelease)
                    edits = [args for args in commands if args[:2] == ('release', 'edit')]
                    self.assertEqual(edits, [('release', 'edit', tag, '--repo', f'github.com/{preflight.REPO}',
                                             '--draft=false', f'--prerelease={str(prerelease).lower()}',
                                             '--latest=false' if prerelease else '--latest')])
                    self.assertEqual([call.args[0] for call in registry.call_args_list], ['publish', 'verify'])

    def test_private_draft_is_found_when_release_by_tag_returns_404(self):
        draft = {'tag_name': 'v0.3.4', 'draft': True, 'assets': []}
        def fake_api(path, missing=False):
            if path == 'releases/tags/v0.3.4':
                self.assertTrue(missing)
                return None
            self.assertEqual(path, 'releases?per_page=100&page=1')
            return [draft]
        with patch.object(preflight, 'api', side_effect=fake_api):
            self.assertIs(preflight.release_record('v0.3.4'), draft)

    def test_npm_exchange_accepts_numeric_unix_expiry(self):
        expiry = datetime.now(timezone.utc) + timedelta(hours=1)
        self.assertEqual(authorization.exchange_expiry(int(expiry.timestamp())).timestamp(), int(expiry.timestamp()))
        with self.assertRaisesRegex(ValueError, 'unsupported expiry'):
            authorization.exchange_expiry(True)

    def test_oidc_subject_accepts_immutable_repository_ids_and_rejects_other_environments(self):
        repository = {'id': 1360442595, 'owner': {'id': 204942796}}
        claims = {'repository': 'BrokkAi/bug-bot', 'repository_id': '1360442595',
                  'repository_owner_id': '204942796',
                  'sub': 'repo:BrokkAi@204942796/bug-bot@1360442595:environment:packages-publish'}
        authorization.check_subject(claims, repository)
        claims['sub'] = claims['sub'].replace('packages-publish', 'other')
        with self.assertRaisesRegex(ValueError, 'environment'):
            authorization.check_subject(claims, repository)

    def test_rebuild_comparison_checks_payload_and_permissions_not_compressor(self):
        self.assertEqual(preflight.snapshot(archive(mtime=1)), preflight.snapshot(archive(mtime=2)))
        self.assertNotEqual(preflight.snapshot(archive()), preflight.snapshot(archive(payload=b'other')))
        self.assertNotEqual(preflight.snapshot(archive()), preflight.snapshot(archive(mode=0o644)))

    def test_missing_wrong_commit_and_failed_runs_are_not_evidence(self):
        sha = 'a' * 40
        cases = [[], [{'head_sha': 'b'*40}], [{
            'head_sha': sha, 'path': preflight.WORKFLOW, 'display_title': 'Release v0.2.2 (publish=false)',
            'id': 1, 'status': 'completed', 'conclusion': 'failure'}]]
        for runs in cases:
            with patch.object(preflight, 'api', return_value={'workflow_runs': runs}):
                with self.assertRaises(ValueError):
                    preflight.remote_run('v0.2.2', sha)

    def test_tag_conflict_fails(self):
        with patch.object(preflight, 'api', return_value={'object': {'type': 'commit', 'sha': 'b'*40}}):
            with self.assertRaisesRegex(ValueError, 'different commit'):
                preflight.tag_version('v0.2.2', 'a'*40)

    def test_publication_failure_before_auth_causes_no_writes(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version', side_effect=ValueError('conflict')), \
                patch.object(preflight, 'gh') as gh, patch.object(authorization, 'github') as auth:
            with self.assertRaisesRegex(ValueError, 'conflict'):
                preflight.publish('v0.2.2', 'a'*40)
            gh.assert_not_called()
            auth.assert_not_called()

    def test_authorization_failure_prevents_any_final_upload(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version'), patch.object(preflight, 'tag_version'), \
                patch.object(preflight, 'github_version', return_value=None), \
                patch.object(authorization, 'github'), patch.object(authorization, 'npm', side_effect=ValueError('permission')), \
                patch.object(preflight, 'gh') as gh:
            with self.assertRaisesRegex(ValueError, 'permission'):
                preflight.publish('v0.2.2', 'a'*40)
            gh.assert_not_called()

    def test_local_identity_cannot_validate_workflow_credentials(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, 'Actions job'):
                authorization.github()
            with self.assertRaisesRegex(ValueError, 'publish-packages.yml'):
                authorization.npm()

    def test_published_recovery_uses_read_only_verification(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version'), patch.object(preflight, 'tag_version'), \
                patch.object(authorization, 'github'), patch.object(authorization, 'npm'), \
                patch.object(preflight, 'github_version', return_value={'draft': False}), \
                patch.object(preflight.package_registry, 'run') as registry, patch.object(preflight, 'gh') as gh:
            preflight.publish('v0.2.2', 'a'*40)
            registry.assert_called_once_with('verify', preflight.PACKAGES)
            gh.assert_not_called()
