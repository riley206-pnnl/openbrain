#!/usr/bin/env python3
"""
build-brain-viz.py — OpenBrain thought visualizer builder.

Reads all thoughts + embeddings from Postgres, projects to 2D via UMAP,
clusters with HDBSCAN, generates LLM cluster labels via Ollama, and emits
brain.json into cmd/openbrain-web/static/ so the Go web server can serve it.

Usage:
    python3 scripts/build-brain-viz.py

Options:
    --output PATH       Write JSON to PATH (default: cmd/openbrain-web/static/brain.json)
    --standalone FILE   Also write a self-contained HTML file (for offline use)
    --kmeans K          Use k-means with K clusters instead of HDBSCAN
    --no-llm            Skip LLM labeling; use heuristic labels only
    --min-cluster N     HDBSCAN min_cluster_size (default: 8)
    --max-edges N       Max similarity edges (default: 2000)
    --sim-thresh F      Min cosine similarity for an edge (default: 0.55)

Requirements (install in a venv):
    pip install psycopg[binary] pgvector numpy umap-learn hdbscan scikit-learn python-dotenv requests
"""

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import requests
from dotenv import dotenv_values


def _import_umap():
    try:
        from umap import UMAP
        return UMAP
    except ImportError:
        print("  [warn] umap-learn not installed; falling back to t-SNE")
        return None


def _import_hdbscan():
    try:
        import hdbscan as hdbscan_mod
        return hdbscan_mod.HDBSCAN
    except ImportError:
        print("  [warn] hdbscan not installed; will use k-means")
        return None


def load_thoughts(cfg):
    import psycopg
    from pgvector.psycopg import register_vector

    host = cfg.get("OPENBRAIN_DB_HOST", "localhost")
    port = cfg.get("OPENBRAIN_DB_PORT", "5432")
    dbname = cfg.get("OPENBRAIN_DB_NAME", "openbrain")
    user = cfg.get("OPENBRAIN_DB_USER", "openbrain")
    password = cfg.get("OPENBRAIN_DB_PASSWORD", "openbrain")

    if host.startswith("/"):
        conn_str = f"host={host} port={port} dbname={dbname} user={user} password={password}"
    else:
        conn_str = f"host={host} port={port} dbname={dbname} user={user} password={password} sslmode=disable"

    print(f"  Connecting to Postgres ({dbname} @ {host})…")
    with psycopg.connect(conn_str) as conn:
        register_vector(conn)
        rows = conn.execute(
            """
            SELECT id::text, content, summary, thought_type,
                   tags, created_at::text, embedding
            FROM thoughts
            WHERE is_current = TRUE AND embedding IS NOT NULL
            ORDER BY created_at
            """,
        ).fetchall()

    thoughts = []
    for row in rows:
        id_, content, summary, ttype, tags, created_at, embedding = row
        thoughts.append({
            "id": id_,
            "content": content or "",
            "summary": summary or "",
            "thought_type": ttype or "note",
            "tags": list(tags) if tags else [],
            "created_at": created_at or "",
            "embedding": embedding.to_numpy().astype(np.float32),
        })

    print(f"  Loaded {len(thoughts)} thoughts with embeddings")
    return thoughts


def project_2d(embeddings, use_umap=True):
    if use_umap:
        UMAP = _import_umap()
        if UMAP is not None:
            print("  Running UMAP (cosine, n_neighbors=15, min_dist=0.1)…")
            reducer = UMAP(n_components=2, metric="cosine",
                           n_neighbors=15, min_dist=0.1,
                           random_state=42, verbose=False)
            return reducer.fit_transform(embeddings)

    from sklearn.manifold import TSNE
    print("  Running t-SNE (cosine, perplexity=30)…")
    tsne = TSNE(n_components=2, metric="cosine", perplexity=30,
                n_iter=1000, random_state=42)
    return tsne.fit_transform(embeddings)


def cluster_hdbscan(embeddings, min_cluster_size=8):
    HDBSCAN = _import_hdbscan()
    if HDBSCAN is None:
        return None
    print(f"  Running HDBSCAN (min_cluster_size={min_cluster_size})…")
    clusterer = HDBSCAN(min_cluster_size=min_cluster_size, metric="euclidean",
                        cluster_selection_epsilon=0.0)
    norms = np.linalg.norm(embeddings, axis=1, keepdims=True)
    norms = np.where(norms == 0, 1, norms)
    labels = clusterer.fit_predict(embeddings / norms)
    n_clusters = len(set(labels)) - (1 if -1 in labels else 0)
    print(f"  HDBSCAN: {n_clusters} clusters, {(labels == -1).sum()} noise points")
    return labels


def cluster_kmeans(embeddings, k=8):
    from sklearn.cluster import KMeans
    print(f"  Running k-means (k={k})…")
    labels = KMeans(n_clusters=k, random_state=42, n_init=10).fit_predict(embeddings)
    print(f"  k-means: {k} clusters")
    return labels


