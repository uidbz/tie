#!/usr/bin/env python3
"""
Deduplicate path UIDs in a tie DB TSV dump.

For each virtual path that maps to N>1 UIDs, one canonical UID is chosen
(highest child-count, then highest triple-count as tiebreaker).  All
parent=<non-canonical> triples are rewritten to parent=<canonical>, and
every triple whose subject is a non-canonical UID is removed.

Usage:
    python3 dedup_tie_paths.py input.tsv output.tsv
"""

import sys
import collections

def main():
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} input.tsv output.tsv", file=sys.stderr)
        sys.exit(1)

    infile, outfile = sys.argv[1], sys.argv[2]

    triples = []
    with open(infile, encoding="utf-8") as f:
        for line in f:
            parts = line.rstrip("\n").split("\t")
            if len(parts) == 3:
                triples.append(tuple(parts))
            else:
                # preserve malformed lines as-is (shouldn't happen)
                triples.append((line.rstrip("\n"),))

    # Build: path -> [uid, ...]
    path_uids: dict[str, list[str]] = collections.defaultdict(list)
    for subj, rel, val in (t for t in triples if len(t) == 3):
        if rel == "path":
            path_uids[val].append(subj)

    # For each duplicate path, pick a canonical UID.
    # Score = (child_count, triple_count) — higher is better.
    # child_count = number of triples (*, "parent", uid) where subject != uid
    # triple_count = number of triples with subject = uid

    child_count: dict[str, int] = collections.Counter()
    triple_count: dict[str, int] = collections.Counter()
    for subj, rel, val in (t for t in triples if len(t) == 3):
        triple_count[subj] += 1
        if rel == "parent" and subj != val:
            child_count[val] += 1

    # canonical[uid] = uid  (maps every uid to its canonical replacement)
    canonical: dict[str, str] = {}
    removed_uids: set[str] = set()

    for path, uids in path_uids.items():
        if len(uids) <= 1:
            for u in uids:
                canonical[u] = u
            continue

        scored = sorted(
            uids,
            key=lambda u: (child_count[u], triple_count[u]),
            reverse=True,
        )
        winner = scored[0]
        losers = scored[1:]
        print(
            f"[dedup] path={path!r}  keeping={winner[:16]}…  "
            f"removing {len(losers)}: {[u[:16]+'…' for u in losers]}",
            file=sys.stderr,
        )
        canonical[winner] = winner
        for u in losers:
            canonical[u] = winner
            removed_uids.add(u)

    # Rewrite triples:
    #   - drop triples whose subject is a removed UID
    #   - rewrite any value that is a removed UID to its canonical
    out_triples = []
    for t in triples:
        if len(t) != 3:
            out_triples.append(t)
            continue
        subj, rel, val = t
        if subj in removed_uids:
            continue  # drop this triple entirely
        # rewrite value if it was a non-canonical UID
        new_val = canonical.get(val, val)
        out_triples.append((subj, rel, new_val))

    with open(outfile, "w", encoding="utf-8") as f:
        for t in out_triples:
            f.write("\t".join(t) + "\n")

    print(
        f"[dedup] {len(triples)} triples in, {len(out_triples)} out, "
        f"{len(triples)-len(out_triples)} removed ({len(removed_uids)} UIDs purged)",
        file=sys.stderr,
    )

if __name__ == "__main__":
    main()
