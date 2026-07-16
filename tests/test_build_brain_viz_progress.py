"""Tests for the --progress-file sidecar in scripts/build-brain-viz.py.

Covers the new progress-emission surface only (OB plan-2 phase-2): the
atomic-write helper's schema and atomicity, the labeling-loop pct mapping
(50-95 band), and that omitting --progress-file is a true no-op. The rest of
the pipeline (Postgres load, UMAP, HDBSCAN, Ollama) is out of scope for this
suite; per [[tdd]] "Scope", scripts earn a smoke test plus documented failure
paths, not full per-function TDD cycling.
"""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path
from typing import Any

import pytest

_SCRIPT_PATH = Path(__file__).resolve().parent.parent / "scripts" / "build-brain-viz.py"


def _load_module() -> Any:
    """Import build-brain-viz.py by path (its filename is not a valid module name)."""
    spec = importlib.util.spec_from_file_location("build_brain_viz", _SCRIPT_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules["build_brain_viz"] = module
    spec.loader.exec_module(module)
    return module


viz = _load_module()


class TestWriteProgressNoOp:
    """progress_path=None must be a true no-op: no directory touched, no file created."""

    def test_none_path_writes_nothing(self, tmp_path: Path) -> None:
        # Point cwd-independent: pass a directory we can inspect and assert it stays empty.
        before = list(tmp_path.iterdir())
        viz._write_progress(None, "loading", 5)
        after = list(tmp_path.iterdir())
        assert before == after == []

    def test_none_path_never_raises(self) -> None:
        # A no-op path must be safe to call from every phase boundary unconditionally.
        viz._write_progress(None, "done", 100, clusters_done=3, clusters_total=3)


class TestWriteProgressSchemaAndAtomicity:
    """The written JSON must match the shared contract exactly and be atomic."""

    def test_writes_exact_schema_fields(self, tmp_path: Path) -> None:
        target = tmp_path / "status.json"
        viz._write_progress(target, "labeling", 62, clusters_done=5, clusters_total=9)

        payload = json.loads(target.read_text(encoding="utf-8"))
        assert set(payload.keys()) == {
            "pct",
            "phase",
            "clusters_done",
            "clusters_total",
            "ts",
        }
        assert payload["pct"] == 62
        assert payload["phase"] == "labeling"
        assert payload["clusters_done"] == 5
        assert payload["clusters_total"] == 9
        assert isinstance(payload["ts"], (int, float))
        assert payload["ts"] > 0

    def test_coarse_phase_defaults_zero_cluster_counts(self, tmp_path: Path) -> None:
        target = tmp_path / "status.json"
        viz._write_progress(target, "projecting", 15)
        payload = json.loads(target.read_text(encoding="utf-8"))
        assert payload["clusters_done"] == 0
        assert payload["clusters_total"] == 0

    def test_write_is_atomic_no_stray_temp_files(self, tmp_path: Path) -> None:
        target = tmp_path / "status.json"
        viz._write_progress(target, "loading", 5)
        viz._write_progress(target, "writing", 95, clusters_done=4, clusters_total=4)

        entries = list(tmp_path.iterdir())
        # Only the final target file exists; no leaked .tmp file from mkstemp.
        assert entries == [target]
        payload = json.loads(target.read_text(encoding="utf-8"))
        assert payload["phase"] == "writing"
        assert payload["pct"] == 95

    def test_creates_missing_parent_directory(self, tmp_path: Path) -> None:
        target = tmp_path / "nested" / "dir" / "status.json"
        viz._write_progress(target, "loading", 5)
        assert target.exists()
        payload = json.loads(target.read_text(encoding="utf-8"))
        assert payload["phase"] == "loading"

    def test_write_failure_is_swallowed_not_raised(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        # Point the parent at a path that can never be a valid directory (a file,
        # not a dir), so mkdir/mkstemp fail. Progress writes are best-effort:
        # this must log a warning and return, never raise, per python.md error
        # handling (surfaced, not silently swallowed).
        blocker = tmp_path / "not_a_dir"
        blocker.write_text("x", encoding="utf-8")
        target = blocker / "status.json"

        viz._write_progress(target, "loading", 5)  # must not raise

        captured = capsys.readouterr()
        assert "failed to write progress file" in captured.err


class TestLabelClustersProgressBand:
    """The labeling loop must map clusters_done/clusters_total onto the 50-95 band."""

    def _thoughts_and_labels(
        self, n_clusters: int, per_cluster: int = 2
    ) -> tuple[list[dict], list, list[int]]:
        import numpy as np

        thoughts = []
        labels = []
        coords = []
        for cid in range(n_clusters):
            for _ in range(per_cluster):
                thoughts.append(
                    {
                        "id": f"t-{cid}-{_}",
                        "content": "some content about a topic",
                        "summary": "",
                        "thought_type": "note",
                        "tags": ["alpha"],
                    }
                )
                labels.append(cid)
                coords.append([float(cid), float(cid)])
        return thoughts, np.array(coords), labels

    def test_pct_climbs_from_50_to_95_across_clusters(self, tmp_path: Path) -> None:
        thoughts, coords, labels = self._thoughts_and_labels(n_clusters=4)
        progress_path = tmp_path / "status.json"

        seen: list[dict] = []
        orig_write = viz._write_progress

        def _spy(path, phase, pct, clusters_done=0, clusters_total=0):  # type: ignore[no-untyped-def]
            orig_write(path, phase, pct, clusters_done, clusters_total)
            if path is not None:
                seen.append(
                    {
                        "phase": phase,
                        "pct": pct,
                        "clusters_done": clusters_done,
                        "clusters_total": clusters_total,
                    }
                )

        viz._write_progress = _spy  # type: ignore[assignment]
        try:
            clusters_out, llm_attempts, llm_fallbacks = viz.label_clusters(
                thoughts,
                coords,
                labels,
                cfg={},
                ollama_model="unused",
                no_llm=True,
                progress_path=progress_path,
            )
        finally:
            viz._write_progress = orig_write  # type: ignore[assignment]

        assert llm_attempts == 0  # --no-llm path takes no Ollama calls
        assert len(clusters_out) == 4

        # First write establishes the total before any cluster is done.
        assert seen[0] == {
            "phase": "labeling",
            "pct": 50,
            "clusters_done": 0,
            "clusters_total": 4,
        }

        # One write per cluster after; pct is monotonically non-decreasing and
        # stays within the granular 50-95 band per plan.md's progress protocol.
        per_cluster_writes = seen[1:]
        assert len(per_cluster_writes) == 4
        for i, entry in enumerate(per_cluster_writes, start=1):
            assert entry["phase"] == "labeling"
            assert entry["clusters_done"] == i
            assert entry["clusters_total"] == 4
            assert 50 <= entry["pct"] <= 95

        # Final cluster write reaches the top of the band.
        assert per_cluster_writes[-1]["pct"] == 95
        # Monotonic non-decreasing across the whole sequence.
        pcts = [e["pct"] for e in seen]
        assert pcts == sorted(pcts)

    def test_no_progress_path_writes_nothing(self, tmp_path: Path) -> None:
        thoughts, coords, labels = self._thoughts_and_labels(n_clusters=2)
        before = list(tmp_path.iterdir())

        clusters_out, _, _ = viz.label_clusters(
            thoughts,
            coords,
            labels,
            cfg={},
            ollama_model="unused",
            no_llm=True,
            progress_path=None,
        )

        assert len(clusters_out) == 2
        assert list(tmp_path.iterdir()) == before == []


class TestProgressFileFlagIsOptIn:
    """CLI-level: the flag defaults to None (no-op) and is otherwise pass-through."""

    def test_default_output_and_progress_file_parses(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setattr(sys, "argv", ["build-brain-viz.py"])
        args = viz.parse_args(tmp_path / "brain.json")
        assert args.progress_file is None

    def test_progress_file_flag_is_threaded_through(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        status_path = tmp_path / "status.json"
        monkeypatch.setattr(
            sys,
            "argv",
            ["build-brain-viz.py", "--progress-file", str(status_path)],
        )
        args = viz.parse_args(tmp_path / "brain.json")
        assert args.progress_file == str(status_path)
