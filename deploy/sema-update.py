#!/usr/bin/env python3
"""Проверить выпуск Сёмы и при --apply обновить контейнер на хосте."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request


VERSION = re.compile(r"build-[0-9a-f]{40}\Z")
REPOSITORY = re.compile(r"[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+\Z")
ARCHES = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}


def fetch_json(url):
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json", "User-Agent": "sema-update"})
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.loads(response.read(1 << 20))


def asset_for(release, repository, arch):
    version = release["tag_name"]
    if not VERSION.fullmatch(version) or release.get("draft") or release.get("prerelease"):
        raise RuntimeError("неподдерживаемый выпуск")
    name = f"sema-linux-{arch}.tar.gz"
    expected = f"https://github.com/{repository}/releases/download/{version}/{name}"
    for asset in release["assets"]:
        if asset["name"] != name:
            continue
        url = asset["browser_download_url"]
        digest = asset.get("digest") or ""
        if url.lower() != expected.lower() or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise RuntimeError("адрес или контрольная сумма пакета не подтверждены GitHub")
        return url, digest.split(":", 1)[1]
    raise RuntimeError(f"для {arch} пакета пока нет")


def stage_binary(url, digest, directory):
    archive = directory / "package.tar.gz"
    hasher = hashlib.sha256()
    size = 0
    with urllib.request.urlopen(url, timeout=30) as response, archive.open("wb") as output:
        dest = urllib.parse.urlsplit(response.geturl())
        if dest.scheme != "https" or dest.hostname not in {
            "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"
        }:
            raise RuntimeError("GitHub направил загрузку пакета на неожиданный адрес")
        for chunk in iter(lambda: response.read(1 << 20), b""):
            size += len(chunk)
            if size > 128 << 20:
                raise RuntimeError("пакет слишком велик")
            hasher.update(chunk)
            output.write(chunk)
    if hasher.hexdigest() != digest:
        raise RuntimeError("контрольная сумма не совпала")
    with tarfile.open(archive, "r:gz") as package:
        member = package.next()
        if not member or member.name != "sema" or not member.isfile() or not 0 < member.size <= 128 << 20:
            raise RuntimeError("неверная структура пакета")
        with package.extractfile(member) as source, (directory / "sema").open("wb") as output:
            shutil.copyfileobj(source, output)
        if package.next() is not None:
            raise RuntimeError("в архиве есть лишние файлы")
    (directory / "sema").chmod(0o755)


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"Docker не выполнил {args[0]}")
    return result.stdout.strip()


def inspect(container):
    return json.loads(docker("container", "inspect", container))[0]


def health(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        data = json.loads(response.read(4096))
    if data.get("ok") is not True or not isinstance(data.get("version"), str):
        raise RuntimeError("сервер не прошёл проверку здоровья")
    return data["version"]


def wait_health(url, version, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if health(url) == version:
                return True
        except (OSError, ValueError, RuntimeError):
            pass
        time.sleep(1)
    return False


def mounts(container):
    return sorted((m["Type"], m["Source"], m["Destination"], m["RW"]) for m in container["Mounts"])


def make_candidate(previous, image, name, repository, hook_key):
    config = previous["Config"].copy()
    config["Image"] = image
    if config.get("Hostname") == previous["Id"][:12]:
        config["Hostname"] = ""
    if hook_key:
        env = {line.split("=", 1)[0]: line for line in config["Env"]}
        env["SEMA_RELEASE_REPOSITORY"] = "SEMA_RELEASE_REPOSITORY=" + repository
        env["SEMA_RELEASE_WEBHOOK_SECRET"] = "SEMA_RELEASE_WEBHOOK_SECRET=" + hook_key
        config["Env"] = list(env.values())
    config["HostConfig"] = previous["HostConfig"]
    endpoints = {}
    for network, details in previous["NetworkSettings"]["Networks"].items():
        if network in ("host", "none"):
            continue
        endpoints[network] = {key: details[key] for key in
                              ("IPAMConfig", "Links", "Aliases", "DriverOpts", "GwPriority") if details.get(key)}
    if endpoints:
        config["NetworkingConfig"] = {"EndpointsConfig": endpoints}
    req = subprocess.run(
        ["curl", "-fsS", "--max-time", "20", "--unix-socket", "/var/run/docker.sock",
         "-H", "Content-Type: application/json", "--data-binary", "@-",
         "http://localhost/containers/create?name=" + name],
        input=json.dumps(config), text=True, capture_output=True,
    )
    if req.returncode:
        raise RuntimeError("Docker не создал контейнер-кандидат")
    candidate = json.loads(req.stdout)["Id"]
    staged = inspect(candidate)
    if mounts(previous) != mounts(staged):
        raise RuntimeError("тома контейнера изменились")
    for key in ("PortBindings", "RestartPolicy", "NetworkMode", "Binds", "Mounts"):
        if previous["HostConfig"].get(key) != staged["HostConfig"].get(key):
            raise RuntimeError(f"изменились параметры контейнера: {key}")
    if sorted(config["Env"]) != sorted(staged["Config"]["Env"]):
        raise RuntimeError("изменилось окружение контейнера")
    return candidate


def switch(previous, image, container, current, version, url, repository, hook_key):
    old_id = previous["Id"]
    old_name = f"{container}-before-{old_id[:12]}"
    candidate_name = f"{container}-candidate-{version[6:18]}-{os.getpid()}"
    candidate = make_candidate(previous, image, candidate_name, repository, hook_key)
    if inspect(container)["Id"] != old_id or health(url) != current:
        raise RuntimeError("рабочий контейнер изменился во время подготовки")
    try:
        docker("stop", "--time", "150", old_id)
        docker("rename", old_id, old_name)
        docker("rename", candidate, container)
        docker("start", candidate)
        if not wait_health(url, version):
            raise RuntimeError("новая сборка не ответила")
        time.sleep(3)
        state = inspect(candidate)
        if not state["State"]["Running"] or state["RestartCount"] != 0 or health(url) != version:
            raise RuntimeError("новая сборка не удержалась в рабочем состоянии")
    except BaseException:
        if inspect(candidate)["State"]["Running"]:
            docker("stop", "--time", "150", candidate)
        if inspect(candidate)["Name"] == "/" + container:
            docker("rename", candidate, candidate_name)
        if inspect(old_id)["Name"] != "/" + container:
            docker("rename", old_id, container)
        if not inspect(old_id)["State"]["Running"]:
            docker("start", old_id)
        if not wait_health(url, current):
            raise RuntimeError("прежний контейнер восстановлен, но не прошёл проверку здоровья")
        raise RuntimeError("выкладка не прошла, прежняя версия восстановлена")
    print(f"Установлена {version}. Предыдущий контейнер сохранён как {old_name}.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", default="PavluninVladimir/sema")
    parser.add_argument("--container", default="sema")
    parser.add_argument("--health-url", default="http://127.0.0.1:8765/api/health")
    parser.add_argument("--webhook-secret-file", type=Path)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not REPOSITORY.fullmatch(args.repository) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", args.container):
        parser.error("проверьте имя репозитория и контейнера")
    arch = ARCHES.get(platform.machine())
    if not arch:
        parser.error("эта архитектура пока не поддерживается")
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "sema-updater"
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (cache / f"{args.container}.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        release = fetch_json(f"https://api.github.com/repos/{args.repository}/releases/latest")
        url, digest = asset_for(release, args.repository, arch)
        version = release["tag_name"]
        current = health(args.health_url)
        if current == version:
            print(f"Уже установлена {version}.")
            return
        if not args.apply:
            print(f"Доступна {version}, сейчас {current}.")
            return
        hook_key = args.webhook_secret_file.read_text().strip() if args.webhook_secret_file else ""
        if args.webhook_secret_file and len(hook_key) < 32:
            parser.error("ключ извещений должен быть не короче 32 знаков")
        previous = inspect(args.container)
        if not previous["State"]["Running"]:
            raise RuntimeError("рабочий контейнер не запущен")
        with tempfile.TemporaryDirectory(prefix="sema-release-") as temp:
            directory = Path(temp)
            stage_binary(url, digest, directory)
            base = f"sema-update:base-{previous['Id'][:12]}"
            image = f"sema-update:{version}"
            docker("image", "tag", previous["Image"], base)
            (directory / "Dockerfile").write_text(f"FROM {base}\nCOPY --chmod=0755 sema /usr/local/bin/sema\n")
            docker("build", "--network", "none", "--pull=false", "-t", image, str(directory))
            switch(previous, image, args.container, current, version, args.health_url, args.repository, hook_key)


if __name__ == "__main__":
    try:
        main()
    except (OSError, KeyError, ValueError, RuntimeError) as error:
        raise SystemExit(f"Обновление: {error}") from None