def compute_edges(embeddings, sim_thresh=0.55, max_edges=2000, top_k=5):
    print(f"  Computing cosine similarity edges (thresh={sim_thresh}, top_k={top_k})…")
    norms = np.linalg.norm(embeddings, axis=1, keepdims=True)
    norms = np.where(norms == 0, 1, norms)
    normed = (embeddings / norms).astype(np.float32)

    edges = []
    seen = set()
    N = len(normed)
    chunk = 200
    for start in range(0, N, chunk):
        batch = normed[start:start + chunk]
        sims = batch @ normed.T
        for bi, row in enumerate(sims):
            i = start + bi
            row[i] = -1
            for j in np.argsort(row)[::-1][:top_k]:
                w = float(row[j])
                if w < sim_thresh:
                    break
                key = (min(i, int(j)), max(i, int(j)))
                if key not in seen:
                    seen.add(key)
                    edges.append({"s": key[0], "t": key[1], "w": round(w, 3)})

    edges.sort(key=lambda e: -e["w"])
    edges = edges[:max_edges]
    print(f"  Edges kept: {len(edges)}")
    return edges


def heuristic_label(cluster_thoughts):
    from collections import Counter
    tag_counts, type_counts = Counter(), Counter()
    for t in cluster_thoughts:
        for tag in t["tags"]:
            tag_counts[tag.lower()] += 1
        type_counts[t["thought_type"]] += 1
    top_tags = [tag for tag, _ in tag_counts.most_common(2)]
    if top_tags:
        return ", ".join(top_tags).title()
    dominant = type_counts.most_common(1)[0][0] if type_counts else "note"
    return dominant.replace("_", " ").title()


def llm_label(cluster_thoughts, cfg, model):
    sample = sorted(cluster_thoughts,
                    key=lambda t: (len(t["summary"]) > 0, len(t["content"])),
                    reverse=True)[:8]
    snippets = [f"- {t['summary'].strip() or t['content'][:200].strip()}" for t in sample]
    prompt = (
        "You are helping label regions of a personal knowledge brain map. "
        "Below are related notes from one cluster. "
        "Give a SHORT 2-4 word label naming the theme of this region, "
        "like a brain lobe name. Examples: 'Work Decisions', 'People & Meetings', "
        "'Technical Ideas', 'Life Goals'. Return ONLY the label, nothing else.\n\n"
        + "\n".join(snippets)
    )
    base_url = cfg.get("OPENBRAIN_OLLAMA_BASE_URL", "http://localhost:11434")
    try:
        resp = requests.post(
            f"{base_url}/api/generate",
            json={"model": model, "prompt": prompt, "stream": False},
            timeout=30,
        )
        resp.raise_for_status()
        label = resp.json().get("response", "").strip().strip('"\'').strip()
        if 2 <= len(label) <= 60 and "\n" not in label:
            return label
    except Exception as exc:
        print(f"    [warn] LLM call failed: {exc}")
    return None


# ── standalone HTML template (--standalone only) ───────────────────────────────
_STANDALONE_TEMPLATE = """\
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1.0"/>
<title>OpenBrain — Thought Map (standalone)</title>
</head>
<body>
<script>window.__STANDALONE__ = true; const DATA = __DATA_JSON__;</script>
<script src="https://cdn.jsdelivr.net/npm/d3@7/dist/d3.min.js"></script>
<script>
// minimal shim so graph.html renderer works standalone
document.addEventListener('DOMContentLoaded', () => { window.__brain_data = DATA; });
</script>
<p style="font:14px system-ui;padding:20px;color:#aaa">
  This standalone file requires the renderer from graph.html.<br>
  For the full experience run <code>make viz</code> and use the web UI.
</p>
</body>
</html>
"""


def build_data(thoughts, coords_2d, labels, edges, clusters_out, cfg):
    xs, ys = coords_2d[:, 0], coords_2d[:, 1]
    margin = 0.05
    xr, yr = xs.max() - xs.min(), ys.max() - ys.min()
    thought_types = sorted(set(t["thought_type"] for t in thoughts))

    nodes_out = []
    for i, t in enumerate(thoughts):
        preview = t["summary"].strip() or t["content"][:300].strip()
        nodes_out.append({
            "id": t["id"],
            "x": round(float(coords_2d[i, 0]), 4),
            "y": round(float(coords_2d[i, 1]), 4),
            "cluster": labels[i],
            "type": t["thought_type"],
            "tags": t["tags"][:5],
            "summary": t["summary"][:200],
            "content": preview[:300],
            "created_at": t["created_at"],
        })

    return {
        "nodes": nodes_out,
        "edges": edges,
        "clusters": clusters_out,
        "meta": {
            "thought_types": thought_types,
            "bounds": [
                round(float(xs.min() - xr * margin), 4),
                round(float(xs.max() + xr * margin), 4),
                round(float(ys.min() - yr * margin), 4),
                round(float(ys.max() + yr * margin), 4),
            ],
            "n_thoughts": len(thoughts),
            "n_clusters": len([c for c in clusters_out if c["id"] >= 0]),
            "n_edges": len(edges),
            "embedding_model": cfg.get("OPENBRAIN_EMBEDDING_MODEL", "unknown"),
        },
    }


