#!/usr/bin/env python3
"""Известить Сёму о готовом выпуске авторизованным запросом из Actions."""

import hashlib
import hmac
import json
import os
import time
import urllib.parse
import urllib.request


def main():
    url = os.environ.get("SEMA_RELEASE_WEBHOOK_URL", "")
    key = os.environ.get("SEMA_RELEASE_WEBHOOK_SECRET", "")
    if not url and not key:
        print("Получатель извещений пока не задан.")
        return
    if urllib.parse.urlsplit(url).scheme != "https" or len(key) < 32:
        raise SystemExit("Для извещения нужны HTTPS-адрес и секрет длиной от 32 знаков.")
    payload = json.dumps({
        "version": os.environ["VERSION"],
        "repository": os.environ["GITHUB_REPOSITORY"],
        "sent": int(time.time()),
    }, separators=(",", ":")).encode()
    signature = hmac.new(key.encode(), payload, hashlib.sha256).hexdigest()
    request = urllib.request.Request(url, data=payload, headers={
        "Content-Type": "application/json",
        "X-Hub-Signature-256": "sha256=" + signature,
    })
    with urllib.request.urlopen(request, timeout=15) as response:
        if response.status != 204:
            raise SystemExit("Сёма не подтвердила извещение.")
    print("Сёма подтвердила получение извещения.")


if __name__ == "__main__":
    main()
