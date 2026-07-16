"""Tests for scripts/build-brain-viz.py.

Covers two surfaces:

1. The --progress-file sidecar (OB plan-2 phase-2): the atomic-write
   helper's schema and atomicity, the labeling-loop pct mapping (50-95
   band), and that omitting --progress-file is a true no-op.
2. The LLM cluster-label parsing and model-resolution surface (the
   viz-clustering fix): _parse_llm_label's list-marker stripping (with the
   digit-leading-label preservation regression coverage) and
   resolve_ollama_model's env-var precedence.

The rest of the pipeline (Postgres load, UMAP, HDBSCAN) is out of scope for
this suite; per [[tdd]] "Scope", scripts earn a smoke test plus documented
failure paths, not full per-function TDD cycling.
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

    def test_empty_labels_returns_empty_result_without_raising(
        self, tmp_path: Path
    ) -> None:
        """The real HDBSCAN-found-no-clusters case: all points noise, or fewer
        thoughts than min_cluster_size, so `labels` (and `thoughts`) are empty.

        Must not raise (no divide-by-zero from clusters_total == 0) and must
        emit only the single pre-loop "labeling" write: no per-cluster writes,
        since the loop body never runs.
        """
        import numpy as np

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
                [],
                np.empty((0, 2)),
                [],
                cfg={},
                ollama_model="unused",
                no_llm=True,
                progress_path=progress_path,
            )
        finally:
            viz._write_progress = orig_write  # type: ignore[assignment]

        assert (clusters_out, llm_attempts, llm_fallbacks) == ([], 0, 0)
        assert seen == [
            {"phase": "labeling", "pct": 50, "clusters_done": 0, "clusters_total": 0}
        ]

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


class TestParseLlmLabel:
    """_parse_llm_label: extract a usable label from a chatty LLM response.

    The digit-leading cases are a regression suite for the CRITICAL bug
    where a numbered-list regex treated bare leading digits as a list
    marker and silently mangled real labels (see git history on this
    branch). Bare digits are preserved unless followed by a "." or ")"
    list-marker delimiter.
    """

    @pytest.mark.parametrize(
        ("raw", "expected"),
        [
            # Single clean line: no marker to strip.
            ("Work Decisions", "Work Decisions"),
            # Numbered list: only the first item's marker is stripped.
            ("1. A\n2. B", "A"),
            # Bullet markers: -, *, bullet char.
            ("- Card Schema Design", "Card Schema Design"),
            ("* Card Schema Design", "Card Schema Design"),
            ("• Card Schema Design", "Card Schema Design"),
            # Digit-leading LEGITIMATE labels must survive untouched: no
            # "." or ")" delimiter after the digits, so this is not a
            # list marker.
            ("3-Phase Feeder Data", "3-Phase Feeder Data"),
            ("2026 Roadmap", "2026 Roadmap"),
            ("5G Network Planning", "5G Network Planning"),
            ("401k Notes", "401k Notes"),
            # Real numbered-list marker: digits + "." or ")" + whitespace.
            ("1. Grid Topology", "Grid Topology"),
            ("2) Grid Topology", "Grid Topology"),
            # Quote / backtick / markdown-bold wrapping is stripped.
            ('"Work Decisions"', "Work Decisions"),
            ("'Work Decisions'", "Work Decisions"),
            ("`Work Decisions`", "Work Decisions"),
            ("**Work Decisions**", "Work Decisions"),
            # Preamble sentence (ends with ":") is skipped in favor of the
            # next non-empty line.
            ("Here are some possible labels:\n\n- Card Schema Design", "Card Schema Design"),
        ],
    )
    def test_extracts_expected_label(self, raw: str, expected: str) -> None:
        assert viz._parse_llm_label(raw) == expected

    @pytest.mark.parametrize("raw", ["", "   ", "\n\n  \t\n"])
    def test_empty_or_whitespace_only_returns_empty_string(self, raw: str) -> None:
        # No usable line: the caller's length gate (2 <= len <= 60) rejects
        # an empty string the same way it would reject a missing response.
        assert viz._parse_llm_label(raw) == ""

    def test_multiline_input_never_leaks_a_newline_into_the_result(self) -> None:
        # Pin the invariant the old "\n" not in label guard used to
        # enforce directly: the parsed result is always a single line.
        raw = "1. Grid Topology\n2. Inverter Control\n3. CSIP Conformance"
        result = viz._parse_llm_label(raw)
        assert "\n" not in result
        assert result == "Grid Topology"


class TestLlmLabelValidationGate:
    """llm_label(): the 2-60 char length gate composed on top of parsing.

    Exercises the None-path end to end via a stubbed requests.post, since
    _parse_llm_label itself only ever returns a string (never None); the
    None contract lives one layer up, in llm_label's post-parse length
    check.
    """

    class _FakeResponse:
        def __init__(self, payload: dict[str, str]) -> None:
            self._payload = payload

        def raise_for_status(self) -> None:
            return None

        def json(self) -> dict[str, str]:
            return self._payload

    def test_empty_response_returns_none(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setattr(
            viz.requests, "post", lambda *a, **kw: self._FakeResponse({"response": ""})
        )
        result = viz.llm_label([{"summary": "x", "content": "x", "tags": []}], {}, "model")
        assert result is None

    def test_over_60_char_label_returns_none(self, monkeypatch: pytest.MonkeyPatch) -> None:
        long_label = "A" * 61
        monkeypatch.setattr(
            viz.requests,
            "post",
            lambda *a, **kw: self._FakeResponse({"response": long_label}),
        )
        result = viz.llm_label([{"summary": "x", "content": "x", "tags": []}], {}, "model")
        assert result is None

    def test_valid_label_is_returned(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setattr(
            viz.requests,
            "post",
            lambda *a, **kw: self._FakeResponse({"response": "IEEE Technical Tests"}),
        )
        result = viz.llm_label([{"summary": "x", "content": "x", "tags": []}], {}, "model")
        assert result == "IEEE Technical Tests"


class TestResolveOllamaModelPrecedence:
    """resolve_ollama_model: OPENBRAIN_EXTRACT_MODEL wins, then FAST, then CHAT."""

    def test_all_three_set_prefers_extract_model(self) -> None:
        cfg = {
            "OPENBRAIN_EXTRACT_MODEL": "gemma3",
            "OPENBRAIN_EXTRACT_MODEL_FAST": "llama3.2:1b",
            "OPENBRAIN_CHAT_MODEL": "mistral:7b",
        }
        assert viz.resolve_ollama_model(cfg) == "gemma3"

    def test_only_fast_and_chat_set_prefers_fast(self) -> None:
        cfg = {
            "OPENBRAIN_EXTRACT_MODEL_FAST": "llama3.2:1b",
            "OPENBRAIN_CHAT_MODEL": "mistral:7b",
        }
        assert viz.resolve_ollama_model(cfg) == "llama3.2:1b"

    def test_only_chat_set_falls_back_to_chat(self) -> None:
        cfg = {"OPENBRAIN_CHAT_MODEL": "mistral:7b"}
        assert viz.resolve_ollama_model(cfg) == "mistral:7b"

    def test_none_set_exits_nonzero(self) -> None:
        with pytest.raises(SystemExit) as exc_info:
            viz.resolve_ollama_model({})
        # sys.exit(str) sets a truthy, non-integer code; assert it is not
        # the "clean exit" 0/None the caller would otherwise get.
        assert exc_info.value.code not in (0, None)