def main():
    # Default output: repo-relative path to the embedded static dir
    script_dir = Path(__file__).parent
    repo_root = script_dir.parent
    default_output = repo_root / "cmd" / "openbrain-web" / "static" / "brain.json"

    parser = argparse.ArgumentParser(description="Build OpenBrain brain visualizer data")
    parser.add_argument("--output", default=str(default_output), metavar="PATH",
                        help=f"Write JSON to PATH (default: {default_output})")
    parser.add_argument("--standalone", metavar="FILE",
                        help="Also write a self-contained HTML to FILE (offline use)")
    parser.add_argument("--kmeans", type=int, default=0, metavar="K")
    parser.add_argument("--no-llm", action="store_true")
    parser.add_argument("--min-cluster", type=int, default=8, metavar="N")
    parser.add_argument("--max-edges", type=int, default=2000, metavar="N")
    parser.add_argument("--sim-thresh", type=float, default=0.55, metavar="F")
    args = parser.parse_args()

    # Find .env by walking up from script location
    env_path = None
    for candidate in [script_dir, repo_root]:
        p = candidate / ".env"
        if p.exists():
            env_path = p
            break

    cfg = {}
    if env_path:
        print(f"  Loading .env from {env_path}")
        cfg = dotenv_values(env_path)
    else:
        print("  [warn] .env not found; using default DB settings")

    print("\n[1/6] Loading thoughts from database…")
    thoughts = load_thoughts(cfg)
    if not thoughts:
        print("ERROR: no thoughts found")
        sys.exit(1)

    embeddings_arr = np.stack([t["embedding"] for t in thoughts])

    print("\n[2/6] Projecting embeddings to 2D…")
    coords_2d = project_2d(embeddings_arr, use_umap=(args.kmeans == 0))

    print("\n[3/6] Clustering…")
    if args.kmeans > 0:
        labels = cluster_kmeans(embeddings_arr, k=args.kmeans)
    else:
        labels = cluster_hdbscan(embeddings_arr, min_cluster_size=args.min_cluster)
        if labels is None:
            print("  [fallback] using k-means k=8")
            labels = cluster_kmeans(embeddings_arr, k=8)

    unique_non_noise = sorted(set(labels) - {-1})
    id_map = {old: new for new, old in enumerate(unique_non_noise)}
    id_map[-1] = -1
    labels = [id_map[l] for l in labels]

    print("\n[4/6] Computing similarity edges…")
    edges = compute_edges(embeddings_arr, sim_thresh=args.sim_thresh,
                          max_edges=args.max_edges)

    print("\n[5/6] Generating cluster labels…")
    ollama_model = (cfg.get("OPENBRAIN_EXTRACT_MODEL_FAST") or
                    cfg.get("OPENBRAIN_EXTRACT_MODEL") or
                    cfg.get("OPENBRAIN_CHAT_MODEL"))
    if not ollama_model:
        raise SystemExit("error: no LLM model configured — set OPENBRAIN_EXTRACT_MODEL_FAST, OPENBRAIN_EXTRACT_MODEL, or OPENBRAIN_CHAT_MODEL in .env")

    clusters_out = []
    for cid in sorted(set(labels)):
        members = [thoughts[i] for i, l in enumerate(labels) if l == cid]
        member_coords = [coords_2d[i] for i, l in enumerate(labels) if l == cid]
        heuristic = heuristic_label(members)

        if cid < 0:
            label = "Unclustered"
        elif args.no_llm:
            label = heuristic
            print(f"  Cluster {cid:3d} ({len(members):3d} thoughts) → {label}  [heuristic]")
        else:
            print(f"  Cluster {cid:3d} ({len(members):3d} thoughts) → asking LLM…", end="", flush=True)
            llm = llm_label(members, cfg, ollama_model)
            if llm:
                label = llm
                print(f' ✓  "{label}"')
            else:
                label = heuristic
                print(f' fallback → "{label}"')

        arr = np.array(member_coords)
        clusters_out.append({
            "id": cid,
            "label": label,
            "heuristic": heuristic,
            "size": len(members),
            "cx": round(float(arr[:, 0].mean()), 4),
            "cy": round(float(arr[:, 1].mean()), 4),
        })

    print("\n[6/6] Writing output…")
    data = build_data(thoughts, coords_2d, labels, edges, clusters_out, cfg)
    data_json = json.dumps(data, ensure_ascii=False)

    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(data_json, encoding="utf-8")
    size_kb = output_path.stat().st_size / 1024
    print(f"\n✅  brain.json written → {output_path} ({size_kb:.0f} KB)")
    print(f"   {data['meta']['n_thoughts']} nodes · {data['meta']['n_clusters']} clusters · {data['meta']['n_edges']} edges")

    if args.standalone:
        standalone_path = Path(args.standalone)
        html = _STANDALONE_TEMPLATE.replace("__DATA_JSON__", data_json)
        standalone_path.write_text(html, encoding="utf-8")
        print(f"   standalone HTML → {standalone_path}")

    print(f"\n   Rebuild anytime:  make viz")
    print(f"   Then open:        http://127.0.0.1:10203/graph")


if __name__ == "__main__":
    main()
