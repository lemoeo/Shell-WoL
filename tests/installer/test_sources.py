"""Transport selection must preserve exact versions and integrity checks."""
import json
import unittest

import test_install as fixture


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.f = fixture.InstallerTests(); self.f.setUp()

    def tearDown(self): self.f.tearDown()
    def requests(self): return (self.f.base/'downloads').read_text().splitlines()

    def test_explicit_github_never_contacts_cdn(self):
        self.f.run_install('--source', 'github', '--service', service=True)
        self.assertTrue(all('jsdelivr.net' not in u for u in self.requests()))

    def test_omitted_source_defaults_to_github(self):
        # An old terminal setting must not silently change the default source.
        self.f.env['SHELL_WOL_SOURCE'] = 'jsdelivr'
        self.f.run_install('--service', service=True)
        self.assertTrue(all('jsdelivr.net' not in u for u in self.requests()))

    def test_jsdelivr_uses_asset_and_source_tags(self):
        self.f.run_install('--source', 'jsdelivr', '--service', service=True)
        requests = self.requests()
        self.assertTrue(all(u.startswith('https://cdn.jsdelivr.net/gh/') for u in requests))
        self.assertTrue(any('@assets/VERSION' in u for u in requests))
        self.assertTrue(any('@assets-v1.2.3/shell-wol-linux-amd64' in u for u in requests))
        self.assertTrue(any('@v1.2.3/deploy/shell-wol.service' in u for u in requests))
        self.assertTrue(any('@main/scripts/menu.sh' in u for u in requests))
        self.assertFalse(any('/releases/' in u or '@main/deploy/shell-wol.' in u or '@latest' in u for u in requests))

    def test_testingcf_environment_default_with_cli_override(self):
        self.f.env['SHELL_WOL_CDN_BASE'] = 'https://testingcf.jsdelivr.net'
        self.f.env['FIXTURE_CDN_BASE'] = 'https://testingcf.jsdelivr.net'
        self.f.run_install('--source', 'jsdelivr')
        self.assertTrue(all(u.startswith('https://testingcf.jsdelivr.net/') for u in self.requests()))
        (self.f.base/'downloads').unlink()
        self.f.run_install('--source', 'github', '--cdn-base', 'https://cdn.jsdelivr.net/')
        self.assertTrue(all('jsdelivr.net' not in u for u in self.requests()))

    def test_cdn_domain_can_be_changed_by_parameter(self):
        self.f.env['FIXTURE_CDN_BASE'] = 'https://testingcf.jsdelivr.net'
        self.f.run_install('--source', 'jsdelivr', '--cdn-base', 'https://testingcf.jsdelivr.net/')
        self.assertTrue(all(u.startswith('https://testingcf.jsdelivr.net/') for u in self.requests()))

    def test_default_github_failure_does_not_switch_to_cdn(self):
        self.f.env['FAIL_URL_PREFIXES'] = json.dumps(['https://github.com/', 'https://raw.githubusercontent.com/'])
        self.f.run_install('--service', service=True, success=False)
        requests = self.requests()
        self.assertIn('/latest/download/VERSION', requests[0])
        self.assertEqual(len(requests), 1)
        self.assertFalse(self.f.root.exists())

    def test_github_failure_after_version_resolution_does_not_switch(self):
        self.f.make_release('v1.2.4')
        self.f.env['CDN_VERSION'] = 'v1.2.3'
        self.f.env['FAIL_URL_PREFIXES'] = json.dumps(['https://github.com/lemoeo/Shell-WoL/releases/download/'])
        self.f.run_install('--service', service=True, success=False)
        requests = self.requests()
        self.assertTrue(any('/download/v1.2.4/' in u for u in requests))
        self.assertFalse(any('jsdelivr.net' in u for u in requests))
        self.assertFalse(self.f.root.exists())

    def test_stale_cdn_version_still_installs_consistent_snapshot(self):
        self.f.make_release('v1.2.4'); self.f.env['CDN_VERSION'] = 'v1.2.3'
        self.f.run_install('--source', 'jsdelivr', '--service', service=True)
        self.assertIn('v1.2.3', (self.f.root/'bin/shell-wol').read_text())
        self.assertFalse(any('@v1.2.4/' in u or '@assets-v1.2.4/' in u for u in self.requests()))

    def test_fixed_version_bypasses_latest_pointer(self):
        self.f.run_install('--source', 'jsdelivr', '--version', 'v1.2.3')
        self.assertFalse(any('@assets/VERSION' in u or '/latest/' in u for u in self.requests()))

    def test_bad_cdn_digest_is_fatal_without_fallback(self):
        self.f.env['CORRUPT_CDN_SUFFIX'] = '/shell-wol-linux-amd64'
        self.f.run_install('--source', 'jsdelivr', success=False)
        self.assertTrue(all('cdn.jsdelivr.net' in u for u in self.requests()))
        self.assertFalse(self.f.root.exists())

    def test_explicit_source_failure_does_not_switch(self):
        self.f.env['FAIL_URL_PREFIXES'] = json.dumps(['https://github.com/'])
        self.f.run_install('--source', 'github', success=False)
        self.assertEqual(len(self.requests()), 1)
        self.assertFalse(self.f.root.exists())

    def test_custom_addresses_override_profile_without_automatic_fallback(self):
        self.f.env['FIXTURE_RELEASE_BASE'] = 'https://release.example/releases'
        self.f.env['FIXTURE_REPO_BASE'] = 'https://repo.example/raw/lemoeo/Shell-WoL'
        options = ('--source', 'jsdelivr', '--download-base', self.f.env['FIXTURE_RELEASE_BASE'],
                   '--repo-base', self.f.env['FIXTURE_REPO_BASE'], '--service')
        self.f.run_install(*options, service=True)
        self.assertTrue(all('jsdelivr.net' not in u for u in self.requests()))
        (self.f.base/'downloads').unlink()
        self.f.env['FAIL_URL_PREFIXES'] = json.dumps(['https://release.example/'])
        self.f.run_install(*options, service=True, success=False)
        self.assertEqual(len(self.requests()), 1)

    def test_invalid_sources_and_cdn_urls_fail_before_download(self):
        for args in (('--source', 'unknown'), ('--source', 'auto'), ('--cdn-base', 'http://cdn.jsdelivr.net'),
                     ('--cdn-base', 'https://cdn.jsdelivr.net/gh'),
                     ('--cdn-base', 'https://cdn.jsdelivr.net?x=1'),
                     ('--cdn-base', 'https://cdn.jsdelivr.net\ninvalid')):
            with self.subTest(args=args): self.f.run_install(*args, success=False)
        self.assertFalse((self.f.base/'downloads').exists())


if __name__ == '__main__': unittest.main(verbosity=2)
