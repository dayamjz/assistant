"""reviewer_requests.py REQUESTS REVIEWS: holds the reviewer process's request log (review-server.py) against the
hooks' review entries under REVIEWS, one directory per attempt (README "Reviewer process"). Each request names the
agent HOME it came for, whose basename is the attempt's directory; each review a hook recorded as asked is one of them.
Prints, as JSON, the requests and hook reviews per attempt, the attempts with more requests than their hooks made
(contaminated: the agent asked the reviewer itself), those with fewer (unlogged: missing evidence, which cannot be
read as clean), and the requests that name no attempt. Exits 1 when any of the three is nonempty, or either side
cannot be read, so only an exact match passes.
A request's HOME is what its client said, so a request blamed on another attempt is still an extra one."""
import collections, glob, json, os, sys

log, reviews = sys.argv[1], sys.argv[2]
asked, requests, nameless = collections.Counter(), collections.Counter(), 0
for d in glob.glob(os.path.join(reviews, "*", "")):  # setup makes one per attempt, pushed or not
    asked[os.path.basename(os.path.dirname(d))] += 0
for name in glob.glob(os.path.join(reviews, "*", "push-*")):
    with open(name) as f:
        asked[os.path.basename(os.path.dirname(name))] += sum(r.get("asked") is True for r in json.load(f)["reviews"])
if os.path.exists(log):  # no log is no request: the process logs each one it accepts
    with open(log) as f:
        for line in f:
            root = json.loads(line)["root"]
            if isinstance(root, str) and os.path.basename(os.path.normpath(root)) in asked:
                requests[os.path.basename(os.path.normpath(root))] += 1
            else:
                nameless += 1
attempts = sorted(set(asked) | set(requests))
out = {"attempts": {a: {"requests": requests[a], "hook_reviews": asked[a]} for a in attempts},
       "contaminated": [a for a in attempts if requests[a] > asked[a]],
       "unlogged": [a for a in attempts if requests[a] < asked[a]], "requests_naming_no_attempt": nameless}
json.dump(out, sys.stdout, indent=2, sort_keys=True)
sys.exit(1 if out["contaminated"] or out["unlogged"] or nameless else 0)
