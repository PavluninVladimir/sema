import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("sema_update", Path(__file__).with_name("sema-update.py"))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)
VERSION = "build-" + "a" * 40


class ReleaseTest(unittest.TestCase):
    def test_webhook_consumes_signal_with_the_same_update_lock(self):
        with tempfile.TemporaryDirectory() as cache, \
             patch.dict(update.os.environ, {"XDG_CACHE_HOME": cache}), \
             patch.object(update.platform, "machine", return_value="aarch64"), \
             patch.object(sys, "argv", ["sema-update.py", "--apply", "--from-webhook"]), \
             patch.object(update, "fetch_json", return_value={"tag_name": VERSION}), \
             patch.object(update, "asset_for", return_value=("url", "sha256")), \
             patch.object(update, "health", return_value=VERSION), \
             patch.object(update.fcntl, "flock") as flock:
            def consume(*args):
                self.assertEqual(flock.call_args.args[1], update.fcntl.LOCK_EX)
                self.assertEqual(args, ("exec", "sema", "rm", "-f", "--", "/data/.release-webhook/request"))

            with patch.object(update, "docker", side_effect=consume) as docker:
                update.main()
                docker.assert_called_once()
            self.assertTrue((Path(cache) / "sema-updater" / "sema.lock").is_file())

    def test_asset_is_bound_to_published_repository_and_digest(self):
        repository = "example/sema"
        name = "sema-linux-arm64.tar.gz"
        url = f"https://github.com/{repository}/releases/download/{VERSION}/{name}"
        digest = "b" * 64
        asset = {"name": name, "browser_download_url": url, "digest": "sha256:" + digest}
        release = {"tag_name": VERSION, "assets": [asset]}
        self.assertEqual(update.asset_for(release, repository, "arm64"), (url, digest))
        self.assertRaises(RuntimeError, update.asset_for, release, "other/sema", "arm64")
        asset["digest"] = None
        self.assertRaises(RuntimeError, update.asset_for, release, repository, "arm64")

    def test_failed_candidate_restores_previous_container(self):
        previous = {"Id": "abc123abc123"}
        states = {
            "abc123abc123": {"Id": "abc123abc123", "Name": "/sema", "State": {"Running": True}},
            "candidate": {"Id": "candidate", "Name": "/candidate", "State": {"Running": False}},
        }

        def inspect(name):
            if name in states:
                return states[name]
            return next(state for state in states.values() if state["Name"] == "/" + name)

        def docker(action, *args):
            if action == "stop":
                states[args[-1]]["State"]["Running"] = False
            elif action == "rename":
                states[args[0]]["Name"] = "/" + args[1]
            elif action == "start":
                states[args[0]]["State"]["Running"] = True
            else:
                self.fail(f"Неожиданный вызов {action}")

        with patch.object(update, "make_candidate", return_value="candidate"), \
             patch.object(update, "inspect", side_effect=inspect), \
             patch.object(update, "docker", side_effect=docker), \
             patch.object(update, "health", return_value="current"), \
             patch.object(update, "wait_health", side_effect=[False, True]):
            with self.assertRaisesRegex(RuntimeError, "прежняя версия восстановлена"):
                update.switch(previous, "image", "sema", "current", VERSION, "health", "example/sema", "")
        self.assertEqual(inspect("sema")["Id"], previous["Id"])
        self.assertTrue(inspect("sema")["State"]["Running"])
        self.assertFalse(inspect("candidate")["State"]["Running"])
