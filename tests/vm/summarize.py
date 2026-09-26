#!/usr/bin/env python3
"""Summarize Go JSON output without confusing skipped tests with coverage."""
import json
import sys
from collections import Counter
counts = Counter()
skipped = []
failed = []
with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        event = json.loads(line)
        if "Test" not in event:
            continue
        action = event.get("Action")
        if action in ("pass", "skip", "fail"):
            counts[action] += 1
        name = event["Package"] + "/" + event["Test"]
        if action == "skip":
            skipped.append(name)
        elif action == "fail":
            failed.append(name)
print(json.dumps({"counts": dict(counts), "skipped": skipped, "failed": failed,
                  "release_approved": False}, indent=2))
